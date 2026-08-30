package tidbcloud

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

const (
	testDSNEnvironment  = "TIDBGO_TEST_DSN"
	testDatabasePrefix  = "tidbgo_test_"
	testDatabaseMaxSize = 64
)

type starterUser struct {
	model.Meta `tidbgo:"table=tidbgo_it_users"`
	ID         int64 `tidbgo:",pk"`
	Email      string
	Orders     []starterOrder  `tidbgo:"has_many,join=ID:UserID"`
	Profile    *starterProfile `tidbgo:"has_one,join=ID:UserID"`
	Roles      []starterRole   `tidbgo:"many_to_many,through=tidbgo_it_user_roles,source=ID:user_id,target=role_id:ID"`
}

type starterOrder struct {
	model.Meta `tidbgo:"table=tidbgo_it_orders"`
	ID         int64 `tidbgo:",pk"`
	UserID     int64
	Total      starterDecimal
	CreatedAt  time.Time
	User       *starterUser `tidbgo:"belongs_to,join=UserID:ID"`
}

type starterRole struct {
	model.Meta `tidbgo:"table=tidbgo_it_roles"`
	ID         int64 `tidbgo:",pk"`
	Name       string
}

type starterProfile struct {
	model.Meta `tidbgo:"table=tidbgo_it_profiles"`
	ID         int64 `tidbgo:",pk"`
	UserID     int64
	Label      string
}

type starterGenerated struct {
	model.Meta `tidbgo:"table=tidbgo_it_generated"`
	ID         int64 `tidbgo:",pk,auto_random"`
	Name       string
	Score      int64
	GroupID    int64
	RowCount   int64 `tidbgo:"row_count,computed"`
}

type starterUserOrderSummary struct {
	model.Meta    `tidbgo:"table=tidbgo_it_users"`
	UserID        int64 `tidbgo:"user_id"`
	Email         string
	OrderCount    int64 `tidbgo:"order_count,computed"`
	LatestOrderID int64 `tidbgo:"latest_order_id,computed"`
}

type starterOnlyID struct {
	model.Meta `tidbgo:"table=tidbgo_it_only_ids"`
	ID         int64 `tidbgo:",pk,auto_random"`
}

type starterUpsert struct {
	model.Meta `tidbgo:"table=tidbgo_it_upserts"`
	Key        string `tidbgo:"record_key,pk"`
	Payload    string
	Revision   int64
}

type starterLease struct {
	model.Meta `tidbgo:"table=tidbgo_it_leases"`
	ChannelID  int64 `tidbgo:",pk"`
	LockOwner  *string
	LockUntil  *time.Time
	RetryCount int64
	LastError  *string
}

type starterSoftDeleteChannel struct {
	model.Meta `tidbgo:"table=tidbgo_it_soft_delete_channels"`
	ID         int64 `tidbgo:",pk"`
	Name       string
	DeletedAt  time.Time                `tidbgo:",soft_delete"`
	Videos     []starterSoftDeleteVideo `tidbgo:"has_many,join=ID:ChannelID"`
}

type starterSoftDeleteVideo struct {
	model.Meta `tidbgo:"table=tidbgo_it_soft_delete_videos"`
	ID         int64 `tidbgo:",pk"`
	ChannelID  int64
	Title      string
	DeletedAt  time.Time `tidbgo:",soft_delete"`
}

type starterRelationGraph struct {
	model.Meta `tidbgo:"table=tidbgo_it_relation_graphs"`
	ID         int64                   `tidbgo:",pk"`
	NodeAID    int64                   `tidbgo:"node_a_id"`
	NodeBID    int64                   `tidbgo:"node_b_id"`
	NodeCID    int64                   `tidbgo:"node_c_id"`
	NodeA      *starterRelationNode    `tidbgo:"belongs_to,join=NodeAID:ID"`
	NodeB      *starterRelationNode    `tidbgo:"belongs_to,join=NodeBID:ID"`
	NodeC      *starterRelationNode    `tidbgo:"belongs_to,join=NodeCID:ID"`
	DetailA    *starterRelationDetailA `tidbgo:"has_one,join=ID:GraphID"`
	DetailB    *starterRelationDetailB `tidbgo:"has_one,join=ID:GraphID"`
	Tags       []starterRelationTag    `tidbgo:"many_to_many,through=tidbgo_it_relation_graph_tags,source=ID:graph_id,target=tag_id:ID"`
	Children   []starterRelationChild  `tidbgo:"has_many,join=ID:GraphID"`
}

type starterRelationNode struct {
	model.Meta `tidbgo:"table=tidbgo_it_relation_nodes"`
	ID         int64 `tidbgo:",pk"`
	Value      string
}

type starterRelationDetailA struct {
	model.Meta `tidbgo:"table=tidbgo_it_relation_details_a"`
	ID         int64 `tidbgo:",pk"`
	GraphID    int64 `tidbgo:"graph_id"`
	Value      string
}

type starterRelationDetailB struct {
	model.Meta `tidbgo:"table=tidbgo_it_relation_details_b"`
	ID         int64 `tidbgo:",pk"`
	GraphID    int64 `tidbgo:"graph_id"`
	Value      string
}

type starterRelationTag struct {
	model.Meta `tidbgo:"table=tidbgo_it_relation_tags"`
	ID         int64 `tidbgo:",pk"`
	NodeID     int64 `tidbgo:"node_id"`
	Value      string
	Node       *starterRelationNode `tidbgo:"belongs_to,join=NodeID:ID"`
}

type starterRelationChild struct {
	model.Meta `tidbgo:"table=tidbgo_it_relation_children"`
	ID         int64 `tidbgo:",pk"`
	GraphID    int64 `tidbgo:"graph_id"`
	NodeID     int64 `tidbgo:"node_id"`
	Priority   int64
	Node       *starterRelationNode `tidbgo:"belongs_to,join=NodeID:ID"`
}

type starterDecimal struct {
	text string
}

func decimal(text string) starterDecimal {
	return starterDecimal{text: text}
}

func (value *starterDecimal) Scan(source any) error {
	switch source := source.(type) {
	case []byte:
		value.text = string(source)
	case string:
		value.text = source
	default:
		return fmt.Errorf("starter decimal: cannot scan %T", source)
	}
	return nil
}

func (value starterDecimal) Value() (driver.Value, error) {
	return value.text, nil
}

type fixtureTable struct {
	name   string
	create string
	drop   string
}

