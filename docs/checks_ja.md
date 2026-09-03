# 解析とdiagnostic

go-tidbはapplicationへqueryごとのdiagnostic登録を要求せず、必要なevidenceによってcheck経路を分けます

| Evidence | APIまたはcommand | DB access |
| --- | --- | --- |
| Go model metadata | `check.Model[T]` | なし |
| SQL snapshot compatibility | `check.Schema[T]` | なし |
| Go sourceのquery patternとprojection利用 | `tidbgo lint` | なし |
| 実行されたquery shapeとstatement behavior | RuntimeCaptureと `tidbgo analyze` | ServerRU収集を有効にしない限りcaptureはapplication statementだけを実行 |
| TiDB optimizer estimate | `SelectQuery.Explain` | あり |
| TiDB runtime plan | `SelectQuery.ExplainAnalyze` と `ExplainAnalyzePlan.Diagnostics` | あり、SELECTも実行 |

## Modelとschemaのcheck

application所有のmodel typeとschema snapshotは通常のGo testで確認します

```go
func TestUserMapping(t *testing.T) {
    if diagnostics := check.Model[User](); len(diagnostics) != 0 {
        t.Fatalf("model diagnostics: %#v", diagnostics)
    }

    sqlText, err := os.ReadFile("testdata/schema.sql")
    if err != nil {
        t.Fatal(err)
    }
    catalog, err := schema.Parse(string(sqlText))
    if err != nil {
        t.Fatal(err)
    }
    if diagnostics := check.Schema[User](catalog); len(diagnostics) != 0 {
        t.Fatalf("schema diagnostics: %#v", diagnostics)
    }
}
```

どちらもofflineで動作し `[]check.Diagnostic` を返します

`check.Model` は `MOD001` から `MOD007` でmodel intentとtagを検証します

`check.Schema` は `CMP001` から `CMP014` で方向付きcompatibility ruleを適用します

## 実行済みqueryのdiagnostic

request、job、analysis testのboundaryでRuntimeCaptureを1回設定し、既存のORM callへderived contextを渡します

```go
capture := orm.NewRuntimeCapture(writer)
ctx = orm.WithRuntimeCapture(ctx, capture)

if err := runOperation(ctx); err != nil {
    return err
}
if err := capture.Err(); err != nil {
    return err
}
```

生成されたJSON Lines artifactをofflineで解析します

```sh
tidbgo analyze runtime.jsonl
tidbgo analyze runtime.jsonl --schema schema.sql
```

captured statementがtyped QueryShapeを持つ場合、analyzerは次のquery ruleを自動適用します

| Code | 内容 |
| --- | --- |
| `QRY002` | positive OFFSETがpageを返す前にrowをskipする |
| `QRY003` | positive LIMITにdeterministicなorderがない |
| `QRY004` | LIKE predicateがwildcardから始まる |
| `QRY005` | Relation filter付きTopNがEXISTS fallbackを使った |
| `QRY006` | 渡したschemaが解析対象のindex accessを表現できない |
| `QRY007` | ordered limited accessに一致するindex prefixがない |

`QRY006` と `QRY007` には `--schema` が必要です

fingerprintとQueryShapeはbind valueを除外しますが、SQL templateとerrorにはapplication dataが含まれる場合があります

runtime解析はmetadata不足、runtime N+1 SELECT候補、ServerRU収集failure、ServerRU baseline regressionもreportします

capture scope、artifact security、ServerRU cost、baseline比較は[Statement observation](observability_ja.md)を参照してください

## Go source解析

applicationをcompileまたは実行せずsourceを解析します

```sh
tidbgo lint .
tidbgo lint . --json
tidbgo lint . --schema schema.sql
```

source解析は解決済みの `Build`、`All`、`First`、`Only`、`Explain`、`ExplainAnalyze` query terminalへ `QRY002` から `QRY004` を適用します

fluent chain、1個のlocal builder定義、local query helper、integerとstring literal、同じfile内の単純なconstantを解決します

関連するpagination、order、leading wildcard有無を全て解決できたterminalは `analyzed_patterns` へ数えます

dynamicな `Limit` または `Offset`、variadic order、未解決predicate helper、別statementで変更されたbuilder、captureされたbuilderは推測せず `uncertain_patterns` へ数えます

`--schema` を指定するとsource解析はruntime model descriptorと同じ `tidbgo` metadataとdefault naming ruleから物理table名とcolumn名も導出します

明示的なpositive `Limit`、同じ方向の `OrderBy`、conjunctiveな `Equal` filterを解決できたroot shapeだけをruntime解析と共通のneutral index-prefix checkerへ渡します

default active soft-delete columnはquery上で `WithDeleted` を解決できない限りequality prefixへ含めます

`index_patterns` はordered positive-limit候補を数え、`analyzed_index_patterns` と `uncertain_index_patterns` は照合できたshapeとできなかったshapeを分離します

Relation predicate、non-equality filter、mixed direction、unknown field、embedded model shape、別statementで変更されたbuilderには推測したindex diagnosticを出さずuncertainとします

`SRC001` は1 function内でresultの全利用を証明できた場合だけprojectionの限定を提案します

repository return、alias、model method、解決できないresult flowは別の `analyzed` と `uncertain` projection counterへ反映します

`QRY005` とrelation-first index解析にはcompilerが正規化したRelation decisionが必要なためruntime capture ruleとして残します

source上の `QRY006` と `QRY007` は上記の高確度なroot accessだけを対象にします

## Runtime plan diagnostic

`Explain` はroot SELECTを実行せずTiDBへestimate planを要求します

`ExplainAnalyze` はroot SELECTを実行して `orm.ExplainAnalyzePlan` を返します

planの `Diagnostics` は追加のDB callなしで、不完全なstatistics、大きなestimate divergence、大規模full scan、positive disk useを `PLN001` から `PLN004` でreportします

```go
plan, err := query.ExplainAnalyze(ctx, connection)
if err != nil {
    return err
}
diagnostics := plan.Diagnostics()
```

planの選択はdata distributionとstatisticsに依存するため、plan diagnosticと実測ServerRU baselineはstatic ruleを補完しますが将来のplanを保証しません

## Suppressionとexit status

`tidbgo analyze` と `tidbgo lint` はreason付きsuppressionを繰り返し受け付けます

```sh
tidbgo analyze runtime.jsonl --suppress 'RUN002=bounded retry loop'
tidbgo lint . --suppress 'SRC001=full row is intentionally returned'
```

codeは現在のresultに存在し、diagnostic側がsuppressibleである必要があります

未使用、重複、reasonなし、non-suppressibleな指定は拒否します

suppressed diagnosticはtextとJSON outputへ残ります

active error diagnosticがある場合はstatus `1`、warningとinfoだけの場合はsuccessです

invalid inputはstatus `2`、I/Oまたはinternal failureはstatus `5` です

## 現在のcoverage境界

- RuntimeCaptureはderived contextを使ってgo-tidbから実行されたstatementだけを対象にする
- source lintは静的に解決できたbuilder flowだけへ `QRY002` から `QRY004` を適用する
- source lintへ `--schema` を指定した場合は解決済みroot ordered-limit accessだけへ `QRY006` と `QRY007` を適用する
- `QRY005` とrelation-first index解析にはcaptured compiler decisionが必要
- `EXPLAIN ANALYZE` はSELECTを実行してRUを消費する
- ServerRU収集はrecognized DML statementごとにsame-session diagnostic round tripを1回追加する
- query planとRUは現在のstatistics、data distribution、workloadに依存する
