# Offline schema compatibility

[English](schema-checks.md)

`go-tidb` はSQL schema snapshotとapplication-owned Go structを、責任範囲が異なる正として扱います

SQL snapshotは期待する物理database、structはapplicationが読み書きするcolumnとRelationを表すため、両者が同じ情報を持つ必要はありません

## 基本的な使用方法

snapshotを1回parseし、immutable catalogを各modelで再利用します

```go
catalog, err := schema.Parse(schemaSQL)
if err != nil {
	return err
}

diagnostics := make([]check.Diagnostic, 0)
diagnostics = append(diagnostics, check.Schema[User](catalog)...)
diagnostics = append(diagnostics, check.Schema[Order](catalog)...)
```

`schema.Parse`、`check.Schema`、`check.SchemaType` はDB I/O、connection configurationの読込、SQLとuser methodの実行を行いません

callerが `reflect.Type` を既に持つ場合は `check.SchemaType` を使用できます

## 受け付けるSQL snapshot

`schema.Parse` は1個以上のself-containedなTiDB `CREATE TABLE` definitionを受け付けます

次の情報を認識します

- 通常のtableとcolumn definition
- inlineとtable-levelのprimary keyとunique key
- ordinary indexとfunctional index part
- nullability、default、generated column、`AUTO_INCREMENT`、`AUTO_RANDOM`
- schema-qualified table name
- `AUTO_RANDOM` のcomment formを含む `SHOW CREATE TABLE` 由来のTiDB executable comment

schema-only dumpで一般的な `SET` や `DROP TABLE` など、`CREATE TABLE` 以外のstatementは無視します

self-containedなcolumn definitionを持たない `CREATE TABLE LIKE` と `CREATE TABLE AS SELECT` は拒否します

`ALTER TABLE` statementの実行とreplayは行いません

現在のmodel metadataはunqualified table nameを使用するため、複数schemaに同じunqualified table nameがあるsnapshotはambiguousとして拒否します

tableとcolumnの比較はTiDBのcase-insensitiveなidentifier lookupに合わせます

## 方向付きcomparison

modelのread、write、Relation cardinalityへ影響し得る次の事実を診断します

- mapping先tableが存在すること
- mappingするcomputed以外の全fieldに物理columnがあること
- 既知のnative GoとSQL type familyに互換性があること
- nullableな物理columnをnon-nullableなnative Go fieldで表す場合はwarning
- pointer、byte slice、value-form soft-delete fieldを `NOT NULL` へmappingする場合はSQL `NULL` を生成できるためwarning
- modelがprimary keyを宣言する場合はordered columnが物理primary keyと一致すること
- mappingしたcolumnの両側で `AUTO_RANDOM` の有無が一致すること
- 物理generated columnを通常のwritable model fieldへmappingしないこと
- `belongs_to` または `has_one` のtargetにat-most-one-row semanticsを証明するprimary keyまたはunique keyがあること

databaseにだけ存在するcolumnは、structが省略したという理由だけではerrorになりません

nullable、default付き、database-generatedのcolumnは許容します

databaseにだけ存在する `NOT NULL` かつdefaultとdatabase generationがないcolumnは、そのmodelからのinsertが失敗し得るためwarningになります

このためdatabase管理の `created_at` などをstructから省略できます

modelがprimary keyを宣言しない場合、compatibility checkは物理primary keyの重複記載を要求しません

primary-key mutation capabilityを使用できないことは `check.Model` が `MOD005` で別に報告します

## Diagnostic

| Code | Default severity | 意味 |
| --- | --- | --- |
| `CMP001` | error | parse済みのnon-nil schema catalogが渡されていない |
| `CMP002` | error | modelまたはto-one Relation targetの物理tableが存在しない |
| `CMP003` | error | mappingしたcomputed以外のfieldまたはto-one target keyに物理columnがない |
| `CMP004` | error | 既知のnative Go representationとSQL type familyに互換性がない |
| `CMP005` | warning | nullableな物理columnをnon-nullableなnative Go fieldで表している |
| `CMP006` | warning | nullableなGo representationを `NOT NULL` columnへmappingしている |
| `CMP007` | error | modelと物理schemaのordered primary keyが一致しない |
| `CMP008` | error | mappingしたfieldと物理columnで `AUTO_RANDOM` の有無が一致しない |
| `CMP009` | error | 通常のwritable model fieldをgenerated columnへmappingしている |
| `CMP010` | warning | databaseだけに存在する必須columnによりmodel insertが失敗し得る |
| `CMP011` | error | to-one Relation targetのunique性をsnapshotから証明できない |

value-formの `time.Time` soft-delete fieldはruntimeがSQL `NULL` をzero timeへscanし、zero timeをSQL `NULL` としてwriteするためnullableとして扱います

不正なmodel metadataはphysical compatibilityを評価する前に既存のnon-suppressible `MOD001` として返します

warningはshared diagnostic representationでsuppressibleです

`check.NewReport` または `tidbgo check` でreason付きsuppressionを適用できます

errorは実行可能なmappingまたはcardinalityの矛盾を表すためsuppressibleではありません

詳細は[Offline diagnostic report guide](checks_ja.md)を参照してください

## Type checkの境界

現在のtype checkは意図的に広いrepresentation familyを使用します

integer width、signed range、Decimal precisionとscale、string length、character set、collation、temporal precision、default expressionはまだ比較しません

将来追加された未知のSQL typeと、applicationが選択した `sql.Scanner` または `driver.Valuer` 実装custom typeの意味を推測しません

custom type semanticsはapplicationの責任です

parserはordinary indexも記録しますが、現在checkするのはto-one Relationの正しさに必要なunique性だけです

performance index、foreign key、junction table constraint、Migration history、live database driftはまだ診断しません

現在のTiDB仕様は[`CREATE TABLE` grammar](https://docs.pingcap.com/tidb/stable/sql-statement-create-table/)、[`AUTO_RANDOM`](https://docs.pingcap.com/tidbcloud/auto-random/)、[case-insensitive table-name behavior](https://docs.pingcap.com/tidbcloud/mysql-compatibility/)を参照してください
