# Diagnostic report

[English](checks.md)

`go-tidb` はdiagnosticの生成とreportを分離します

offline checkではapplication codeがmodel type、query builder、SQL schema snapshotを明示的に選択し、`check.Report` と `tidbgo check` がDB接続なしで1個の固定result policyを適用します

この明示的なcheck boundaryにはgenerated registry、project YAML、live schema inspectionが不要です

独立したopt-inの `tidbgo lint` はpackageをloadせずGo sourceをscanします

明示的なconnected runtime plan収集は分離され、引き続きopt-inです

## Diagnosticの生成

applicationが所有するcheck commandでdiagnosticを集約します

```go
diagnostics := make([]check.Diagnostic, 0)
diagnostics = append(diagnostics, check.Model[User]()...)
diagnostics = append(diagnostics, recentOrdersQuery.Diagnostics()...)
diagnostics = append(diagnostics, check.Schema[User](catalog)...)
diagnostics = append(diagnostics, recentClipsQuery.DiagnosticsWithSchema(catalog)...)

err := json.NewEncoder(os.Stdout).Encode(diagnostics)
```

`tidbgo check` のJSON inputは `check.Diagnostic` objectのarrayを1個だけ含みます

`DiagnosticsWithSchema` はparse済みsnapshotを再利用し、確度の高いordered query accessと物理index prefixを照合します

完全offlineで動作し、出力するschema-aware evidenceにはbind valueを含まないquery fingerprintを記録します

modelとqueryを明示的に登録する実行例は[`examples/starter-app/cmd/check`](../examples/starter-app/cmd/check)を参照してください

## Go source projection解析

queryを個別登録せずcurrent directoryを再帰的にscanします

```sh
tidbgo lint
```

対象を狭める場合は1個のproduction Go fileまたはdirectoryを指定します

```sh
tidbgo lint ./internal/repository
tidbgo lint ./internal/repository --json
```

`tidbgo lint` はapplication packageのloadまたは実行、code generation、schema fileのread、DB接続を行わずsourceをparseします

directory scanは現在のGo build contextに従い、test file、generated file、`vendor`、`testdata`、hidden directoryを除外します

現在の `SRC001` ruleはdefault projectionを使用する `All`、`First`、`Only` terminalを認識します

modelのmapped scalar fieldと同じfunction内にあるresultの全利用を証明できる場合だけ `Select` を提案します

`*orm.SelectQuery[T]` を返すsame-packageのtop-level query helperとlocal mutable builderは保守的に追跡し、computed、ignored、Relation fieldはdefault projection比較に含めません

別functionへ渡す、returnする、aliasを作る、model methodまたはRelationを利用する、`Preload` でloadするresultは不確実とし、projection warningを出しません

解決できないmodel shapeと条件付きで変更されるbuilderも不確実として扱い、安全でないprojectionを機械的な修正として提示しないようにします

textとJSONの全reportに次のcoverage countを含めます

- `files`: parseしたnon-generated production file数
- `model_types`: 解析対象として解決できたquery result model type数
- `result_queries`: 認識した `All`、`First`、`Only` terminal数
- `explicit_projections`: `Select` を使用していると認識したresult query数
- `analyzed`: completeなlocal利用を証明できたdefault projection result数
- `uncertain`: 意図的に推測しなかったrecognized result数

`SRC001` はsuppressible warningでありsuccess exit statusを変更しません

`--suppress 'SRC001=reason'` は `tidbgo check` と同じreason付きpolicyを使います

不正なsourceまたは対象production Go fileがないinputはstatus `2`、読み取れないpathとoutput failureはstatus `5` を返します

## 任意のoffline statement count tooling

statement数はdefaultではwarningではなく事実を表すplanning dataとして返します

```go
bulkCount, err := orm.InsertMany(orders).StatementCount()
allEstimate, err := usersWithOrdersQuery().EstimateAllStatements()
```

bulk insertとbulk upsertは成功する実行pathの正確な件数を返します

`All` estimateは常に最小値を返し、builderおよびRelation cardinalityから証明できる場合だけ最大値をknownとして返します

これらのmethodはofflineで動作し、element valueの参照、custom `driver.Valuer` methodの実行、DB接続を行いません

production operationごとにstatement count wrapperを並行して実装しません

通常のapplication codeはbuilderを直接実行します

requestまたはjob境界へobserverを1回設定するとstructured runtime captureが実際のstatement、自動bulk split、preload batchを記録します

offlineの値は `SelectQuery.Diagnostics` へ追加せず、`tidbgo check` も直接読み取りません

複数statementは安全なbulk分割またはRelation loadingの意図した結果でもあるため、project固有のthresholdをdiagnosticにするかは専用testまたはcustom offline toolingが決定します

