# サーバー警告の診断

[English](warnings.md) | [statementの観測](observability_ja.md)

`CollectWarnings` は通常のSELECT、INSERT、UPSERT、UPDATE、DELETE後に `SHOW WARNINGS` を明示的に収集します
repositoryで使うexecutorまたはcontextへ一度設定します

```go
executor := orm.Observe(db, orm.NewStatementLogger(os.Stderr), orm.CollectWarnings())
err := q.ScanAll(ctx, executor, &result)
```

`WithStatementObserver` と `WithRuntimeCapture` も同じoptionを受け取ります

```go
capture := orm.NewRuntimeCapture(captureWriter)
ctx = orm.WithRuntimeCapture(ctx, capture, orm.CollectWarnings())
```

個別のpreloadやbulk statementを含む、認識済みDMLごとにDB通信が1回増えます
poolの `*sql.DB` は結果のcloseと警告取得が終わるまで接続を固定します
`*sql.Conn`、`*sql.Tx` とそれらの `Observe` wrapperにも対応します
内部で取得した接続を解放してからcallbackを実行します。借用sessionを並行利用しないでください
暗黙のEXPLAIN、query再実行、session SET、レプリカ確認は行いません

## 結果と確認範囲

`StatementEvent.Warnings` は `*WarningObservation` を返します

| 状態 | 意味 |
| --- | --- |
| `nil` | 収集未指定、または対象外 |
| `Known=true`、`Warnings` が空、`Error=nil` | SHOW WARNINGSに成功し、0行だった |
| `Known=true`、`Warnings` が空でない | サーバーから取得した行がある |
| `Known=false`、`Error!=nil` | 収集失敗、または収集を省略した |

取得後に接続解放が失敗した場合、取得済み結果と `Error` が同時に存在します
`AuxiliaryStatements` は試行した警告query数、`DiagnosticDuration` は本体の実行時間と別の診断時間です
対象statementや結果scanの失敗後は、以前のsession警告との混同を避けるため収集を省略します
非対応executorや警告queryの失敗は観測結果へ返します
これらの失敗で本体の結果やエラーを置き換えません

呼び出し側は `PlanWarning` の `Level`、`Code`、`Message` を参照できます
メッセージと補助エラーにはSQLの値が含まれる場合があります
`Diagnostics()` は値を含まない固定の要約へ変換します

| コード | 意味 |
| --- | --- |
| `WRN001` | TiDBがMPPを使えない可能性を通知した |
| `WRN002` | その他のサーバー警告、エラー、noteを取得した |
| `WRN003` | 警告収集を正常に完了できなかった |

`WRN001` はwarningレベル、1105コード、TiDBの既知MPPメッセージprefixを合わせて判定します
1105だけではMPP制約と判断しません。未知のメッセージは汎用診断へ残します
noteだけの `WRN002` はinfo、それ以外はwarningです
成功したqueryやCLI解析を自動的に失敗へ変えません

組み込みloggerは分類と件数を出力し、警告本文や補助エラー本文は出力しません
RuntimeCaptureには分類別件数、収集状態、診断時間、補助statement数だけを保存します
`tidbgo analyze capture.jsonl` はfingerprint別に診断を表示し、反復した件数を合算します
JSONには `warning_collections` と `warning_collection_errors` を含めます
以前のcaptureに警告metadataがない場合は未収集であり、正常な0件とは扱いません
CLIのsuppressionでも各コードを指定できます

## MPPと明示的なplan

集計／vectorの `Explain`、`ExplainAnalyze` と集計の `Compare` は、既に同一sessionの警告を取得します
`CollectWarnings` の指定や追加queryなしで、その観測を設定済みobserverとcaptureへ渡します
planの `Diagnostics()` も警告を分類します
`Compare.WriteCapture` は測定sampleだけをexportし、planと警告の観測は含めません

MPP制約にはTiDBがEXPLAIN時だけ公開するものがあります
通常SELECTの警告が空でも、MPPへの対応や実行を証明できません
window SUMの制約は明示的なplanと警告を確認します
`WRN001` はplanの一部でMPPが使われていても発生します
`PLN006` は、MPPを要求したものの認識済みplanにMPPがないことを示します
どちらも性能悪化の証明ではありません
明示planはそのstatementの情報であり、先行する `ScanAll` の実行を示すものではありません

TiDBの[MPP仕様](https://docs.pingcap.com/tidb/stable/use-tiflash-mpp-mode/)、[警告仕様](https://docs.pingcap.com/tidb/stable/sql-statement-show-warnings/)、[MPP警告実装](https://github.com/pingcap/tidb/blob/release-8.5/pkg/sessionctx/variable/session.go)を参照してください

## ServerRUとの関係

TiDB Cloud Starterでは `SHOW WARNINGS` が直前queryのRU情報を変更し、`SELECT @@tidb_last_query_info` は対象statementの警告を置き換えます
どちらの順序でも、両方のprobeで同一実行の情報を正確に取得できません

両optionが有効な場合はServerRUを優先します
警告は不明のままとし、警告SQLを実行せず、`WarningObservation.Error` に `errors.Is` で判定できる `orm.ErrWarningsWithServerRU` を返します
loggerとanalyzerは `WRN003` を出力します
必ずRUを取得する `Compare` の通常sampleにも適用し、独立したplan実行の警告は引き続き通知します

警告とRUは、利用側が明示的に選んだ別の実行で取得します
警告収集だけを有効にした場合も、後続の `LastServerRU` が参照する情報は変わります
後から取得したRUを対象queryの消費量として扱わないでください
[tidb_last_query_info](https://docs.pingcap.com/tidb/stable/system-variables/#tidb_last_query_info-new-in-v4014)も参照してください