var fixtureTables = []fixtureTable{
	{
		name: "tidbgo_it_users",
		create: `CREATE TABLE tidbgo_it_users (
  id BIGINT NOT NULL,
  email VARCHAR(255) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY tidbgo_it_users_email (email)
)`,
		drop: "DROP TABLE tidbgo_it_users",
	},
	{
		name: "tidbgo_it_orders",
		create: `CREATE TABLE tidbgo_it_orders (
  id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  total DECIMAL(20, 2) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (id),
  KEY tidbgo_it_orders_user_id (user_id)
)`,
		drop: "DROP TABLE tidbgo_it_orders",
	},
	{
		name: "tidbgo_it_profiles",
		create: `CREATE TABLE tidbgo_it_profiles (
  id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  label VARCHAR(255) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY tidbgo_it_profiles_user_id (user_id)
)`,
		drop: "DROP TABLE tidbgo_it_profiles",
	},
	{
		name: "tidbgo_it_roles",
		create: `CREATE TABLE tidbgo_it_roles (
  id BIGINT NOT NULL,
  name VARCHAR(255) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY tidbgo_it_roles_name (name)
)`,
		drop: "DROP TABLE tidbgo_it_roles",
	},
	{
		name: "tidbgo_it_user_roles",
		create: `CREATE TABLE tidbgo_it_user_roles (
  user_id BIGINT NOT NULL,
  role_id BIGINT NOT NULL,
  PRIMARY KEY (user_id, role_id),
  KEY tidbgo_it_user_roles_role_id (role_id)
)`,
		drop: "DROP TABLE tidbgo_it_user_roles",
	},
	{
		name: "tidbgo_it_generated",
		create: `CREATE TABLE tidbgo_it_generated (
  id BIGINT PRIMARY KEY AUTO_RANDOM(5),
  name VARCHAR(255) NOT NULL,
  score BIGINT NOT NULL,
  group_id BIGINT NOT NULL,
  UNIQUE KEY tidbgo_it_generated_name (name),
  KEY tidbgo_it_generated_group_id (group_id)
)`,
		drop: "DROP TABLE tidbgo_it_generated",
	},
	{
		name: "tidbgo_it_only_ids",
		create: `CREATE TABLE tidbgo_it_only_ids (
  id BIGINT PRIMARY KEY AUTO_RANDOM(5)
)`,
		drop: "DROP TABLE tidbgo_it_only_ids",
	},
	{
		name: "tidbgo_it_upserts",
		create: `CREATE TABLE tidbgo_it_upserts (
  record_key VARCHAR(255) NOT NULL,
  payload VARCHAR(255) NOT NULL,
  revision BIGINT NOT NULL,
  PRIMARY KEY (record_key)
)`,
		drop: "DROP TABLE tidbgo_it_upserts",
	},
	{
		name: "tidbgo_it_leases",
		create: `CREATE TABLE tidbgo_it_leases (
  channel_id BIGINT NOT NULL,
  lock_owner VARCHAR(255) NULL,
  lock_until DATETIME(6) NULL,
  retry_count BIGINT NOT NULL,
  last_error TEXT NULL,
  PRIMARY KEY (channel_id)
)`,
		drop: "DROP TABLE tidbgo_it_leases",
	},
	{
		name: "tidbgo_it_soft_delete_channels",
		create: `CREATE TABLE tidbgo_it_soft_delete_channels (
  id BIGINT NOT NULL,
  name VARCHAR(255) NOT NULL,
  deleted_at DATETIME(6) NULL DEFAULT NULL,
  PRIMARY KEY (id)
)`,
		drop: "DROP TABLE tidbgo_it_soft_delete_channels",
	},
	{
		name: "tidbgo_it_soft_delete_videos",
		create: `CREATE TABLE tidbgo_it_soft_delete_videos (
  id BIGINT NOT NULL,
  channel_id BIGINT NOT NULL,
  title VARCHAR(255) NOT NULL,
  deleted_at DATETIME(6) NULL DEFAULT NULL,
  PRIMARY KEY (id),
  KEY tidbgo_it_soft_delete_videos_channel_id (channel_id)
)`,
		drop: "DROP TABLE tidbgo_it_soft_delete_videos",
	},
	{
		name: "tidbgo_it_relation_graphs",
		create: `CREATE TABLE tidbgo_it_relation_graphs (
  id BIGINT NOT NULL,
  node_a_id BIGINT NOT NULL,
  node_b_id BIGINT NOT NULL,
  node_c_id BIGINT NOT NULL,
  PRIMARY KEY (id)
)`,
		drop: "DROP TABLE tidbgo_it_relation_graphs",
	},
	{
		name: "tidbgo_it_relation_nodes",
		create: `CREATE TABLE tidbgo_it_relation_nodes (
  id BIGINT NOT NULL,
  value VARCHAR(255) NOT NULL,
  PRIMARY KEY (id)
)`,
		drop: "DROP TABLE tidbgo_it_relation_nodes",
	},
	{
		name: "tidbgo_it_relation_details_a",
		create: `CREATE TABLE tidbgo_it_relation_details_a (
  id BIGINT NOT NULL,
  graph_id BIGINT NOT NULL,
  value VARCHAR(255) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY tidbgo_it_relation_details_a_graph_id (graph_id)
)`,
		drop: "DROP TABLE tidbgo_it_relation_details_a",
	},
	{
		name: "tidbgo_it_relation_details_b",
		create: `CREATE TABLE tidbgo_it_relation_details_b (
  id BIGINT NOT NULL,
  graph_id BIGINT NOT NULL,
  value VARCHAR(255) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY tidbgo_it_relation_details_b_graph_id (graph_id)
)`,
		drop: "DROP TABLE tidbgo_it_relation_details_b",
	},
	{
		name: "tidbgo_it_relation_tags",
		create: `CREATE TABLE tidbgo_it_relation_tags (
  id BIGINT NOT NULL,
  node_id BIGINT NOT NULL,
  value VARCHAR(255) NOT NULL,
  PRIMARY KEY (id)
)`,
		drop: "DROP TABLE tidbgo_it_relation_tags",
	},
	{
		name: "tidbgo_it_relation_graph_tags",
		create: `CREATE TABLE tidbgo_it_relation_graph_tags (
  graph_id BIGINT NOT NULL,
  tag_id BIGINT NOT NULL,
  PRIMARY KEY (graph_id, tag_id),
  KEY tidbgo_it_relation_graph_tags_tag_id (tag_id)
)`,
		drop: "DROP TABLE tidbgo_it_relation_graph_tags",
	},
	{
		name: "tidbgo_it_relation_children",
		create: `CREATE TABLE tidbgo_it_relation_children (
  id BIGINT NOT NULL,
  graph_id BIGINT NOT NULL,
  node_id BIGINT NOT NULL,
  priority BIGINT NOT NULL,
  PRIMARY KEY (id),
  KEY tidbgo_it_relation_children_graph_id (graph_id)
)`,
		drop: "DROP TABLE tidbgo_it_relation_children",
	},
}

type fixtureInsert struct {
	statement string
	arguments []any
}

type statementCountingExecutor struct {
	executor orm.QueryExecutor
	count    int
}

func (executor *statementCountingExecutor) QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error) {
	executor.count++
	return executor.executor.QueryContext(ctx, query, arguments...)
}

var fixtureInserts = []fixtureInsert{
	{
		statement: "INSERT INTO tidbgo_it_users (id, email) VALUES (?, ?), (?, ?)",
		arguments: []any{int64(1), "ada@example.test", int64(2), "grace@example.test"},
	},
	{
		statement: "INSERT INTO tidbgo_it_orders (id, user_id, total, created_at) VALUES (?, ?, ?, ?), (?, ?, ?, ?), (?, ?, ?, ?)",
		arguments: []any{
			int64(11), int64(1), decimal("10.50"), fixtureCreatedAt(11),
			int64(12), int64(1), decimal("20.25"), fixtureCreatedAt(12),
			int64(21), int64(2), decimal("30.75"), fixtureCreatedAt(21),
		},
	},
	{
		statement: "INSERT INTO tidbgo_it_profiles (id, user_id, label) VALUES (?, ?, ?)",
		arguments: []any{int64(201), int64(1), "primary"},
	},
	{
		statement: "INSERT INTO tidbgo_it_roles (id, name) VALUES (?, ?), (?, ?)",
		arguments: []any{int64(101), "admin", int64(102), "reader"},
	},
	{
		statement: "INSERT INTO tidbgo_it_user_roles (user_id, role_id) VALUES (?, ?), (?, ?), (?, ?)",
		arguments: []any{int64(1), int64(101), int64(1), int64(102), int64(2), int64(102)},
	},
	{
		statement: "INSERT INTO tidbgo_it_leases (channel_id, lock_owner, lock_until, retry_count, last_error) VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?)",
		arguments: []any{
			int64(1), nil, nil, int64(0), "previous failure",
			int64(2), "existing-worker", fixtureCreatedAt(23), int64(2), nil,
		},
	},
	{
		statement: "INSERT INTO tidbgo_it_soft_delete_channels (id, name, deleted_at) VALUES (?, ?, ?), (?, ?, ?)",
		arguments: []any{int64(1), "active", nil, int64(2), "deleted", fixtureCreatedAt(8)},
	},
	{
		statement: "INSERT INTO tidbgo_it_soft_delete_videos (id, channel_id, title, deleted_at) VALUES (?, ?, ?, ?), (?, ?, ?, ?)",
		arguments: []any{
			int64(101), int64(1), "active", nil,
			int64(102), int64(1), "deleted", fixtureCreatedAt(9),
		},
	},
	{
		statement: "INSERT INTO tidbgo_it_relation_graphs (id, node_a_id, node_b_id, node_c_id) VALUES (?, ?, ?, ?)",
		arguments: []any{int64(1), int64(10), int64(20), int64(30)},
	},
	{
		statement: "INSERT INTO tidbgo_it_relation_nodes (id, value) VALUES (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?)",
		arguments: []any{
			int64(10), "a", int64(20), "b", int64(30), "c",
			int64(80), "child-a", int64(81), "child-b", int64(82), "child-c",
			int64(90), "tag-a", int64(91), "tag-b", int64(92), "tag-c",
		},
	},
	{
		statement: "INSERT INTO tidbgo_it_relation_details_a (id, graph_id, value) VALUES (?, ?, ?)",
		arguments: []any{int64(40), int64(1), "detail-a"},
	},
	{
		statement: "INSERT INTO tidbgo_it_relation_details_b (id, graph_id, value) VALUES (?, ?, ?)",
		arguments: []any{int64(50), int64(1), "detail-b"},
	},
	{
		statement: "INSERT INTO tidbgo_it_relation_tags (id, node_id, value) VALUES (?, ?, ?), (?, ?, ?), (?, ?, ?)",
		arguments: []any{int64(60), int64(90), "tag-a", int64(61), int64(91), "tag-b", int64(62), int64(92), "tag-c"},
	},
	{
		statement: "INSERT INTO tidbgo_it_relation_graph_tags (graph_id, tag_id) VALUES (?, ?), (?, ?), (?, ?)",
		arguments: []any{int64(1), int64(60), int64(1), int64(61), int64(1), int64(62)},
	},
	{
		statement: "INSERT INTO tidbgo_it_relation_children (id, graph_id, node_id, priority) VALUES (?, ?, ?, ?), (?, ?, ?, ?), (?, ?, ?, ?)",
		arguments: []any{
			int64(70), int64(1), int64(80), int64(1),
			int64(71), int64(1), int64(81), int64(2),
			int64(72), int64(1), int64(82), int64(3),
		},
	},
}