正確な境界は[query guide](queries_ja.md#relation-preload)と[mutation guide](mutations_ja.md#insert)、自動runtime収集は[observation guide](observability_ja.md#structured-runtime-capture)を参照してください

## Opt-inのconnected plan diagnostic

明示的に実行した `ExplainAnalyze` planのdiagnosticを同じreportへ追加できます

```go
runtimePlan, err := recentClipsQuery.ExplainAnalyze(ctx, db)
if err != nil {
    return err
}
diagnostics = append(diagnostics, runtimePlan.Diagnostics()...)
```

`ExplainAnalyze` はcompleteなroot SELECTを実行し、database resourceを消費します

`Diagnostics` 自体は決定的でDB I/Oを行わず、既に返されたrowだけを検査します

不完全なstatistics、保守的なrow estimateの差、大規模table full scan、認識可能な正数のdisk usageを `PLN001` から `PLN004` のsuppressible warningとして返します

evidenceはTiDBのaccess objectを保持し、mappingを一意に判断できる場合はcompilerが解決したphysical table、Go model、Relation pathも追加します

これは手動のconnected analysisであり、自動runtime captureではありません

`RuntimeCapture` を設定しても `EXPLAIN ANALYZE` の実行、plan rowの収集、これらのdiagnostic生成は行いません

threshold、evidence、cost、data handlingの詳細は[EXPLAIN ANALYZE boundary](observability_ja.md#explain-analyze)を参照してください

## CLIによるreport

standard inputからarrayを読み取ります

```sh
go run ./cmd/check | tidbgo check
```

独立したartifactを使用する場合は1個のinput fileを渡します

```sh
tidbgo check diagnostics.json
```

inputを省略するか `-` を指定するとstandard inputを使用します

defaultのtext reportはdiagnosticの順序を維持し、suppressed diagnosticとreasonを表示した後、activeなerror、warning、info、suppressedの件数を出力します

structured reportには `--json` を使用します

```sh
go run ./cmd/check | tidbgo check --json
```

exit policyは固定です

| Status | 意味 |
| ---: | --- |
| `0` | activeなerror diagnosticが残っていない |
| `1` | activeなerror diagnosticが1個以上残っている |
| `2` | command argument、diagnostic JSON、suppression inputが不正 |
| `5` | inputのread、outputのwrite、内部operationを完了できない |

warningとinfoは成功statusを変更しません

runtime capture artifactは作成済みdiagnostic arrayではなくexecution recordを含むため別commandで扱います

```sh
tidbgo analyze runtime.jsonl
tidbgo analyze runtime.jsonl --schema schema.sql
```

このcommandはDBへ接続しません

artifact boundaryは[observation guide](observability_ja.md#structured-runtime-capture)を参照してください

captured typed query shapeへ `QRY002` から `QRY005` を自動適用します

`--schema` は渡したTiDB `CREATE TABLE` snapshotをofflineでparseし、`QRY006` と `QRY007` を追加するためapplication側のquery registryは不要です

statisticsはquery shapeを持つcaptured statement数とsnapshotでcheckしたstatement数をreportします

completeなshapeを持たないstatementはこれらのquery pattern ruleとschema ruleの対象外です

`CollectServerRU` がsampleを生成した場合、runtime statisticsはtargetとdiagnosticのduration、go-tidbとauxiliaryのstatement数、成功sample数、collection error数、ServerRU合計を分離します

`RUN003` はcollection failureによって測定dataが不完全なことを示すためsuppressできません

## 許容したdiagnosticのsuppression

suppressionは1個のexact diagnostic codeを指定し、空ではないreasonを必須とします

```sh
go run ./cmd/check | \
  tidbgo check --suppress 'MOD005=read-only model does not use key mutations'
```

異なるcodeには `--suppress` を繰り返し指定できます

1個のsuppressionはinput array内でexact codeが一致する全suppressible diagnosticへ適用します

suppressed diagnostic全体とreasonはreportへ残ります

duplicate suppression code、使用されなかったsuppression、空のreason、`Suppressible` がfalseのdiagnosticを対象とするsuppressionはerrorになります

CLI processを必要としない場合は同じpolicyをGoから直接適用できます

```go
report, err := check.NewReport(
    diagnostics,
    check.Allow("MOD005", "read-only model does not use key mutations"),
)
if err != nil {
    return err
}
if report.HasErrors() {
    return errors.New("go-tidb checks failed")
}
```

`Report.Diagnostics()` はactive diagnostic、`Report.Suppressed()` は記録されたsuppression、`Report.Summary()` は固定された件数を返します

## Securityとdata boundary

現在のmodel checkとquery checkはbind valueを含まず、schema-aware checkは渡されたsnapshotだけを使用します

query fingerprintもbind valueとpagination valueを除外します

diagnostic messageにはmodel名、schema identifier、source path、parser errorが含まれる場合があります

`SRC001` はsource literalとbind valueをdiagnosticへ含めません

diagnostic messageにはfield名も含まれ、Go parser errorは不正なtoken textをquoteする場合があります

diagnostic JSONとreportはdevelopment artifactとして扱い、出力先と保存期間を管理してください

text rendererはterminalへuntrusted inputを書き込む前にcontrol characterをescapeします

JSON outputはstructured dataを維持し、valueをSQLへinterpolateしません
