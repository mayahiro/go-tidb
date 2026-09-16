# 集計の実行方針を比較する

[English](aggregate-comparison.md)

`AggregateQuery.Compare` は同じ[集計query](aggregates_ja.md)を `auto`、`tikv`、`tiflash_mpp` の要求で実行します
全結果値と順序を照合し、通常SELECTのlatencyとServerRUを測定した後、別の実行でruntime planと警告を取得します

```go
q := orm.Aggregate[Order]().
    Select(orm.Field("ShopID"), orm.CountAll().As("OrderCount"), orm.Sum("Amount").As("Total")).
    GroupBy("ShopID").OrderBy(orm.Asc("ShopID"))

report, err := q.Compare(ctx, db, orm.AggregateCompareOptions{
    Case: "shop-totals-fixture-v1",
})
if err != nil {
    return err // report retains successful samples and any collected plans
}
for _, variant := range report.Variants {
    fmt.Printf("%s rows=%d median_ms=%.3f mean_ServerRU=%.6f plan=%s\n",
        variant.Name, variant.Rows, variant.LatencyMS.Median,
        variant.ServerRU.Mean, variant.PlanStatus)
}
```

元のqueryは変更しません。既存の `ReadFrom` と `MPP` は比較用copy内で置換します
`auto` はpolicy hintを追加せずsession設定を継承し、設定をresetしません
TiKVは行指向storageを要求し、TiFlashは列指向storageとMPPのcost選択の強制を要求します
hintによって未対応の処理を実行可能にしたり、不足するレプリカを作成したりはできません
[TiDBのMPP選択](https://docs.pingcap.com/tidb/stable/use-tiflash-mpp-mode/)を参照してください

## 入力と結果照合

固定datasetまたは適切なread snapshot、同等のdriver／session設定、安定した入力を用意します
`Case` はdataの版と入力の選択性を含む条件の名前です
結果一致は元dataが変わらなかったことの証明にはなりません。条件の維持とsnapshotの設定は呼び出し側の責務です

| Option | 契約 |
| --- | --- |
| `Case` | 必須、ASCII英数字と `.`, `_`, `-` の1から128文字、先頭は英数字 |
| `Samples` | 各variantの測定SELECT数、5から1000、0指定は5 |
| `MaxRows` | 1実行で保持する結果行数の上限、0指定は100,000 |
| `FloatAbsoluteTolerance` | 0以上の有限な絶対許容誤差、defaultの0は完全一致 |
| `FloatRelativeTolerance` | 0から1の有限な相対許容誤差、defaultは0 |

`report.Options` はdefault適用後の設定と許容誤差を保持します
groupingを使うqueryには `OrderBy` が必須で、順序が一意になるtie-breakerを指定します
選択した [`Date` と `YearMonth` のkey](aggregates_ja.md#日別月別の集計) にも同じ結果照合を適用できます
case内でsession timezone、driverの `loc`、`parseTime` を揃えてください
[`CountIf` と `SumIf`](aggregates_ja.md#条件付き集計) にも同じ結果とNULLの照合を適用します
HAVING／順序で繰り返す参照を含め、条件式の各SQL parameterをwarmup前に1回ずつ評価して固定します
`Limit`、`Offset`、predicate、grouping、soft-delete scopeは元の指定を維持します
`MaxRows` 超過は失敗とし、結果の切り詰めによる成功やLIMITの書き換えは行いません
memoryには基準結果と再利用する現在結果、およびsampleのmetadataを保持します
上限は出力行数に適用し、値ごとのbyte数、scanする入力行数、DBのcostを制限しません

I/O前に標準 `database/sql` のbind値を位置ごとに一度固定します
pointerを解決し、`driver.Valuer` を一度評価し、byte sliceをcopyします
unsigned 64bit integerは対応するdriverへ渡せる形で保持します
標準converterで扱えないdriver固有の引数型は拒否します
比較中にqueryや入力を変更しないでください

全warmupと測定結果を最初のauto warmupと照合します
行、列、driverの値の型、NULL、整数、DECIMAL表現には完全一致を要求し、byte sliceは内容、時刻は同じ瞬間かを比較します
DECIMALのbyte表現を浮動小数点へ変換しません
`float32` と `float64` は同じdriver型同士を比較し、絶対または相対許容誤差のどちらかを満たせば差を許容します
相対誤差は差の絶対値を両値の絶対値の大きい方で割ります
NaNは一致せず、無限大は同じ符号の無限大にだけ一致します。case内で許容誤差を揃えてください

## 測定とsessionの寿命

各variantは2回のwarmup後に `Samples` 回測定し、roundごとに最初のvariantを交代します
defaultでは次を実行します

- 通常SELECTを21回実行し、それぞれ直後に同じsessionでRUを取得する
- 独立した `EXPLAIN ANALYZE` を3回実行し、それぞれ直後に `SHOW WARNINGS` を取得する

48 statementを1つの固定connectionで実行します
executorは `*sql.DB`、`*sql.Conn`、`*sql.Tx` とそれらの `Observe` wrapperに対応します
poolから取得したconnectionは比較後に解放し、呼び出し側所有のconnectionとtransactionは開いたままにします
借用sessionを同時使用しないでください

`Duration` はquery実行、rawな `database/sql` scan、rowsのcloseを測定します
compile、connection取得、引数固定、結果照合、RU取得、plan、callbackは区間外です
`ScanAll` の結果mappingやapplicationのScannerは測定しません
`DiagnosticDuration` はRU取得を別に記録します
各variantはlatencyのmillisecond値とRUについて、件数、最小、中央値、平均、最大を返します

比較は `CollectServerRU` がなくてもRUを明示的に取得します
既存observerとRuntimeCaptureはwarmup、測定、planのeventを受け取り、callbackは比較と内部で固定したconnectionの解放後に実行します
成功SELECTとRU取得の間にcallbackや警告queryを挟みません
外側のcaptureはwarmupとplanも含むため、baseline用の測定だけの入力には後述の `WriteCapture` を使います

## Plan、失敗、結果の解釈

`variant.Plan.Executed` と `Warnings` は独立した `EXPLAIN ANALYZE` の結果です
`Plan.Planned` は空です。`PlanStatus` はその独立実行だけを表します

| Status | 意味 |
| --- | --- |
| `unrequested` | Autoには確認対象となるstorage／MPPの明示要求がない |
| `matched` | source storageへのaccessがあり、認識できたsource／関連tableのaccessが要求engineと一致し、MPP強制時はMPP taskもある |
| `mismatch` | 認識できたaccessが別engineを使うか、強制したMPPがない |
| `unknown` | Plan取得に失敗した、taskやtableの対応が未知、またはsource storageへのaccessがない |

[`Where(Has(...))`](aggregates_ja.md#関連行による絞り込み) はsource、target、中間tableに同じpolicyを使います
planではphysical tableとRelation pathを解決し、関連tableが別engineを使う場合もmismatchにします
照合対象は観測できたaccessです。TiDBの最適化で削除されたtableには確認するaccessがありません
physical table名だけでは区別できない場合、特に自己参照ではRelation pathが不明のままになることがあります

`TableDual` のようなstorage accessのないplanではengineを確認できず、強制policyは `unknown` になります
独立planは通常SELECT sampleの実行engineを証明しません
警告は未redactのserver textで値を含む場合があり、reportを共有する前に内容を確認してください

`Complete` は結果照合、測定sample、plan query、警告取得、policy照合がすべて成功した場合に成立します
失敗時はerrorと未完了reportを返し、成功済みsampleと取得済みplanを保持します
SELECT、RU取得、値照合に失敗したものを成功sampleへ含めません
部分的な統計は診断用で、sample数0はcostが0という意味ではありません
policyの不一致や不明は完全なexportを拒否します。警告取得が成功していても、呼び出し側が確認すべき警告を含む場合があります

APIは勝者の選択、applicationのhint変更、レプリカ準備、transaction開始、session設定変更を行いません
ServerRUはTiDBが報告するstatementの消費量で、請求RUではありません
latency、列指向storage、実行頻度、egressなどのcostは別に評価します
[Starter FAQ](https://docs.pingcap.com/tidbcloud/serverless-faqs/)を参照してください

## Baseline確認用のsample出力

完了した比較の1つのvariantを、呼び出し側所有の `io.Writer` へ出力します

```go
err = report.WriteCapture(writer, "tiflash_mpp")
```

既存RuntimeCaptureのJSON Lines形式で、測定SELECTだけを1 sampleにつき1 scopeで記録します
warmup、plan、警告、bind値、結果値は含めません
writerの失敗で部分出力が残る可能性があるため、error時は破棄してください

caseとvariantごとにfileを分けます
基準runを `reference-tiflash.jsonl`、後の同等条件のrunを `current-tiflash.jsonl` へ出力した場合は次を実行します

```sh
tidbgo baseline reference-tiflash.jsonl --workload shop-totals-fixture-v1 > baseline.json
tidbgo analyze current-tiflash.jsonl --workload shop-totals-fixture-v1 --baseline baseline.json
```

両commandの `--workload` に `report.Options.Case` を指定します
caseの同一性は呼び出し側が指定し、exportするrecordやSQL fingerprintには埋め込みません
fingerprintはhintを含むため、既存regression checkは同じvariantとSQL shapeを要求します
engine間の比較にはreportの測定値を使い、TiKVのbaselineがTiFlashのfingerprintを暗黙に許容することはありません
[Baseline coverage](workload-baselines_ja.md)と[再現可能な検証](development_ja.md#集計とtiflashの検証)を参照してください
