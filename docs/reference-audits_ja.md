# FKを使わない論理参照の検査

[English](reference-audits.md)

GoのRelation宣言で参照関係を、`schema.sql` で物理table・列・keyを表します
物理FKは任意です。source Lintは構造をofflineで検査し、`tidbgo audit` は明示的にDBへ接続して孤児参照を検出します
TiDB 8.5.3で利用できる構成で、8.5.6で追加されたFKの共有lock設定には依存しません

## 構造の検査

```sh
tidbgo lint . --schema schema.sql
```

`model.Meta` の明示model、認識したqueryのsource model、そこからRelationで到達するtargetが対象です
`belongs_to` はsourceからtarget、`has_one` と `has_many` はtargetからsourceへの参照を確認します
pure `many_to_many` の中間tableと `via` のedge modelは両端を検査します
既存の `tidbgo` relation tagで関係を宣言し、列名だけから参照関係を推測しません

| Code | Severity | 内容 |
| --- | --- | --- |
| `REF001` | error | 参照table／key列の不足、または無効なmapping |
| `REF002` | error | 物理key列のSQL基本型または符号が不一致 |
| `REF003` | error | 参照先のidentityを保証する無条件の主キー／一意制約がない |
| `REF004` | warning | 参照検索に対応する可視の完全列index prefixがない |
| `SRC003` | info | sourceからRelation mappingを解決できない |
| `CMP011` | error | `has_one` のtargetにRelation列の一意制約がない |
| `CMP012` | error | pure junctionにsource-target pairと完全一致する一意制約がない |

`INT`／`INTEGER` などSQLの別名を正規化しますが、精度・長さ・照合順序・custom Go型の意味まで一致することは証明しません
等価検索用index prefixは列順が異なっても認めます。TiDBのindex選択やRUを予測する検査ではありません
不可視のunique indexは一意性を保証しますが、既定の検索用indexとしては数えません
Lintの `REF004` は理由付きで抑制できます。参照のerrorは抑制できません

`schema_relations`、`analyzed_schema_relations`、`uncertain_schema_relations` はmodelやqueryとは別にRelation宣言の検査範囲を表します
analyzedにはスキーマのerrorを検出した宣言も含みます。Lintの `SRC003` はinfoです
埋込み、alias、sourceがないmodel、未対応mappingなどは未確認となり得ます
applicationの型を使うreflectionによるmodel検査には `check.Schema` を利用します

## Auditの確認と実行

```sh
# Offline: 解決した参照と生成されるSELECTを確認する
tidbgo audit . --schema schema.sql --dry-run

# Connected: TIDBGO_DSNはデプロイ環境から渡す
tidbgo audit . --schema schema.sql --timeout 30s

# --dry-runに表示された名前で検査対象を指定する
tidbgo audit . --schema schema.sql --relation User.Orders --timeout 10s --json
```

`--schema` は必須で、DSNが選択するDBの構造を表すsnapshotを渡します。Auditはlive schema driftを検証しません
別の環境変数を使う場合は `--dsn-env NAME` を指定します
接続にはDB名、TCP、証明書を検証するTLS (`tls=true`) が必要です
複数statement、DSNのsession変数parameter、無制限のlocal file accessは拒否します。driverは独立CLI moduleに置き、LintもAuditもapplicationのGo codeを実行しません

`--relation` は複数回指定できます。planの `User.Orders` や `models/User.Orders` などの名前を使います
many-to-manyの名前では両端を選択し、片方だけなら `#source` または `#target` を付けます
同じSQLになる検査は一度だけ実行し、他の宣言を `also_declared_by` に表示します
指定はDB検査を絞り込みますが、source入力全体のスキーマ事前検査は必要です

スキーマerror、未解決model／Relation、検査対象の参照がない場合はDBへ接続しません
`--dry-run` は解決した部分を表示できますが、検査範囲が不完全な場合は失敗statusを返します
認識できないcodeや未登録modelはsource検出の範囲外です。対象modelのsourceを含め、認識できるquery terminalがないmodelには `model.Meta` を明示します

各検査は、非NULLの子keyに一致する親がない行が「1件以上存在するか」を返します
孤児行の総数は数えず、行のkeyや値は出力しません。複合keyは全要素が非NULLの行を検査します
soft-deleteのscopeは適用せず、論理削除された親も物理的に存在すれば参照先として認め、論理削除された子も検査します
対象は物理的な参照であり、applicationからの可視性やライフサイクルの方針ではありません

検査は逐次実行し、接続処理全体へ期限を設定します
既定値は30秒で、`--timeout` は正の時間から最大1時間まで指定できます
`LIMIT 1` は結果を制限しますが、走査行数やRUの上限にはなりません。大きいtableに孤児行がなければ全走査になる可能性があります
dry-runのSQLをTiDBの `EXPLAIN` で確認し、Relationの選択や実行時間帯を調整します。期限はserver側の正確な処理量やRUの予算ではありません

## 結果の読み方

JSONには `plan`、`results`、sourceの `statistics`、`diagnostics` を含めます
各resultの `checked` がfalseの場合、`orphan=false` を正常という意味に解釈してはいけません
`complete` は孤児参照の検出有無によらず選択した全検査が完了したことを表し、dry-runや実行未完了ではfalseです
`REF005` は孤児参照、`REF006` は事前検査または実行の未完了を報告します
DB errorや期限到達後も完了済みの結果を保持し、残りは未確認とします。driverの生errorや行の値は出力しません

終了statusは正常なdry-runまたは孤児参照なしの完了が `0`、スキーマ／検査範囲のerrorまたは孤児参照の検出が `1`、無効なoption／接続設定が `2`、実行／出力errorが `5` です

SELECTごとにDBのsnapshotを読みます。成功は観測したデータについての結果で、全参照にまたがる単一snapshotや、その後の全writeを保証しません
一部の[Relation query最適化](queries_ja.md)は参照先の存在を前提とし、その契約をAuditで点検できます
AuditはFK作成、applicationのwrite拒否、関連削除、データ修復を行いません。同時更新を含む厳密な保証には、協調するwrite経路またはDB制約が必要です

TiDBの[transaction snapshot](https://docs.pingcap.com/tidb/stable/transaction-overview/)と[SELECTのtimeout](https://docs.pingcap.com/tidb/stable/dev-guide-timeouts-in-tidb/)も参照してください
型を使う広い検査範囲は[スキーマ互換性guide](schema-checks_ja.md)、snapshotの管理は[migration guide](migrations_ja.md)に記載しています
