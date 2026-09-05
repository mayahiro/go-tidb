# Mutationとraw SQL

[English](mutations.md)

`orm` packageはapplicationが所有するmodelからCRUD statementを構築し、明示的に渡した `database/sql` executorだけで実行します

query構築ではconnectionをopenせず、code generationも要求しません

## Insert

```go
user := User{Email: "ada@example.test"}
affected, err := orm.Insert(&user).Exec(ctx, db)
```

`Insert` は `computed` fieldと1個の `auto_random` fieldを除くmapped scalar fieldを全て書き込みます

single-row insertが成功すると、signedまたはunsigned integerの `auto_random` fieldへ `sql.Result.LastInsertId` を反映します

それ以外のcaller-owned valueは変更しません

non-pointerの `soft_delete` fieldではzero `time.Time` をSQL `NULL` として送ります

pointerのsoft-delete fieldは通常のnullable semanticsに従い、nilは `NULL`、non-nil pointerは明示値になります

single-rowとbulk writeは同じ変換を使います

value sliceとpointer sliceはどちらも自動的に上限内へ分割したmulti-row statementでinsertします

```go
orders := []*Order{orderA, orderB}
affected, err := orm.InsertMany(orders).Exec(ctx, db)
```

`[]Order` と `[]*Order` の両方を扱います

pointer sliceのnil要素は、実行前にzero-basedの行番号付きerrorとして報告します

empty sliceはno-opです

1個の `sql.Result` から全generated valueを取得できないため、`InsertMany` は個別のgenerated IDを反映しません

requestまたはjob境界へruntime captureを1回設定すると、各attempted batchのgroup、位置、row数、予定総数を自動的に記録します

`Exec` は1statementがTiDBの65535 placeholder上限を超える場合だけ決定的に分割します

各statementの最大row数は `floor(65535 / max(1, insert対象field数))` です

bind argumentがないgenerated columnだけのmodelもvirtual field数を1としてrow数を制限します

pointer sliceの全要素を最初のstatementより前に検証し、databaseが報告したaffected row数の合計を返します

`Build` は正確に1個の実行可能statementを表すため、分割が必要な場合は必要statement数を含むerrorを返します

`go-tidb` はbatch全体を囲むtransactionを暗黙に開始しません

後続batchが失敗した場合は、完了したstatementのaffected countと失敗したbatchおよびhalf-openのsource row範囲を含むerrorを返します

全batchをまとめてcommitまたはrollbackする必要がある場合はcaller-owned `*sql.Tx` を渡します

## Upsert

TiDBの `INSERT ... ON DUPLICATE KEY UPDATE` には `Upsert` を使います

```go
affected, err := orm.Upsert(&rating).Exec(ctx, db)
affected, err = orm.Upsert(&rating, "Score", "RatedAt").Exec(ctx, db)
```

field名を省略すると、conflict時にmappedされたwritableなnon-primary-key fieldを全て更新します

field名を渡すと `Update` と同じ検証により選択したGo fieldだけを更新します

insert valueはTiDBの `VALUES(column)` 構文で参照するため、update valueによるbind parameterの追加はありません

value sliceとpointer sliceには対応するbulk operationを使います

```go
affected, err := orm.UpsertMany(ratings).Exec(ctx, db)
```

`UpsertMany` は `InsertMany` と同じplaceholder上限による自動分割、affected countの集約、empty sliceのno-op、transaction境界を持ちます

RuntimeCaptureは実際に実行された全batchについてposition、row range、batch総数を記録します

`Upsert` と `UpsertMany` はgenerated `AUTO_RANDOM` fieldを変更しません

1個の `sql.Result` ではinsertと別のunique keyによるconflictを安全に区別できません

generated IDをmodelへ反映する必要があるsingle-row operationには `Insert` を使います

soft-delete fieldはdefaultのwritable fieldに含むため、activeなzero valueを持つ `Upsert` または `UpsertMany` は `VALUES(deleted_at)` を通じて `NULL` を書き込み、conflictしたlogical deleted rowをrestoreします