func TestMySQLDriverRegistered(t *testing.T) {
	t.Parallel()

	if !slices.Contains(sql.Drivers(), "mysql") {
		t.Fatal("mysql database/sql driver is not registered")
	}
}

func TestTestDatabaseName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		database string
		valid    bool
	}{
		{name: "dedicated database", database: "tidbgo_test_ci", valid: true},
		{name: "numeric suffix", database: "tidbgo_test_20260829", valid: true},
		{name: "maximum length", database: testDatabasePrefix + strings.Repeat("a", testDatabaseMaxSize-len(testDatabasePrefix)), valid: true},
		{name: "empty", database: "", valid: false},
		{name: "missing suffix", database: "tidbgo_test_", valid: false},
		{name: "wrong prefix", database: "application", valid: false},
		{name: "uppercase", database: "tidbgo_test_CI", valid: false},
		{name: "punctuation", database: "tidbgo_test_ci-blue", valid: false},
		{name: "too long", database: testDatabasePrefix + strings.Repeat("a", testDatabaseMaxSize-len(testDatabasePrefix)+1), valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := validTestDatabaseName(test.database); got != test.valid {
				t.Fatalf("validTestDatabaseName(%q) = %v, want %v", test.database, got, test.valid)
			}
		})
	}
}

func TestTiDBCloudStarter(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("TIDBGO_TEST_DSN is not set; skipping the connected TiDB Cloud Starter test")
	}
	config := parseTestDSN(t, dsn)
	if !config.ParseTime {
		t.Fatal("TIDBGO_TEST_DSN must include parseTime=true so DATETIME fields can scan into time.Time")
	}
	t.Logf("validated DSN options: parseTime=%t interpolateParams=%t", config.ParseTime, config.InterpolateParams)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	database := openTestDatabase(t, dsn)

	if err := database.PingContext(ctx); err != nil {
		fatalDatabaseError(t, dsn, "connect to TiDB Cloud Starter", err)
	}
	verifyConnectedTarget(t, ctx, database, dsn)
	installFixture(t, ctx, database, dsn)

	t.Run("scalar terminals and custom decimal", func(t *testing.T) {
		testScalarTerminals(t, ctx, database, dsn)
	})
	t.Run("temporal field with parseTime", func(t *testing.T) {
		testTemporalField(t, ctx, database, config, dsn)
	})
	t.Run("direct and many-to-many preload", func(t *testing.T) {
		testPreloads(t, ctx, database, dsn)
	})
	t.Run("three-statement relation graph preload", func(t *testing.T) {
		testRelationGraphPreload(t, ctx, database, dsn)
	})
	t.Run("direct and many-to-many relation predicates", func(t *testing.T) {
		testRelationPredicates(t, ctx, database, dsn)
	})
	t.Run("preload in caller transaction", func(t *testing.T) {
		testTransactionPreload(t, ctx, database, dsn)
	})
	t.Run("pure many-to-many relation mutations", func(t *testing.T) {
		testRelationMutations(t, ctx, database, dsn)
	})
	t.Run("conditional update assignments", func(t *testing.T) {
		testConditionalUpdates(t, ctx, database, dsn)
	})
	t.Run("soft delete filters preload mutations and restore", func(t *testing.T) {
		testSoftDeletes(t, ctx, database, dsn)
	})
	t.Run("mutations AUTO_RANDOM raw query and transaction helper", func(t *testing.T) {
		testMutationsAndRawQuery(t, ctx, database, dsn)
	})
	t.Run("single and bulk upsert", func(t *testing.T) {
		testUpserts(t, ctx, database, dsn)
	})
	t.Run("statement observation", func(t *testing.T) {
		testStatementObservation(t, ctx, database, dsn)
	})
	t.Run("debug report", func(t *testing.T) {
		testDebugReport(t, ctx, database, dsn)
	})
	t.Run("same-session ServerRU", func(t *testing.T) {
		testServerRU(t, ctx, database, dsn)
	})
	t.Run("SELECT EXPLAIN", func(t *testing.T) {
		testSelectExplain(t, ctx, database, dsn)
	})
	t.Run("SELECT EXPLAIN ANALYZE", func(t *testing.T) {
		testSelectExplainAnalyze(t, ctx, database, dsn)
	})
}

func testSelectExplain(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	plan, err := orm.Query[starterOrder]().
		Select("ID", "UserID", "Total").
		Where(orm.Equal("ID", int64(11))).
		Explain(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "explain starter order SELECT", err)
	}
	if len(plan) == 0 {
		t.Fatal("SELECT EXPLAIN returned an empty plan")
	}
	foundTable := false
	for index, row := range plan {
		if row.ID == "" || row.Task == "" || row.EstRows < 0 {
			t.Fatalf("SELECT EXPLAIN row %d = %#v", index, row)
		}
		if strings.Contains(row.AccessObject, "table:tidbgo_it_orders") {
			foundTable = true
		}
	}
	if !foundTable {
		t.Fatalf("SELECT EXPLAIN plan does not reference tidbgo_it_orders: %#v", plan)
	}
}

func testSelectExplainAnalyze(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	plan, err := orm.Query[starterOrder]().
		Select("ID", "UserID", "Total").
		Where(orm.Equal("ID", int64(11))).
		ExplainAnalyze(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "explain analyze starter order SELECT", err)
	}
	if len(plan) == 0 {
		t.Fatal("SELECT EXPLAIN ANALYZE returned an empty plan")
	}
	foundTable := false
	foundActualRootRow := false
	foundRU := false
	for index, row := range plan {
		if row.ID == "" || row.Task == "" || row.EstRows < 0 || row.ActRows < 0 || row.ExecutionInfo == "" || row.Memory == "" || row.Disk == "" {
			t.Fatalf("SELECT EXPLAIN ANALYZE row %d = %#v", index, row)
		}
		if strings.Contains(row.AccessObject, "table:tidbgo_it_orders") {
			foundTable = true
		}
		if row.Task == "root" && row.ActRows == 1 {
			foundActualRootRow = true
		}
		if strings.Contains(row.ExecutionInfo, "RU:") {
			foundRU = true
		}
	}
	if !foundTable || !foundActualRootRow || !foundRU {
		t.Fatalf("SELECT EXPLAIN ANALYZE plan lacks table, actual root row, or RU data: %#v", plan)
	}
}

