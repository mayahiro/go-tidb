# Offline diagnostic report

[English](checks.md)

`go-tidb` はdiagnosticの生成とreportを分離します

application codeがmodel type、query builder、SQL schema snapshotを明示的に選択し、`check.Report` と `tidbgo check` がDB接続なしで1個の固定result policyを適用します

この境界にはgenerated registry、source scan、project YAML、live schema inspectionが不要です

## Diagnosticの生成

applicationが所有するcheck commandでdiagnosticを集約します

```go
diagnostics := make([]check.Diagnostic, 0)
diagnostics = append(diagnostics, check.Model[User]()...)
diagnostics = append(diagnostics, recentOrdersQuery.Diagnostics()...)
diagnostics = append(diagnostics, check.Schema[User](catalog)...)

err := json.NewEncoder(os.Stdout).Encode(diagnostics)
```

`tidbgo check` のJSON inputは `check.Diagnostic` objectのarrayを1個だけ含みます

modelとqueryを明示的に登録する実行例は[`examples/starter-app/cmd/check`](../examples/starter-app/cmd/check)を参照してください

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

現在のmodel checkとquery checkはbind valueを含まず、schema checkは渡されたsnapshotだけを使用します

diagnostic messageにはmodel名、schema identifier、source path、parser errorが含まれる場合があります

diagnostic JSONとreportはdevelopment artifactとして扱い、出力先と保存期間を管理してください

text rendererはterminalへuntrusted inputを書き込む前にcontrol characterをescapeします

JSON outputはstructured dataを維持し、valueをSQLへinterpolateしません
