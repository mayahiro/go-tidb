package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

const snapshotHeader = "-- tidbgo schema snapshot v1; generated from the current database\n\n"

type snapshot struct {
	SQL     string
	tables  int
	managed bool
}
type tableDDL struct {
	name, definition string
	dependencies     []string
}

func capture(ctx context.Context, conn *sql.Conn, database string) (snapshot, error) {
	rows, err := conn.QueryContext(ctx, "SELECT TABLE_NAME, TABLE_TYPE, TABLE_COMMENT FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME", database)
	if err != nil {
		return snapshot{}, err
	}
	var names []string
	managed := false
	for rows.Next() {
		var name, kind, comment string
		if err = rows.Scan(&name, &kind, &comment); err != nil {
			break
		}
		if strings.EqualFold(name, historyTable) {
			if name != historyTable || kind != "BASE TABLE" || comment != historyMarker {
				err = fmt.Errorf("migrate: reserved history table exists without the expected ownership marker")
				break
			}
			managed = true
			continue
		}
		if kind != "BASE TABLE" {
			err = fmt.Errorf("migrate: unsupported schema object %q of type %q; snapshot was not written", name, kind)
			break
		}
		names = append(names, name)
		if len(names) > 10000 {
			err = fmt.Errorf("migrate: snapshot exceeds 10000 tables")
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return snapshot{}, err
	}
	definitions := make(map[string]tableDDL, len(names))
	size := 0
	for _, name := range names {
		var returned, ddl string
		if err := conn.QueryRowContext(ctx, "SHOW CREATE TABLE "+quoteIdentifier(database)+"."+quoteIdentifier(name)).Scan(&returned, &ddl); err != nil {
			return snapshot{}, err
		}
		if returned != name {
			return snapshot{}, fmt.Errorf("migrate: SHOW CREATE TABLE returned an unexpected table")
		}
		ddl, err = normalizedDDL(ddl)
		if err != nil {
			return snapshot{}, err
		}
		size += len(ddl)
		if size > maxSQLSize {
			return snapshot{}, fmt.Errorf("migrate: schema snapshot exceeds 16 MiB")
		}
		ddl, dependencies, err := portableReferences(ddl, database)
		if err != nil {
			return snapshot{}, err
		}
		definitions[strings.ToLower(name)] = tableDDL{name, ddl, dependencies}
	}
	ordered, err := orderTables(definitions)
	if err != nil {
		return snapshot{}, err
	}
	var b strings.Builder
	b.Grow(size + len(names)*3 + len(snapshotHeader))
	b.WriteString(snapshotHeader)
	for _, table := range ordered {
		b.WriteString(table.definition)
		b.WriteString(";\n\n")
	}
	rows, err = conn.QueryContext(ctx, "SELECT TABLE_NAME, REPLICA_COUNT, LOCATION_LABELS FROM information_schema.TIFLASH_REPLICA WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME", database)
	if err != nil {
		return snapshot{}, err
	}
	for rows.Next() {
		var name string
		var count int64
		var labels sql.NullString
		if err = rows.Scan(&name, &count, &labels); err != nil {
			break
		}
		if name == historyTable {
			continue
		}
		if _, ok := definitions[strings.ToLower(name)]; !ok {
			err = fmt.Errorf("migrate: schema changed during snapshot collection")
			break
		}
		if count == 0 {
			continue
		}
		if count != 2 || labels.Valid && labels.String != "" {
			err = fmt.Errorf("migrate: unsupported TiFlash replica configuration")
			break
		}
		fmt.Fprintf(&b, "ALTER TABLE %s SET TIFLASH REPLICA 2;\n\n", quoteIdentifier(name))
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr = rows.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return snapshot{}, err
	}
	source := b.String()
	if len(source) > maxSQLSize {
		return snapshot{}, fmt.Errorf("migrate: schema snapshot exceeds 16 MiB")
	}
	return snapshot{source, len(names), managed}, nil
}

func snapshotHash(source string) (string, error) {
	statements, err := splitSQL(source)
	if err != nil {
		return "", err
	}
	canonical := make([]string, len(statements))
	for i, statement := range statements {
		canonical[i], err = canonicalSQL(statement)
		if err != nil {
			return "", err
		}
	}
	sort.Strings(canonical)
	return checksum(strings.Join(canonical, ";\n")), nil
}

func portableReferences(source, database string) (string, []string, error) {
	ts, err := tokens(source)
	if err != nil {
		return "", nil, err
	}
	var result []string
	var b strings.Builder
	last := 0
	for i := 0; i+1 < len(ts); i++ {
		if ts[i].kind != 'w' || !strings.EqualFold(ts[i].text, "REFERENCES") {
			continue
		}
		name := identifier(ts[i+1])
		i++
		if i+2 < len(ts) && ts[i+1].kind == '.' {
			if !strings.EqualFold(name, database) {
				return "", nil, fmt.Errorf("migrate: cross-database foreign keys cannot be included in a portable snapshot")
			}
			// Same-database qualifiers must not bind a replay in another database
			// back to the original source database.
			b.WriteString(source[last:ts[i].start])
			last = ts[i+2].start
			name = identifier(ts[i+2])
			i += 2
		}
		result = append(result, strings.ToLower(name))
	}
	if last > 0 {
		b.WriteString(source[last:])
		source = b.String()
	}
	return source, result, nil
}

func orderTables(tables map[string]tableDDL) ([]tableDDL, error) {
	keys := make([]string, 0, len(tables))
	for key := range tables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	state := make(map[string]byte, len(tables))
	var result []tableDDL
	var visit func(string) error
	visit = func(key string) error {
		if state[key] == 2 {
			return nil
		}
		if state[key] == 1 {
			return fmt.Errorf("migrate: cyclic foreign keys require a separately designed initial migration")
		}
		table, ok := tables[key]
		if !ok {
			return fmt.Errorf("migrate: a foreign key references a table outside the snapshot")
		}
		state[key] = 1
		dependencies := append([]string(nil), table.dependencies...)
		sort.Strings(dependencies)
		for _, dep := range dependencies {
			if dep != key {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		state[key] = 2
		result = append(result, table)
		return nil
	}
	for _, key := range keys {
		if err := visit(key); err != nil {
			return nil, err
		}
	}
	return result, nil
}
