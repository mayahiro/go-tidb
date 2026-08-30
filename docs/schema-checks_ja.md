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

Relationを持つmodelのsnapshotには、宣言されたRelation target tableとmany-to-many junction tableも含める必要があります

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
- 宣言した全Relation targetとmany-to-many junctionのtableおよびkey columnが存在し、既知のRelation keyとSQL type familyに互換性があること
- `belongs_to`、`has_one`、`many_to_many` のtarget identityを証明するprimary keyまたはunique keyがあること
- pure many-to-many junctionにsource-target pair全体だけを対象とするprimary keyまたはunique keyがあること
- Relation insertはkeyだけを渡すため、pure junctionにdefaultまたはdatabase generationのない追加の `NOT NULL` columnがないこと
- `has_many` targetまたはmany-to-many junctionにsource key全体をleading columnとして含むindexがない場合はwarning

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
| `CMP002` | error | model、Relation target、junctionの物理tableが存在しない |
| `CMP003` | error | mappingしたmodelまたはRelation keyに物理columnがない |
| `CMP004` | error | 既知のmodelまたはRelation key representationとSQL type familyに互換性がない |
| `CMP005` | warning | nullableな物理columnをnon-nullableなnative Go fieldで表している |
| `CMP006` | warning | nullableなGo representationを `NOT NULL` columnへmappingしている |
| `CMP007` | error | modelと物理schemaのordered primary keyが一致しない |
| `CMP008` | error | mappingしたfieldと物理columnで `AUTO_RANDOM` の有無が一致しない |
| `CMP009` | error | 通常のwritable model fieldをgenerated columnへmappingしている |
| `CMP010` | warning | databaseだけに存在する必須columnによりmodel insertが失敗し得る |
| `CMP011` | error | to-oneまたはmany-to-many target identityのunique性をsnapshotから証明できない |
| `CMP012` | error | many-to-many junctionにsource-target pair全体だけのunique constraintがない |
| `CMP013` | error | many-to-many junctionへのinsertにmappingしたkey以外の値が必要 |
| `CMP014` | warning | collection Relationにsource key全体から始まるindexがない |

value-formの `time.Time` soft-delete fieldはruntimeがSQL `NULL` をzero timeへscanし、zero timeをSQL `NULL` としてwriteするためnullableとして扱います

不正なmodel metadataはphysical compatibilityを評価する前に既存のnon-suppressible `MOD001` として返します

structuralなRelation index warningを含むwarningはshared diagnostic representationでsuppressibleです

`check.NewReport` または `tidbgo check` でreason付きsuppressionを適用できます

errorは実行可能なmapping、insert、cardinalityの矛盾を表すためsuppressibleではありません

詳細は[Offline diagnostic report guide](checks_ja.md)を参照してください

composite Relation keyでは全componentを生成queryが制約するため、indexのleading position内でmappingと異なるcolumn順を使用できます

expression indexはこのstructural coverageを証明しません

exact junction pairもsource-targetまたはtarget-sourceのどちらの順序でもよい一方、追加のunique-key componentを含めることはできません

## Type checkの境界

現在のtype checkは意図的に広いrepresentation familyを使用します

integer width、signed range、Decimal precisionとscale、string length、character set、collation、temporal precision、default expressionはまだ比較しません

将来追加された未知のSQL typeと、applicationが選択した `sql.Scanner` または `driver.Valuer` 実装custom typeの意味を推測しません

custom type semanticsはapplicationの責任です

Relation index diagnosticはORMが `has_many` と `many_to_many` に対して決定的に生成するaccess pathだけを対象とします

snapshotにstructuralなprefix coverageがない事実だけを報告し、optimizerの選択予測とapplication query全般のindex推奨は行いません

production indexを変更する前に `Explain` または `ExplainAnalyze` で実際のaccess pathを確認してください

foreign keyは要求も検査も行いません

referential-integrity policy、一般的なperformance index、Migration history、live database driftはoffline comparisonの対象外です

現在のTiDB仕様は[`CREATE TABLE` grammar](https://docs.pingcap.com/tidb/stable/sql-statement-create-table/)、[`AUTO_RANDOM`](https://docs.pingcap.com/tidbcloud/auto-random/)、[case-insensitive table-name behavior](https://docs.pingcap.com/tidbcloud/mysql-compatibility/)、[index-prefix guidance](https://docs.pingcap.com/developer/dev-guide-index-best-practice/)を参照してください