upsertでdeletion stateを維持する場合はupdate fieldを明示的に選択します

TiDBはprimary keyまたはunique keyのいずれかのconflictへ反応し、このAPIからconflict targetは選べません

TiDB公式guideは、特に複数unique keyがあるtableではconflictが意図した1rowを特定できる場合だけこのstatementを使うことを推奨しています

返り値はdatabaseが報告したaffected countであり、入力model数と一致するとは限りません

database semanticsとconstraint上の注意は[TiDBのupdate guide](https://docs.pingcap.com/developer/dev-guide-update-data/#use-insert-on-duplicate-key-update)を参照してください

## Update

`Update` はmodel上の全primary key componentでrowを特定します

field名を省略すると、mappedされたwritableなnon-primary-key fieldを全て書き込みます

```go
user.Email = "grace@example.test"
affected, err := orm.Update(&user).Exec(ctx, db)
```

Go field名を渡すとpartial updateになります

```go
affected, err := orm.Update(&user, "Email").Exec(ctx, db)
```

primary key、`auto_random`、`computed` fieldはupdate対象に指定できません

soft-delete modelの `Update` と `UpdateWhere` はdefaultでactive rowだけにmatchします

deletion fieldをclearしてrestoreする場合は `WithDeleted` を使います

```go
affected, err := orm.UpdateWhere[Video](
    orm.Set("DeletedAt", time.Time{}),
).WithDeleted().Where(
    orm.Equal("ID", videoID),
).Exec(ctx, db)
```

`WithDeleted` はprimary-key `Update` でも使用でき、modelにsoft-delete fieldが必要です

上記のvalue形式ではzero timeをSQL `NULL` へ変換し、pointer形式ではnilを使用できます

predicateに一致する任意のrow数を更新する場合はassignmentとpredicateを明示します

```go
affected, err := orm.UpdateWhere[JobLease](
    orm.Set("LockOwner", owner),
    orm.Set("LockUntil", lockUntil),
).Where(
    orm.Equal("JobID", jobID),
    orm.Or(
        orm.IsNull("LockUntil"),
        orm.LessThanOrEqual("LockUntil", now),
    ),
).Exec(ctx, db)
```

`Set` のvalueはbind argumentとして維持します

SQL NULLを代入する場合は `nil` を渡します

同じcolumnへのatomic additionには `Increment` を使います

```go
affected, err = orm.UpdateWhere[JobLease](
    orm.Increment("RetryCount", int64(1)),
    orm.Set("LastError", message),
    orm.Set("LockOwner", nil),
    orm.Set("LockUntil", nil),
).Where(
    orm.Equal("JobID", jobID),
    orm.Equal("LockOwner", owner),
).Exec(ctx, db)
```

`Increment` はnative numeric fieldとcustom `driver.Valuer` を使うfieldを受け入れるため、applicationが選択したDECIMAL表現にも対応できます

physical columnとdeltaがadditionを扱えるかはTiDBが検証します

`Build` はdeltaの `driver.Valuer` を実行しません

`UPDATE` 内の同じcolumnを使うarithmeticの公式例はTiDBの[Transaction overview](https://docs.pingcap.com/tidb/stable/dev-guide-transaction-overview/)を参照してください

`UpdateWhere` は1個以上のassignmentと1個以上のscalar predicateを要求します

Relation predicate、assignmentの重複、primary key、`auto_random`、`computed` fieldの変更は拒否します

無条件のtyped updateはありません

typed SQL expressionは `Increment` による同じcolumnへのadditionだけとし、他のexpressionやjoined updateには `RawExec` を使います

## Delete

全primary key componentでmodelを1件deleteします

```go
affected, err := orm.Delete(&user).Exec(ctx, db)
```

複数rowをdeleteする場合は1個以上のscalar predicateを明示します

```go
affected, err := orm.DeleteWhere[Order](
    orm.Equal("UserID", user.ID),
).Exec(ctx, db)
```

`DeleteWhere` はempty predicate listとRelation predicateを拒否します

専用の無条件typed delete operationはありませんが、predicateが全rowにmatchする場合はあります

空の `NotIn` は `TRUE`、空の `In` は `FALSE` へcompileします

modelに `soft_delete` fieldがある場合、両delete builderはTiDBの `CURRENT_TIMESTAMP(6)` を代入し、`deleted_at IS NULL` を追加する1個の `UPDATE` を生成します

同じoperationを繰り返した場合のaffected rowは0件です

server timestampはcallerが所有するGo modelへ反映せず、statement observationは実際のoperationを `UPDATE` として記録します

tagがないmodelでは従来どおりphysical `DELETE` を使います

soft-delete modelをhard deleteするapplication policyでは明示的な `RawExec` を使います

operation境界でRuntimeCaptureを有効にすると、`UpdateWhere` と `DeleteWhere` はbind valueを含まないscalar predicate metadataを記録します

`tidbgo analyze runtime.jsonl --schema schema.sql` で、queryごとの登録なしにindex prefixを診断できます

supported predicate、未確定coverage、DML EXPLAINの安全性の境界は[条件付きwriteのindex check](observability_ja.md#条件付きwriteのindex-check)を参照してください

## Pure many-to-many Relation

pure many-to-many Relationへtarget keyを追加する場合、junctionへ1個のmulti-row statementを実行します

```go
roleIDs := []int64{adminRoleID, readerRoleID}
affected, err := orm.AddRelation[User](
    "Roles",
    user.ID,
    roleIDs...,
).Exec(ctx, db)
```

Relation引数はsource model上のexported Go field名です

defaultではduplicate junction keyをerrorにします

同じrelationが繰り返し配送される場合だけ、既存junction rowを維持する挙動を明示します

```go
affected, err := orm.AddRelation[User]("Roles", user.ID, roleIDs...).
    IgnoreExisting().
    Exec(ctx, db)
```

`IgnoreExisting` はno-opの [`ON DUPLICATE KEY UPDATE`](https://docs.pingcap.com/developer/dev-guide-update-data/#insert-on-duplicate-key-update) を使い、duplicate以外のinsert errorもwarningへ変換し得る `INSERT IGNORE` は使いません

この挙動はsource-target pairがjunctionで唯一のunique keyになるpure junction invariantを前提とします

選択したtargetまたは1個のsourceに属する全targetを削除できます

```go
affected, err := orm.RemoveRelation[User]("Roles", user.ID, roleIDs...).
    Exec(ctx, db)
affected, err = orm.ClearRelation[User]("Roles", user.ID).
    Exec(ctx, db)
```

single-column Relation keyにはscalar Go valueを渡します

composite mappingではRelation宣言順のcomponentを `CompositeKey` へ渡します

```go
source := orm.CompositeKey(tenantID, userID)
groups := []orm.RelationKey{
    orm.CompositeKey(tenantID, groupAID),
    orm.CompositeKey(tenantID, groupBID),
}
affected, err := orm.AddRelation[User]("Groups", source, groups...).Exec(ctx, db)
```

4個のoperationは全てoffline `Build` に対応します

empty addまたはremove target sliceはno-opです

TiDBの65535 placeholder上限を超えるstatementはpartial successになり得る分割をせず拒否します

このAPIはpayloadのないpure junctionだけを対象とし、application dataを持つjunctionは通常のedge modelとしてCRUDします

## Offline Build

全typed mutationは `Build` に対応します

```go
sqlText, arguments, err := orm.Update(&user, "Email").Build()
```

`Build` はDBへ接続せず、metadata、primary key、field名、predicate safety、placeholder上限を検証します

custom `driver.Valuer` は `Value` methodを実行せずbind argumentとして返します

`InsertMany.Build` と `UpsertMany.Build` は1個のstatementを返し、`Exec`で自動分割が必要な場合はそのことを報告します

## Typed raw result

scalar builderの範囲外となるJOIN、CTE、aggregateなどには `Raw[T]` を使います

```go
type UserSummary struct {
    model.Meta `tidbgo:"table=users"`
    UserID     int64 `tidbgo:"user_id"`
    OrderCount int64 `tidbgo:"order_count,computed"`
}

summary, err := orm.Raw[UserSummary](`
SELECT user_id, COUNT(*) AS order_count
FROM orders
WHERE user_id = ?
GROUP BY user_id`, userID).Only(ctx, db)
```

result column名を `tidbgo` columnへmappingします

partial resultを許可し、SQL expressionには通常fieldまたは `computed` fieldへmappingするaliasを付けます

unknownまたはduplicate result columnは拒否します

scan planはmodel typeとresult column signature単位でcacheします

`Raw[T].Build` はmodelとSQLがemptyではないことを検証しますが、caller-supplied SQLのparse、validation、実行前のresult column確認は行いません

rawの `First` と `Only` はSQLを書き換えないため、必要なorderとlimitはSQL側へ記載します

明示的なmutation escape hatchには `RawExec` を使います

```go
affected, err := orm.RawExec(
    ctx,
    db,
    "UPDATE counters SET value = value + ? WHERE id = ?",
    delta,
    id,
)
```

`RawExec` が検証するのはexecution boundaryとSQLがemptyではないことだけです

model、identifier、predicate、mutation safetyの検証をbypassします

### Raw SQLのsecurity

`Raw[T]` と `RawExec` は渡されたstatementを変更せずexecutorへ送ります

SQL textのparse、sanitize、安全性の証明は行いません

- statementはtrusted application codeに置く
- request parameterを含む未信頼valueは `?` placeholderと別argumentで渡す
- string concatenation、`fmt.Sprintf`、template展開でvalueをSQLへ組み込まない
- placeholderはtable名、column名、sort direction、SQL keywordを表現できないため、動的なSQL構造はclosed allowlistから選ぶ
- 全 `RawExec` callでpredicate範囲、transaction boundary、permissionを確認する

次の例ではvalueがstatementから分離されるため安全です

```go
result, err := orm.Raw[User](
    "SELECT id, email FROM users WHERE email = ?",
    requestedEmail,
).Only(ctx, db)
```

`"... WHERE email = '" + requestedEmail + "'"` のような連結を行ってはいけません

未信頼入力をSQL textへ組み込んだ後ではRaw APIはapplicationを保護できません

Go公式の[SQL injection guidance](https://go.dev/doc/database/sql-injection)を参照してください

applicationが `go-sql-driver/mysql` と `interpolateParams=true` を使う場合は、driverが禁止するBIG5、CP932、GB2312、GBK、SJISとの併用を避けます

interpolationはdriverが行うため、valueを正しくargumentで渡した場合にもこの制限が適用されます

driverの[`interpolateParams` documentation](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#interpolateparams)を参照してください

## Transactionとconnection

`*sql.DB`、`*sql.Conn`、`*sql.Tx` は対応するexecutor interfaceを実装します

mutation methodが暗黙にtransactionをbegin、commit、rollbackすることはありません

複数operationをdefaultの `database/sql` optionで同じtransactionに含める場合は `Transaction` を使います

```go
err := orm.Transaction(ctx, db, func(tx *sql.Tx) error {
    if _, err := orm.Insert(&user).Exec(ctx, tx); err != nil {
        return err
    }
    if _, err := orm.InsertMany(orders).Exec(ctx, tx); err != nil {
        return err
    }
    return nil
})
```

`*sql.DB` と `*sql.Conn` は `TransactionBeginner` を実装します

`Transaction` はcallbackがnil errorを返した場合にcommitし、callbackがerrorを返すかpanicした場合にrollbackします

panicはそのまま再送出します

rollbackが成功した場合はcallback errorを変更せず返し、rollbackも失敗した場合は両方のerrorを結合します

callbackにはconcrete `*sql.Tx` を渡し、callbackはtransaction内の処理を所有しますが、その `*sql.Tx` 自体をcommitまたはrollbackしてはいけません

helperはcallbackをretryせず、nested transactionにも対応しません

custom `sql.TxOptions` または手動のlifecycle管理が必要な場合は `BeginTx` を直接使います

connection設定、ping、close、driver登録、DSN、TLS、retry policy、transaction optionはapplicationの責任です