func testServerRU(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "reserve connection for ServerRU", err)
	}
	defer func() {
		if err := connection.Close(); err != nil {
			t.Errorf("close ServerRU connection: %s", redact.Error(err, dsn))
		}
	}()

	if _, err := orm.Query[starterOrder]().
		Where(orm.Equal("ID", int64(11))).
		Only(ctx, connection); err != nil {
		fatalDatabaseError(t, dsn, "execute ServerRU connection query", err)
	}
	serverRU, err := orm.LastServerRU(ctx, connection)
	if err != nil {
		fatalDatabaseError(t, dsn, "read connection ServerRU", err)
	}
	if serverRU <= 0 {
		t.Fatalf("connection ServerRU = %v, want a positive value", serverRU)
	}

	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		fatalDatabaseError(t, dsn, "begin ServerRU transaction", err)
	}
	if _, err := orm.Query[starterOrder]().
		Where(orm.Equal("ID", int64(12))).
		Only(ctx, transaction); err != nil {
		_ = transaction.Rollback()
		fatalDatabaseError(t, dsn, "execute ServerRU transaction query", err)
	}
	transactionRU, err := orm.LastServerRU(ctx, transaction)
	if err != nil {
		_ = transaction.Rollback()
		fatalDatabaseError(t, dsn, "read transaction ServerRU", err)
	}
	if transactionRU <= 0 {
		_ = transaction.Rollback()
		t.Fatalf("transaction ServerRU = %v, want a positive value", transactionRU)
	}
	if err := transaction.Rollback(); err != nil {
		fatalDatabaseError(t, dsn, "roll back ServerRU transaction", err)
	}
}

func testConditionalUpdates(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	now := fixtureCreatedAt(16)
	leaseUntil := now.Add(5 * time.Minute)
	affected, err := orm.UpdateWhere[starterLease](
		orm.Set("LockOwner", "worker-a"),
		orm.Set("LockUntil", leaseUntil),
	).Where(
		orm.In("ChannelID", []int64{1, 2}),
		orm.Or(orm.IsNull("LockUntil"), orm.LessThanOrEqual("LockUntil", now)),
	).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "claim an available lease through a conditional UPDATE", err)
	}
	if affected != 1 {
		t.Fatalf("conditional claim affected rows = %d, want 1", affected)
	}

	claimed, err := orm.Query[starterLease]().Where(orm.Equal("ChannelID", int64(1))).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the claimed lease", err)
	}
	if claimed.LockOwner == nil || *claimed.LockOwner != "worker-a" || claimed.LockUntil == nil || !claimed.LockUntil.Equal(leaseUntil) || claimed.RetryCount != 0 {
		t.Fatalf("claimed lease = %#v", claimed)
	}
	locked, err := orm.Query[starterLease]().Where(orm.Equal("ChannelID", int64(2))).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the unavailable lease", err)
	}
	if locked.LockOwner == nil || *locked.LockOwner != "existing-worker" || locked.RetryCount != 2 {
		t.Fatalf("unavailable lease changed = %#v", locked)
	}

	affected, err = orm.UpdateWhere[starterLease](
		orm.Set("LastError", "network failure"),
		orm.Set("LockOwner", nil),
		orm.Set("LockUntil", nil),
		orm.Increment("RetryCount", int64(1)),
	).Where(
		orm.Equal("ChannelID", int64(1)),
		orm.Equal("LockOwner", "worker-a"),
	).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "finish a failed lease through a conditional UPDATE", err)
	}
	if affected != 1 {
		t.Fatalf("conditional failure affected rows = %d, want 1", affected)
	}
	failed, err := orm.Query[starterLease]().Where(orm.Equal("ChannelID", int64(1))).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the failed lease", err)
	}
	if failed.LockOwner != nil || failed.LockUntil != nil || failed.LastError == nil || *failed.LastError != "network failure" || failed.RetryCount != 1 {
		t.Fatalf("failed lease = %#v", failed)
	}

	affected, err = orm.UpdateWhere[starterLease](
		orm.Set("LastError", nil),
	).Where(
		orm.Equal("ChannelID", int64(1)),
		orm.Equal("LockOwner", "stale-worker"),
	).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "reject a stale lease owner through a conditional UPDATE", err)
	}
	if affected != 0 {
		t.Fatalf("stale conditional update affected rows = %d, want 0", affected)
	}

	affected, err = orm.UpdateWhere[starterOrder](
		orm.Increment("Total", decimal("1.25")),
	).Where(orm.Equal("ID", int64(11))).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "increment an application-selected DECIMAL through a conditional UPDATE", err)
	}
	if affected != 1 {
		t.Fatalf("DECIMAL increment affected rows = %d, want 1", affected)
	}
	order, err := orm.Query[starterOrder]().Where(orm.Equal("ID", int64(11))).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the incremented DECIMAL", err)
	}
	if order.Total.text != "11.75" {
		t.Fatalf("incremented DECIMAL = %q, want 11.75", order.Total.text)
	}
}

func testSoftDeletes(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	active, err := orm.Query[starterSoftDeleteChannel]().Preload("Videos").OrderBy(orm.Asc("ID")).All(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load active soft-delete channels", err)
	}
	if len(active) != 1 || active[0].ID != 1 || !active[0].DeletedAt.IsZero() || len(active[0].Videos) != 1 || active[0].Videos[0].ID != 101 {
		t.Fatalf("active soft-delete channels = %#v", active)
	}

	allChannels, err := orm.Query[starterSoftDeleteChannel]().WithDeleted().OrderBy(orm.Asc("ID")).All(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load all soft-delete channels", err)
	}
	if len(allChannels) != 2 || allChannels[0].ID != 1 || allChannels[1].ID != 2 || allChannels[1].DeletedAt.IsZero() {
		t.Fatalf("all soft-delete channels = %#v", allChannels)
	}

	channel, err := orm.Query[starterSoftDeleteChannel]().Where(orm.Equal("ID", int64(1))).Preload("Videos").Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "preload active soft-delete videos", err)
	}
	if len(channel.Videos) != 1 || channel.Videos[0].ID != 101 {
		t.Fatalf("active preloaded videos = %#v", channel.Videos)
	}
	channel, err = orm.Query[starterSoftDeleteChannel]().Where(orm.Equal("ID", int64(1))).Preload(
		"Videos",
		orm.PreloadWithDeleted(),
		orm.PreloadOrderBy(orm.Asc("ID")),
	).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "preload all soft-delete videos", err)
	}
	if len(channel.Videos) != 2 || channel.Videos[0].ID != 101 || channel.Videos[1].ID != 102 || channel.Videos[1].DeletedAt.IsZero() {
		t.Fatalf("all preloaded videos = %#v", channel.Videos)
	}

	hasDeletedVideo, err := orm.Query[starterSoftDeleteChannel]().Where(
		orm.Equal("ID", int64(1)),
		orm.Has("Videos", orm.Equal("Title", "deleted")),
	).Exists(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "check a relation predicate against a deleted target", err)
	}
	if hasDeletedVideo {
		t.Fatal("relation predicate matched a soft-deleted target")
	}

	video := starterSoftDeleteVideo{ID: 101}
	affected, err := orm.Delete(&video).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "soft delete a video", err)
	}
	if affected != 1 || !video.DeletedAt.IsZero() {
		t.Fatalf("soft delete affected = %d, caller model = %#v", affected, video)
	}
	deletedVideo, err := orm.Query[starterSoftDeleteVideo]().WithDeleted().Where(orm.Equal("ID", int64(101))).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the soft-deleted video", err)
	}
	if deletedVideo.ID != 101 || deletedVideo.DeletedAt.IsZero() {
		t.Fatalf("soft-deleted video = %#v", deletedVideo)
	}

	deletedVideo.Title = "blocked update"
	affected, err = orm.Update(&deletedVideo, "Title").Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "guard a primary-key update from a deleted row", err)
	}
	if affected != 0 {
		t.Fatalf("guarded update affected rows = %d, want 0", affected)
	}

	affected, err = orm.UpdateWhere[starterSoftDeleteVideo](
		orm.Set("DeletedAt", time.Time{}),
	).WithDeleted().Where(orm.Equal("ID", int64(101))).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "restore a soft-deleted video", err)
	}
	if affected != 1 {
		t.Fatalf("restore affected rows = %d, want 1", affected)
	}
	restored, err := orm.Query[starterSoftDeleteVideo]().Where(orm.Equal("ID", int64(101))).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the restored video", err)
	}
	if !restored.DeletedAt.IsZero() || restored.Title != "active" {
		t.Fatalf("restored video = %#v", restored)
	}

	if _, err := orm.Delete(&restored).Exec(ctx, database); err != nil {
		fatalDatabaseError(t, dsn, "soft delete the video before single upsert restore", err)
	}
	restored.Title = "single restored"
	restored.DeletedAt = time.Time{}
	affected, err = orm.Upsert(&restored).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "restore a soft-deleted video through single upsert", err)
	}
	if affected == 0 {
		t.Fatal("single upsert restore reported no affected rows")
	}
	restored, err = orm.Query[starterSoftDeleteVideo]().Where(orm.Equal("ID", int64(101))).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the single-upsert-restored video", err)
	}
	if restored.Title != "single restored" || !restored.DeletedAt.IsZero() {
		t.Fatalf("single-upsert-restored video = %#v", restored)
	}

	if _, err := orm.Delete(&restored).Exec(ctx, database); err != nil {
		fatalDatabaseError(t, dsn, "soft delete the video before bulk upsert restore", err)
	}
	affected, err = orm.UpsertMany([]starterSoftDeleteVideo{{
		ID:        101,
		ChannelID: 1,
		Title:     "bulk restored",
	}}).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "restore a soft-deleted video through bulk upsert", err)
	}
	if affected == 0 {
		t.Fatal("bulk upsert restore reported no affected rows")
	}
	restored, err = orm.Query[starterSoftDeleteVideo]().Where(orm.Equal("ID", int64(101))).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the bulk-upsert-restored video", err)
	}
	if restored.Title != "bulk restored" || !restored.DeletedAt.IsZero() {
		t.Fatalf("bulk-upsert-restored video = %#v", restored)
	}

	affected, err = orm.DeleteWhere[starterSoftDeleteVideo](orm.Equal("ChannelID", int64(1))).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "soft delete active videos by predicate", err)
	}
	if affected != 1 {
		t.Fatalf("predicate soft delete affected rows = %d, want 1", affected)
	}
	remaining, err := orm.Query[starterSoftDeleteVideo]().Where(orm.Equal("ChannelID", int64(1))).Count(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "count active videos after predicate soft delete", err)
	}
	if remaining != 0 {
		t.Fatalf("active videos after predicate soft delete = %d, want 0", remaining)
	}
}

