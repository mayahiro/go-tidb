# 操作単位のServerRU baseline

[English](workload-baselines.md)

fingerprint別baselineは1 statementの平均costを比較するため、平均RUが変わらないまま同じstatementの実行回数が増えても検出しません

任意のworkload baselineでは、captured scopeごとのRU合計の平均と、scopeごとのDML statement数も比較します

## Captureの前提

既存のruntime captureをoperation境界で使います

```go
capture := orm.NewRuntimeCapture(captureWriter)

// Once for each request, job, or test operation, not once per query.
ctx = orm.WithRuntimeCapture(ctx, capture, orm.CollectServerRU())
// Pass ctx to the existing repository functions.
```

application側にworkload label、query登録、wrapper、追加APIは不要です

[Statement observation](observability_ja.md#structured-runtime-capture)の手順でcapture contextを設定し、既存の処理へ渡します

操作単位で比較する場合は、次の条件でbaselineとcurrentのartifactを別々に用意します

- 1 scopeを1回のoperation呼び出しとし、配下のSELECT、write、preload、自動分割batchを含めます
- 各入力fileの全scopeは同種のoperationと同等の入力条件に統一し、異なるoperation、行数、衝突pattern、transaction policyはworkloadを分けます
- writeで次回の入力が変化する場合は反復間にfixtureを同等の状態へ戻し、costだけでなく処理結果も確認します
- operation全体でServerRU collectionを一貫して有効にし、setup SQL、fixture確認、plan probeはcaptured scopeの外へ置きます
- 全operationの完了とquery rowのcloseを待ち、`capture.Err()` を確認してからartifactを保存します、書き込み中のfileは比較しません

`--workload` はこの前提を入力全体へ宣言するものであり、recordのfilter、SQLからの業務的意味の推定、入力の同等性の証明は行いません

scopeの数値IDは実行間で異なって構いません、各artifact内の `(capture_id, scope_id)` でgroup化してからscopeごとの分布を比較します

## 保存と比較

```sh
tidbgo baseline reference.jsonl --workload sync-video-100-edges > baseline.json
tidbgo analyze current.jsonl --workload sync-video-100-edges --baseline baseline.json
tidbgo analyze current.jsonl --workload sync-video-100-edges --baseline baseline.json --json
```

両側で同じ名前を明示します

名前はASCII英数字で始まる1-128文字で、ASCII英数字、dot、underscore、hyphenを使用できます

安定したscenario名を使い、user data、ID、secret、絶対pathは含めません

両側に5 scope以上が必要で、各scopeに1件以上のrecognized DML、完全なServerRU計測coverage、statementとcollectionのerrorがないことを要求します

未分類raw SQLやplan statementがあるworkloadは比較不能です

1 scope内で100 statementを計測してもoperation sampleは1件です

measured fingerprintごとに5件以上の完全なsampleを要求する既存の条件も適用します

metricごとに次の固定limitを使用します

```text
max(baseline scope mean * 1.30, baseline observed scope maximum)
```

- `RU003`: currentのscope単位RU合計の平均、またはDML statement数の平均がlimitを厳密に超えています、同値はpassします
- `RU004`: 名前の欠落や不一致、scope不足、計測漏れ、error、未対応statement、RU合計のoverflowなどによりworkloadを比較できません

どちらもsuppressできないerrorで、`analyze` のexit statusは `1` になります

不正入力やreference計測の不足では `baseline` が失敗し、baselineを出力しません

`--baseline` がない場合のworkload解析は記述統計なのでcoverageを確認してください、CLI成功はbudget checkの合格ではありません

各metricを独立に比較するため、operationの反復回数を倍にしただけではoperation当たりの平均costは回帰しません

各scopeのUPDATEが10回から100回へ増えれば、statement単位RUが同じでも回帰を検出できます

RU合計が減っていてもstatement数が増えれば回帰になる場合があります

fingerprintのcheckは引き続き有効で、SQL形状が変わった場合はworkload全体のRUが下がっていても `RU002` になります

新しいSQLと実測を確認してからbaselineを置き換えてください、workload modeはrewriteを自動承認しません

候補warningの `RUN005` はこのbudgetと独立してtypedな `Update`／`UpdateWhere` の反復をreportします

`--workload` やbaselineは不要で、一括化可能性や回帰を証明しません、理由付きでsuppressしても `RU003` と `RU004` は無効になりません

## 出力と制約

解析JSONは `workload` にscope数、計測済みscopeとmeasurement coverage、DMLとtransaction controlの件数、scopeごとの `total`、`mean`、`min`、`max` を持つ `server_ru` と `statement_count` を追加します

coverage不足時のRU metricは取得できたsampleだけの統計であり、比較可能なoperation costではありません、計測が0件でもRUが0という意味ではありません

比較JSONは `server_ru_comparison.workload` にstatus、reason、baseline/currentのmetric、limit、metric別のregression flagを追加します

textでは `server_ru_workload` と `server_ru_workload_comparison` 行を使用します

`server_ru_comparison.summary` は引き続きfingerprintだけを集計します

全体のgateにはCLIのexit statusまたはtop-level diagnosticの `summary.errors` を使ってください、fingerprint summaryがpassでもworkload比較がpassとは限りません

baseline formatはversion `1` のままで、任意の `workload` metricを追加します

capture/scope ID、operationごとのrecord、冗長な成功・error counterは保存しません

runtime artifactのformatとSQL生成は変更しません

CLIはofflineで動作し、`--workload` がある場合だけscopeごとのbudget counterを保持します、全statementやRU sampleは保持せず、memoryは異なるscopeと既存の解析identityの数に応じて増加します

budgetはrecognized SELECT、INSERT、UPSERT、UPDATE、DELETEを含みます

BEGIN、COMMIT、ROLLBACKのeventは別途数えますが、そのRUは収集しません

自動RU probeもbudgetの対象外です

TiDBの [`tidb_last_query_info`](https://docs.pingcap.com/tidb/stable/system-variables/#tidb_last_query_info) は直前のDML statementを報告するもので、operation全体の値ではありません

これらの合計は請求RUでもtransaction全体のRUでもありません

collectionはrecognized DMLごとに1 probe round tripを追加するため、専用の計測で使い、計測付きの時間を純粋なapplication latencyとは扱いません

scopeには終了markerがありません

解析側ではoperation全体や末尾の欠落、applicationとしての成功、直接の `database/sql` 呼び出しやcapture contextを渡していない呼び出しを確認できません

statementが記録されなかったoperationは見えず、zero-cost sampleとは扱いません、記録済みtransaction-only scopeも比較できません

artifactの完全性とapplicationの処理結果はこのdiagnosticとは別に確認してください

codeが同じでもplan、cache効果、data distributionによってRUは変化します