func fixtureCreatedAt(hour int) time.Time {
	return time.Date(2026, time.August, 29, hour, 34, 56, 123456000, time.UTC)
}

func parseTestDSN(t testing.TB, dsn string) *mysql.Config {
	t.Helper()

	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse TIDBGO_TEST_DSN: %s", redact.Error(err, dsn))
	}
	return config
}

func openTestDatabase(t testing.TB, dsn string) *sql.DB {
	t.Helper()

	database, err := sql.Open("mysql", dsn)
	if err != nil {
		fatalDatabaseError(t, dsn, "open the database", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(4 * time.Minute)
	database.SetConnMaxIdleTime(time.Minute)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close the database: %s", redact.Error(err, dsn))
		}
	})
	return database
}

func validTestDatabaseName(name string) bool {
	if len(name) <= len(testDatabasePrefix) || len(name) > testDatabaseMaxSize || !strings.HasPrefix(name, testDatabasePrefix) {
		return false
	}
	for index := range len(name) {
		character := name[index]
		if character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func verifyConnectedTarget(t testing.TB, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	var databaseName sql.NullString
	if err := database.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&databaseName); err != nil {
		fatalDatabaseError(t, dsn, "read the current database", err)
	}
	if !databaseName.Valid || !validTestDatabaseName(databaseName.String) {
		t.Fatalf("refusing fixture DDL: the selected database must have a lowercase %q prefix and a non-empty suffix", testDatabasePrefix)
	}

	var version string
	if err := database.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		fatalDatabaseError(t, dsn, "read the server version", err)
	}
	if !strings.Contains(strings.ToLower(version), "tidb") {
		t.Fatal("the connected server does not identify itself as TiDB")
	}
}

func installFixture(t testing.TB, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	created := make([]fixtureTable, 0, len(fixtureTables))
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for index := len(created) - 1; index >= 0; index-- {
			if _, err := database.ExecContext(cleanupContext, created[index].drop); err != nil {
				t.Errorf("drop integration fixture table %s: %s", created[index].name, redact.Error(err, dsn))
			}
		}
	})

	for _, table := range fixtureTables {
		if _, err := database.ExecContext(ctx, table.create); err != nil {
			fatalDatabaseError(t, dsn, "create integration fixture table "+table.name+"; the test never removes a pre-existing table", err)
		}
		created = append(created, table)
	}
	for _, insert := range fixtureInserts {
		if _, err := database.ExecContext(ctx, insert.statement, insert.arguments...); err != nil {
			fatalDatabaseError(t, dsn, "insert integration fixture rows", err)
		}
	}
}

func testScalarTerminals(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	orders, err := orm.Query[starterOrder]().
		Where(orm.Equal("UserID", int64(1))).
		OrderBy(orm.Asc("ID")).
		All(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load scalar orders", err)
	}
	requireIDs(t, orderIDs(orders), 11, 12)
	if orders[0].Total.text != "10.50" || orders[1].Total.text != "20.25" {
		t.Fatalf("scanned DECIMAL values = %q and %q, want 10.50 and 20.25", orders[0].Total.text, orders[1].Total.text)
	}

	first, err := orm.Query[starterOrder]().
		Where(orm.Equal("UserID", int64(1))).
		OrderBy(orm.Desc("ID")).
		First(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the first order", err)
	}
	if first.ID != 12 {
		t.Fatalf("first order ID = %d, want 12", first.ID)
	}

	only, err := orm.Query[starterUser]().
		Where(orm.Equal("Email", "ada@example.test")).
		Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load exactly one user", err)
	}
	if only.ID != 1 {
		t.Fatalf("only user ID = %d, want 1", only.ID)
	}

	exists, err := orm.Query[starterOrder]().
		Where(orm.Equal("Total", decimal("20.25"))).
		Exists(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "check an order with a custom decimal argument", err)
	}
	if !exists {
		t.Fatal("custom decimal predicate did not find the fixture order")
	}

	count, err := orm.Query[starterOrder]().
		Where(orm.Equal("UserID", int64(1))).
		Count(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "count fixture orders", err)
	}
	if count != 2 {
		t.Fatalf("order count = %d, want 2", count)
	}

	selected, err := orm.Query[starterOrder]().
		Select("ID").
		Where(orm.In("ID", []int64{11, 21})).
		OrderBy(orm.Asc("ID")).
		All(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load orders from a typed IN slice", err)
	}
	requireIDs(t, orderIDs(selected), 11, 21)
}

func testTemporalField(t *testing.T, ctx context.Context, database *sql.DB, config *mysql.Config, dsn string) {
	t.Helper()

	order, err := orm.Query[starterOrder]().
		Where(orm.Equal("ID", int64(11))).
		Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load a DATETIME field into time.Time", err)
	}
	want := fixtureCreatedAt(11).Format("2006-01-02 15:04:05.000000")
	if got := order.CreatedAt.Format("2006-01-02 15:04:05.000000"); got != want {
		t.Fatalf("created_at wall clock = %s, want %s", got, want)
	}

	withoutParseTime := config.Clone()
	withoutParseTime.ParseTime = false
	withoutParseTimeDSN := withoutParseTime.FormatDSN()
	withoutParseTimeDatabase := openTestDatabase(t, withoutParseTimeDSN)
	if err := withoutParseTimeDatabase.PingContext(ctx); err != nil {
		fatalDatabaseError(t, withoutParseTimeDSN, "connect without parseTime", err)
	}
	var unsupported time.Time
	err = withoutParseTimeDatabase.QueryRowContext(
		ctx,
		"SELECT created_at FROM tidbgo_it_orders WHERE id = ?",
		int64(11),
	).Scan(&unsupported)
	if err == nil {
		t.Fatal("DATETIME unexpectedly scanned into time.Time with parseTime=false")
	}
}

func testPreloads(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	collectionExecutor := &statementCountingExecutor{executor: database}
	users, err := orm.Query[starterUser]().
		Select("Email").
		Preload("Orders").
		Preload("Roles").
		OrderBy(orm.Asc("ID")).
		All(ctx, collectionExecutor)
	if err != nil {
		fatalDatabaseError(t, dsn, "preload direct and many-to-many relations", err)
	}
	if len(users) != 2 {
		t.Fatalf("preloaded user count = %d, want 2", len(users))
	}
	requireIDs(t, []int64{users[0].ID, users[1].ID}, 1, 2)
	requireIDs(t, orderIDs(users[0].Orders), 11, 12)
	requireIDs(t, orderIDs(users[1].Orders), 21)
	requireIDs(t, roleIDs(users[0].Roles), 101, 102)
	requireIDs(t, roleIDs(users[1].Roles), 102)
	if collectionExecutor.count != 3 {
		t.Fatalf("collection preload statement count = %d, want 3", collectionExecutor.count)
	}

	nestedExecutor := &statementCountingExecutor{executor: database}
	nested, err := orm.Query[starterUser]().
		Select("ID").
		Preload("Orders", orm.PreloadFields("ID", "Total"), orm.PreloadOrderBy(orm.Desc("ID"))).
		Preload("Orders.User").
		Where(orm.Equal("ID", int64(1))).
		Only(ctx, nestedExecutor)
	if err != nil {
		fatalDatabaseError(t, dsn, "preload a projected and ordered nested relation", err)
	}
	if len(nested.Orders) != 2 || nested.Orders[0].ID != 12 || nested.Orders[1].ID != 11 {
		t.Fatalf("nested ordered orders = %#v, want IDs 12 and 11", nested.Orders)
	}
	for _, order := range nested.Orders {
		if order.User == nil || order.User.ID != 1 || order.User.Email != "ada@example.test" {
			t.Fatalf("nested user for order %d = %#v", order.ID, order.User)
		}
	}
	if nestedExecutor.count != 2 {
		t.Fatalf("nested preload statement count = %d, want 2", nestedExecutor.count)
	}

	belongsToExecutor := &statementCountingExecutor{executor: database}
	order, err := orm.Query[starterOrder]().
		Preload("User").
		Where(orm.Equal("ID", int64(11))).
		Only(ctx, belongsToExecutor)
	if err != nil {
		fatalDatabaseError(t, dsn, "preload an inline belongs-to relation", err)
	}
	if order.User == nil || order.User.ID != 1 || order.User.Email != "ada@example.test" {
		t.Fatalf("inline belongs-to user = %#v", order.User)
	}
	if belongsToExecutor.count != 1 {
		t.Fatalf("belongs-to preload statement count = %d, want 1", belongsToExecutor.count)
	}

	hasOneExecutor := &statementCountingExecutor{executor: database}
	profileUsers, err := orm.Query[starterUser]().
		Select("ID").
		Preload("Profile").
		OrderBy(orm.Asc("ID")).
		All(ctx, hasOneExecutor)
	if err != nil {
		fatalDatabaseError(t, dsn, "preload an inline has-one relation", err)
	}
	if len(profileUsers) != 2 || profileUsers[0].Profile == nil || profileUsers[0].Profile.Label != "primary" || profileUsers[1].Profile != nil {
		t.Fatalf("inline has-one users = %#v", profileUsers)
	}
	if hasOneExecutor.count != 1 {
		t.Fatalf("has-one preload statement count = %d, want 1", hasOneExecutor.count)
	}
}

func testRelationPredicates(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	buyers, err := orm.Query[starterUser]().
		Select("ID", "Email").
		Where(orm.Has("Orders", orm.GreaterThan("Total", decimal("25.00")))).
		All(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "filter users through a direct relation", err)
	}
	if len(buyers) != 1 || buyers[0].ID != 2 {
		t.Fatalf("direct relation predicate users = %#v, want user 2", buyers)
	}
	if buyers[0].Orders != nil || buyers[0].Roles != nil {
		t.Fatalf("direct relation predicate hydrated relations = Orders %#v, Roles %#v, want nil", buyers[0].Orders, buyers[0].Roles)
	}

	admins, err := orm.Query[starterUser]().
		Select("ID", "Email").
		Where(orm.Has("Roles", orm.Equal("Name", "admin"))).
		All(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "filter users through a many-to-many relation", err)
	}
	if len(admins) != 1 || admins[0].ID != 1 {
		t.Fatalf("many-to-many relation predicate users = %#v, want user 1", admins)
	}
	if admins[0].Orders != nil || admins[0].Roles != nil {
		t.Fatalf("many-to-many relation predicate hydrated relations = Orders %#v, Roles %#v, want nil", admins[0].Orders, admins[0].Roles)
	}

	count, err := orm.Query[starterUser]().Where(orm.Has("Orders")).Count(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "count users through a relation", err)
	}
	if count != 2 {
		t.Fatalf("relation predicate user count = %d, want 2", count)
	}
}

func relationGraphQuery() *orm.SelectQuery[starterRelationGraph] {
	return orm.Query[starterRelationGraph]().
		Select("ID", "NodeAID", "NodeBID", "NodeCID").
		Preload("NodeA").
		Preload("NodeB").
		Preload("NodeC").
		Preload("DetailA").
		Preload("DetailB").
		Preload("Tags", orm.PreloadOrderBy(orm.Asc("ID"))).
		Preload("Tags.Node").
		Preload("Children", orm.PreloadOrderBy(orm.Asc("Priority"))).
		Preload("Children.Node").
		Where(orm.Equal("ID", int64(1)))
}

func testRelationGraphPreload(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	executor := &statementCountingExecutor{executor: database}
	graph, err := relationGraphQuery().Only(ctx, executor)
	if err != nil {
		fatalDatabaseError(t, dsn, "preload the representative relation graph", err)
	}
	if executor.count != 3 {
		t.Fatalf("relation graph statement count = %d, want 3", executor.count)
	}
	if graph.NodeA == nil || graph.NodeA.Value != "a" || graph.NodeB == nil || graph.NodeB.Value != "b" || graph.NodeC == nil || graph.NodeC.Value != "c" {
		t.Fatalf("relation graph belongs-to values = %#v, %#v, %#v", graph.NodeA, graph.NodeB, graph.NodeC)
	}
	if graph.DetailA == nil || graph.DetailA.Value != "detail-a" || graph.DetailB == nil || graph.DetailB.Value != "detail-b" {
		t.Fatalf("relation graph has-one values = %#v, %#v", graph.DetailA, graph.DetailB)
	}
	if len(graph.Tags) != 3 || graph.Tags[0].Node == nil || graph.Tags[2].Node == nil {
		t.Fatalf("relation graph tags = %#v", graph.Tags)
	}
	if len(graph.Children) != 3 || graph.Children[0].Node == nil || graph.Children[2].Node == nil {
		t.Fatalf("relation graph children = %#v", graph.Children)
	}
}

func testTransactionPreload(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		fatalDatabaseError(t, dsn, "begin the preload transaction", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	users, err := orm.Query[starterUser]().
		Select("ID").
		Preload("Roles").
		OrderBy(orm.Asc("ID")).
		All(ctx, transaction)
	if err != nil {
		fatalDatabaseError(t, dsn, "preload in the caller transaction", err)
	}
	if len(users) != 2 {
		t.Fatalf("transaction user count = %d, want 2", len(users))
	}
	requireIDs(t, roleIDs(users[0].Roles), 101, 102)
	requireIDs(t, roleIDs(users[1].Roles), 102)
	if err := transaction.Rollback(); err != nil {
		fatalDatabaseError(t, dsn, "roll back the preload transaction", err)
	}
}

func testRelationMutations(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	if _, err := orm.AddRelation[starterUser]("Roles", int64(1), int64(101)).Exec(ctx, database); err == nil {
		t.Fatal("default relation add accepted an existing junction key")
	}
	if _, err := orm.AddRelation[starterUser]("Roles", int64(1), int64(101), int64(102)).
		IgnoreExisting().
		Exec(ctx, database); err != nil {
		fatalDatabaseError(t, dsn, "keep existing relation junction rows", err)
	}
	var fixtureCount int64
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM tidbgo_it_user_roles").Scan(&fixtureCount); err != nil {
		fatalDatabaseError(t, dsn, "count relation rows after duplicate-preserving add", err)
	}
	if fixtureCount != 3 {
		t.Fatalf("junction row count after duplicate-preserving add = %d, want 3", fixtureCount)
	}

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		fatalDatabaseError(t, dsn, "begin the relation mutation transaction", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	affected, err := orm.RemoveRelation[starterUser]("Roles", int64(2), int64(102)).Exec(ctx, transaction)
	if err != nil {
		fatalDatabaseError(t, dsn, "remove one existing relation", err)
	}
	if affected != 1 {
		t.Fatalf("initial relation remove affected rows = %d, want 1", affected)
	}
	affected, err = orm.AddRelation[starterUser]("Roles", int64(2), int64(101), int64(102)).Exec(ctx, transaction)
	if err != nil {
		fatalDatabaseError(t, dsn, "add two relations in one statement", err)
	}
	if affected != 2 {
		t.Fatalf("relation add affected rows = %d, want 2", affected)
	}
	affected, err = orm.RemoveRelation[starterUser]("Roles", int64(2), int64(101)).Exec(ctx, transaction)
	if err != nil {
		fatalDatabaseError(t, dsn, "remove selected relation", err)
	}
	if affected != 1 {
		t.Fatalf("selected relation remove affected rows = %d, want 1", affected)
	}
	affected, err = orm.ClearRelation[starterUser]("Roles", int64(2)).Exec(ctx, transaction)
	if err != nil {
		fatalDatabaseError(t, dsn, "clear source relations", err)
	}
	if affected != 1 {
		t.Fatalf("relation clear affected rows = %d, want 1", affected)
	}
	var remaining int64
	if err := transaction.QueryRowContext(ctx, "SELECT COUNT(*) FROM tidbgo_it_user_roles WHERE user_id = ?", int64(2)).Scan(&remaining); err != nil {
		fatalDatabaseError(t, dsn, "count cleared relation rows", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining relation rows = %d, want 0", remaining)
	}
	if err := transaction.Rollback(); err != nil {
		fatalDatabaseError(t, dsn, "roll back relation mutations", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM tidbgo_it_user_roles").Scan(&fixtureCount); err != nil {
		fatalDatabaseError(t, dsn, "verify relation mutation rollback", err)
	}
	if fixtureCount != 3 {
		t.Fatalf("junction row count after rollback = %d, want 3", fixtureCount)
	}
}

func testMutationsAndRawQuery(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	onlyID := starterOnlyID{}
	affected, err := orm.Insert(&onlyID).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "insert a model containing only an AUTO_RANDOM field", err)
	}
	if affected != 1 || onlyID.ID <= 0 {
		t.Fatalf("AUTO_RANDOM-only insert affected = %d, ID = %d", affected, onlyID.ID)
	}
	affected, err = orm.InsertMany([]starterOnlyID{{}, {}}).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "bulk insert models containing only an AUTO_RANDOM field", err)
	}
	if affected != 2 {
		t.Fatalf("AUTO_RANDOM-only bulk insert affected = %d, want 2", affected)
	}

	generated := starterGenerated{Name: "created", Score: 10, GroupID: 1}
	affected, err = orm.Insert(&generated).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "insert an AUTO_RANDOM model", err)
	}
	if affected != 1 || generated.ID <= 0 {
		t.Fatalf("AUTO_RANDOM insert affected = %d, ID = %d", affected, generated.ID)
	}

	generated.Name = "updated"
	generated.Score = 20
	affected, err = orm.Update(&generated).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "update every writable model field by primary key", err)
	}
	if affected != 1 {
		t.Fatalf("UPDATE affected rows = %d, want 1", affected)
	}
	loaded, err := orm.Query[starterGenerated]().Where(orm.Equal("ID", generated.ID)).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the updated AUTO_RANDOM model", err)
	}
	if loaded.Name != "updated" || loaded.Score != 20 || loaded.RowCount != 0 {
		t.Fatalf("updated model = %#v", loaded)
	}
	generated.Score = 30
	affected, err = orm.Update(&generated, "Score").Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "update selected model fields by primary key", err)
	}
	if affected != 1 {
		t.Fatalf("partial UPDATE affected rows = %d, want 1", affected)
	}
	loaded, err = orm.Query[starterGenerated]().Where(orm.Equal("ID", generated.ID)).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the partially updated AUTO_RANDOM model", err)
	}
	if loaded.Name != "updated" || loaded.Score != 30 {
		t.Fatalf("partially updated model = %#v", loaded)
	}

	affected, err = orm.Delete(&generated).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "delete a model by primary key", err)
	}
	if affected != 1 {
		t.Fatalf("primary-key DELETE affected rows = %d, want 1", affected)
	}

	batch := []*starterGenerated{
		{Name: "batch-a", Score: 1, GroupID: 7},
		{Name: "batch-b", Score: 2, GroupID: 7},
	}
	affected, err = orm.InsertMany(batch).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "bulk insert AUTO_RANDOM models", err)
	}
	if affected != 2 || batch[0].ID != 0 || batch[1].ID != 0 {
		t.Fatalf("bulk INSERT affected = %d, batch = %#v", affected, batch)
	}

	aggregate, err := orm.Raw[starterGenerated](
		"SELECT group_id, COUNT(*) AS row_count FROM tidbgo_it_generated WHERE group_id = ? GROUP BY group_id",
		int64(7),
	).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load a typed raw aggregate", err)
	}
	if aggregate.GroupID != 7 || aggregate.RowCount != 2 {
		t.Fatalf("raw aggregate = %#v", aggregate)
	}

	summary, err := orm.Raw[starterUserOrderSummary](`
WITH order_summary AS (
    SELECT user_id, COUNT(*) AS order_count, MAX(id) AS latest_order_id
    FROM tidbgo_it_orders
    GROUP BY user_id
)
SELECT u.id AS user_id, u.email, s.order_count, s.latest_order_id
FROM tidbgo_it_users AS u
JOIN order_summary AS s ON s.user_id = u.id
WHERE u.id = ?`, int64(1)).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load a typed raw CTE and JOIN", err)
	}
	if summary.UserID != 1 || summary.Email != "ada@example.test" || summary.OrderCount != 2 || summary.LatestOrderID != 12 {
		t.Fatalf("raw CTE summary = %#v", summary)
	}

	affected, err = orm.DeleteWhere[starterGenerated](orm.Equal("GroupID", int64(7))).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "delete models by an explicit predicate", err)
	}
	if affected != 2 {
		t.Fatalf("predicate DELETE affected rows = %d, want 2", affected)
	}

	rolledBack := starterGenerated{Name: "rolled-back", Score: 1, GroupID: 8}
	rollbackSignal := errors.New("integration-requested rollback")
	err = orm.Transaction(ctx, database, func(transaction *sql.Tx) error {
		affected, err := orm.Insert(&rolledBack).Exec(ctx, transaction)
		if err != nil {
			return err
		}
		if affected != 1 || rolledBack.ID <= 0 {
			return fmt.Errorf("transaction INSERT affected = %d, ID = %d", affected, rolledBack.ID)
		}
		return rollbackSignal
	})
	if err != rollbackSignal {
		fatalDatabaseError(t, dsn, "roll back through the transaction helper", err)
	}
	exists, err := orm.Query[starterGenerated]().Where(orm.Equal("ID", rolledBack.ID)).Exists(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "verify the mutation rollback", err)
	}
	if exists {
		t.Fatal("rolled-back AUTO_RANDOM model still exists")
	}

	committed := starterGenerated{Name: "committed", Score: 2, GroupID: 8}
	err = orm.Transaction(ctx, database, func(transaction *sql.Tx) error {
		affected, err := orm.Insert(&committed).Exec(ctx, transaction)
		if err != nil {
			return err
		}
		if affected != 1 || committed.ID <= 0 {
			return fmt.Errorf("transaction INSERT affected = %d, ID = %d", affected, committed.ID)
		}
		return nil
	})
	if err != nil {
		fatalDatabaseError(t, dsn, "commit through the transaction helper", err)
	}
	exists, err = orm.Query[starterGenerated]().Where(orm.Equal("ID", committed.ID)).Exists(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "verify the transaction helper commit", err)
	}
	if !exists {
		t.Fatal("committed AUTO_RANDOM model does not exist")
	}
}

func testUpserts(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	generated := starterGenerated{Name: "upsert-generated", Score: 1, GroupID: 9}
	affected, err := orm.Upsert(&generated).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "insert an AUTO_RANDOM model through UPSERT", err)
	}
	if affected != 1 || generated.ID != 0 {
		t.Fatalf("AUTO_RANDOM UPSERT affected = %d, ID = %d", affected, generated.ID)
	}
	inserted, err := orm.Query[starterGenerated]().Where(orm.Equal("Name", generated.Name)).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the AUTO_RANDOM model inserted through UPSERT", err)
	}
	if inserted.ID <= 0 || inserted.Score != 1 || inserted.GroupID != 9 {
		t.Fatalf("AUTO_RANDOM UPSERT insert result = %#v", inserted)
	}

	conflicting := starterGenerated{ID: 7717, Name: generated.Name, Score: 2, GroupID: 10}
	affected, err = orm.Upsert(&conflicting).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "update an AUTO_RANDOM model through a non-primary UNIQUE key conflict", err)
	}
	if affected <= 0 || conflicting.ID != 7717 {
		t.Fatalf("AUTO_RANDOM UPSERT conflict affected = %d, input ID = %d", affected, conflicting.ID)
	}
	updated, err := orm.Query[starterGenerated]().Where(orm.Equal("Name", generated.Name)).Only(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load the AUTO_RANDOM model updated through a non-primary UNIQUE key conflict", err)
	}
	if updated.ID != inserted.ID || updated.Score != 2 || updated.GroupID != 10 {
		t.Fatalf("AUTO_RANDOM UPSERT conflict result = %#v, original ID = %d", updated, inserted.ID)
	}

	value := starterUpsert{Key: "first", Payload: "created", Revision: 1}
	affected, err = orm.Upsert(&value).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "insert through UPSERT", err)
	}
	if affected != 1 {
		t.Fatalf("insert UPSERT affected = %d, want 1", affected)
	}

	value.Payload = "updated"
	value.Revision = 2
	affected, err = orm.Upsert(&value, "Payload", "Revision").Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "update through UPSERT", err)
	}
	if affected <= 0 {
		t.Fatalf("update UPSERT affected = %d, want a positive database-reported count", affected)
	}

	batch := []*starterUpsert{
		{Key: "first", Payload: "bulk-updated", Revision: 3},
		{Key: "second", Payload: "bulk-created", Revision: 1},
	}
	affected, err = orm.UpsertMany(batch).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "execute bulk UPSERT", err)
	}
	if affected <= 0 {
		t.Fatalf("bulk UPSERT affected = %d, want a positive database-reported count", affected)
	}
	if batch[0].Key != "first" || batch[1].Key != "second" {
		t.Fatalf("bulk UPSERT changed caller values = %#v", batch)
	}

	loaded, err := orm.Query[starterUpsert]().OrderBy(orm.Asc("Key")).All(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "load UPSERT results", err)
	}
	want := []starterUpsert{
		{Key: "first", Payload: "bulk-updated", Revision: 3},
		{Key: "second", Payload: "bulk-created", Revision: 1},
	}
	if len(loaded) != len(want) {
		t.Fatalf("UPSERT result count = %d, want %d", len(loaded), len(want))
	}
	for index := range want {
		if loaded[index].Key != want[index].Key || loaded[index].Payload != want[index].Payload || loaded[index].Revision != want[index].Revision {
			t.Fatalf("UPSERT result %d = %#v, want %#v", index, loaded[index], want[index])
		}
	}
}

func testStatementObservation(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	var events []orm.StatementEvent
	observedContext := orm.WithStatementObserver(ctx, func(event orm.StatementEvent) {
		events = append(events, event)
	}, orm.IncludeStatementArguments())
	value := starterGenerated{Name: "statement-observed", Score: 1, GroupID: 11}
	if _, err := orm.Insert(&value).Exec(observedContext, database); err != nil {
		fatalDatabaseError(t, dsn, "insert a statement-observed model", err)
	}
	if _, err := orm.Query[starterGenerated]().Where(orm.Equal("Name", value.Name)).Only(observedContext, database); err != nil {
		fatalDatabaseError(t, dsn, "load a statement-observed model", err)
	}
	if _, err := orm.RawExec(observedContext, database, "UPDATE tidbgo_it_generated SET score = score + 1 WHERE id = ?", value.ID); err != nil {
		fatalDatabaseError(t, dsn, "update a statement-observed model through raw SQL", err)
	}
	if err := orm.Transaction(observedContext, database, func(transaction *sql.Tx) error {
		_, err := orm.Delete(&value).Exec(observedContext, transaction)
		return err
	}); err != nil {
		fatalDatabaseError(t, dsn, "delete a statement-observed model in a transaction", err)
	}
	if _, err := orm.Query[starterGenerated]().Where(orm.Equal("Name", value.Name)).First(observedContext, database); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("load a deleted statement-observed model error = %v, want sql.ErrNoRows", err)
	}

	wantOperations := []orm.StatementOperation{
		orm.StatementInsert,
		orm.StatementSelect,
		orm.StatementUpdate,
		orm.StatementBegin,
		orm.StatementDelete,
		orm.StatementCommit,
		orm.StatementSelect,
	}
	wantArgumentCounts := []int{3, 2, 1, 0, 1, 0, 2}
	if len(events) != len(wantOperations) {
		t.Fatalf("statement event count = %d, want %d: %#v", len(events), len(wantOperations), events)
	}
	for index := range wantOperations {
		event := events[index]
		if event.Operation != wantOperations[index] || event.ArgumentCount != wantArgumentCounts[index] || event.StartedAt.IsZero() || event.Duration < 0 {
			t.Fatalf("statement event %d = %#v", index, event)
		}
		if strings.Contains(event.SQL, value.Name) {
			t.Fatalf("statement event %d SQL contains a bind argument value: %q", index, event.SQL)
		}
	}
	if len(events[0].Arguments) != 3 || events[0].Arguments[0] != value.Name || events[0].Arguments[1] != int64(1) || events[0].Arguments[2] != int64(11) {
		t.Fatalf("INSERT statement arguments = %#v", events[0].Arguments)
	}
	for _, index := range []int{0, 2, 4} {
		if !events[index].RowsAffectedKnown || events[index].RowsAffected != 1 || events[index].Error != nil {
			t.Fatalf("mutation statement event %d = %#v", index, events[index])
		}
	}
	if !errors.Is(events[len(events)-1].Error, sql.ErrNoRows) {
		t.Fatalf("terminal statement event error = %v, want sql.ErrNoRows", events[len(events)-1].Error)
	}
}

func testDebugReport(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()

	var user starterUser
	report, err := orm.Debug(ctx, func(debugContext context.Context) error {
		var queryErr error
		user, queryErr = orm.Query[starterUser]().
			Select("ID", "Email").
			Preload("Orders").
			Where(orm.Equal("ID", int64(1))).
			Only(debugContext, database)
		return queryErr
	})
	if err != nil {
		fatalDatabaseError(t, dsn, "debug a user and orders preload", err)
	}
	if user.ID != 1 || len(user.Orders) != 2 {
		t.Fatalf("debug report result = %#v", user)
	}
	if report.StartedAt.IsZero() || report.Duration <= 0 || report.StatementDuration <= 0 || report.Duration < report.StatementDuration {
		t.Fatalf("debug report timing = %#v", report)
	}
	if len(report.Statements) != 2 {
		t.Fatalf("debug report statement count = %d, want 2: %#v", len(report.Statements), report.Statements)
	}
	if !strings.Contains(report.Statements[0].SQL, "FROM `tidbgo_it_users`") || !strings.Contains(report.Statements[1].SQL, "FROM `tidbgo_it_orders`") {
		t.Fatalf("debug report SQL = %q, %q", report.Statements[0].SQL, report.Statements[1].SQL)
	}
	var statementDuration time.Duration
	for index, event := range report.Statements {
		statementDuration += event.Duration
		if event.Operation != orm.StatementSelect || event.ArgumentCount == 0 || event.Arguments != nil || event.Error != nil {
			t.Fatalf("debug report statement %d = %#v", index, event)
		}
	}
	if report.StatementDuration != statementDuration {
		t.Fatalf("debug report statement duration = %s, want %s", report.StatementDuration, statementDuration)
	}
}

func orderIDs(orders []starterOrder) []int64 {
	ids := make([]int64, len(orders))
	for index := range orders {
		ids[index] = orders[index].ID
	}
	return ids
}

func roleIDs(roles []starterRole) []int64 {
	ids := make([]int64, len(roles))
	for index := range roles {
		ids[index] = roles[index].ID
	}
	return ids
}

func requireIDs(t *testing.T, got []int64, want ...int64) {
	t.Helper()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
}

func fatalDatabaseError(t testing.TB, dsn, operation string, err error) {
	t.Helper()
	t.Fatalf("%s: %s", operation, redact.Error(err, dsn))
}
