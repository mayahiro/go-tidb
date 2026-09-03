# Scalar query

[English](queries.md)

`orm` packageはapplicationが所有するGo structからscalar SELECTを構築します

queryの構築とvalidationにcode generationとDB接続は不要です

## SQLのoffline構築

modelのexported Go field名を使用します

```go
query := orm.Query[Order]().
    Select("ID", "UserID", "Total").
    Where(orm.Equal("UserID", userID)).
    OrderBy(orm.Desc("ID")).
    SeekAfter(lastID).
    Limit(100)

sqlText, arguments, err := query.Build()
```

`Build`はcached model metadataから物理table名とcolumn名を解決します

connectionの作成、SQL実行、environment variableの読込、custom `driver.Valuer`の実行は行いません

identifierにはvalidated metadataだけを使い、valueはbind argumentとしてSQLから分離します

`Query[T]` のmodel typeにはnon-pointerのnamed structを指定します

query methodは同じbuilderを変更して返すため、1個のbuilderを並行して変更しないでください

`Select`を指定しない場合は、mappingされたnon-`computed` scalar fieldをstruct宣言順でprojectionへ含めます

`SELECT *` は使用しません

`Select`は物理column名ではなくGo field名を受け取り、指定順をscan順として保持します

computed fieldはalias付き `Raw[T]` resultだけで使用できます

## 実行済みquery shapeの解析

request、job、analysis testのboundaryでRuntimeCaptureを1回設定すると、実行されたtyped queryのbind-free QueryShapeを記録します

`tidbgo analyze` はapplication所有のquery登録なしでquery-pattern ruleをoffline適用します

```sh
tidbgo analyze runtime.jsonl
tidbgo analyze runtime.jsonl --schema schema.sql
```

schema-aware形式はbind valueとpagination valueを含まないversion付きquery fingerprintをevidenceへ記録します

| Code | Severity | 意味 |
| --- | --- | --- |
| `QRY002` | warning | positive OFFSETがrowをskipし、offsetの増加に伴ってcostが増える |
| `QRY003` | warning | 明示的なpositive LIMITにORDER BYがない |
| `QRY004` | warning | `Contains` または `HasSuffix` がleading wildcard付きLIKE patternを生成する |
| `QRY005` | warning | orderedかつlimitedなcollection `Has` が `EXISTS` fallbackを使用する |
| `QRY006` | error | 渡したsnapshotに解析対象ordered accessが必要とするtableまたはcolumnがない |
| `QRY007` | warning | orderedかつpositive Limitのaccessに一致するdefaultで利用可能なdirect-column index prefixがsnapshotにない |

`tidbgo lint` もpackage loadなしで関連builder flowを解決できるsource query terminalへ `QRY002` から `QRY005` を適用します

`tidbgo lint --schema schema.sql` はconjunctiveな `Equal` filterと同じ方向のorderだけを持つ解決済みroot ordered positive-limit access、および解決済みrelation-first TopN compiler decisionが生成するassociation accessへ `QRY006` と `QRY007` も適用します

RuntimeCaptureで実行されなかったcodeも対象になります

dynamicなRelation名、未解決のRelation metadata、associationのnon-equality filter、mixed order、別statementで変更されたbuilderは推測せずsource coverage statisticsへ反映します

要求されたschema-aware checkを完了できないため `QRY006` はsuppressibleではありません

他のdiagnosticは有効なquery shapeを対象とし、`Suppressible` をtrueにします

reason付きreportとsuppressionの挙動は[解析guide](checks_ja.md)を参照してください

TiDBの[pagination guide](https://docs.pingcap.com/developer/dev-guide-paginate-results/)はpaginated resultのorderを推奨し、offsetが大きくなるほどcompute resourceを消費すると説明しています

applicationでstable cursorを保持できる場合は `SeekAfter` を優先します

`Contains` と `HasSuffix` は意図的にpatternを `%` で開始し、そのmatching behaviorはTiDBの[`LIKE` documentation](https://docs.pingcap.com/tidb/stable/string-functions/#like)に従います

schema-aware ruleは1個のindex候補を構造的に決定できる場合だけ適用します

root accessではpositive `Limit`、同じ方向の `OrderBy`、conjunctiveな `Equal` filterを必要とし、relation-first TopNではruntimeまたはsourceのcompiler decisionが生成したassociation accessを対象にします

activeなdefault soft-delete scopeがある場合は、生成される `IS NULL` のcolumnもequality prefixへ含めます

equality columnはleading prefix内で任意の順序を使用でき、その後にordered columnが続く必要があります

expressionまたはprefix length付きindexはこのcoverageを証明しません

equality filterがsimple unique key全体を制約する場合は最大1 rowだけをorderするため、追加のorder columnがなくてもruleを満たします

partial、invisible、FULLTEXT、SPATIAL indexはこのruleでdefault利用可能なunconditional lookupを証明しません

最初のruleは `Or`、`Not`、range filter、mixed order direction、fallback `EXISTS` accessを意図的に診断しません

statistics、data distribution、collation、optimizer behaviorはconnected concernとして残るため、特定のphysical planを断定しません

実際のaccess pathは `Explain` または `ExplainAnalyze` で確認します

runtime ruleは実行statementへ付加されたQueryShapeを解析します

Raw SQLはtyped query ASTの外にあるため解析しません

`q1:` fingerprintはbind valueを除いたlogical shapeとcompiler shapeを識別します

projection、predicate structure、order、preload、compiler rewriteが変わるとfingerprintも変わり、compiler decisionが変わらない範囲でbind valueまたは `Limit` と `Offset` の値だけが変わる場合は同じSQL placeholder shapeとして同じfingerprintを維持します

`QRY005` はrelation-first TopNを適用できなかったmetadataだけの理由を報告します

例として1 rootあたり1 rowの証明不足、target primary key全体を固定しないpure many-to-many filter、異なるroot order、root predicateまたはactiveなroot soft-delete scope、logical group、複数collection predicate、`SeekAfter` があります

target predicate valueは含めません

fallbackも有効なRelation existence queryであるため、実際のplanを許容できるかは `Explain` または `ExplainAnalyze` で判断します

## Soft delete scope

`tidbgo:",soft_delete"` fieldを1個持つmodelでは、`Build`、`All`、`First`、`Only`、`Exists`、`Count`、`Explain`、`ExplainAnalyze` へ `deleted_at IS NULL` を自動追加します

active rowとlogical deleted rowの両方が必要な場合だけ `WithDeleted` を使います

```go
allVideos, err := orm.Query[Video]().
    WithDeleted().
    OrderBy(orm.Asc("ID")).
    All(ctx, db)
```

`WithDeleted` はsoft-delete fieldを必要とし、root modelだけへ適用します

独立したscopeを持つRelation preloadには影響しません

typed `Raw[T]` は指定したSQLを正として扱うためhidden predicateを追加しません

only-deleted専用methodは追加せず、logical deleted root rowだけが必要な場合は `WithDeleted().Where(orm.IsNotNull("DeletedAt"))` を組み合わせます

## Predicate

1回の `Where` に渡した複数predicateと、後続の `Where` で追加したpredicateはcall順に `AND` で結合します

現在のconstructorは次のとおりです

- `Equal`, `NotEqual`
- `GreaterThan`, `GreaterThanOrEqual`
- `LessThan`, `LessThanOrEqual`
- `In`, `NotIn`
- `IsNull`, `IsNotNull`
- `Between`
- `Contains`, `HasPrefix`, `HasSuffix`
- `Has`
- `And`, `Or`, `Not`

`In` と `NotIn` はtyped sliceを直接受け取ります

```go
videos := orm.Query[Video]().Where(orm.NotIn("ID", excludedVideoIDs))
```

empty sliceの `In` は `FALSE`、empty sliceの `NotIn` は `TRUE` へcompileします

comparison predicateはnil valueを拒否するため、NULL比較には `IsNull` または `IsNotNull` を使います

string patternはliteralの `%`、`_`、固定escape characterをescapeした後、compilerが管理するwildcardを追加します

### Relation predicate

`Has` はrelated rowが1件以上存在することを条件にします

`Has` にtarget model predicateを渡すと、その全てに一致するrelated rowが1件以上存在することを条件にします

```go
admins, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Has(
        "Roles",
        orm.Equal("Name", "admin"),
    )).
    All(ctx, db)
```

Relation名と全target field名にはexported Go field名を使います

存在条件だけならtarget predicateを省略して `Has("Orders")` とします

target scopeではlogical predicateとnestedした `Has` も使えます

`Has` は固定SQL templateではなくRelationの存在を表すlogical predicateです

direct Relationは通常target tableへのcorrelated `EXISTS`、pure `many_to_many` は通常junction-to-targetのcorrelated `EXISTS` へcompileします

compositeなdirect mappingまたはjunction mappingでは宣言した全key componentをcorrelationへ含めます

positive conjunctive contextのfiltered `has_many` または `many_to_many` predicateでは、compilerが `EXISTS` query blockへTiDBの `SEMI_JOIN_REWRITE()` hintを配置します

このhintによりTiDBはfilter済みRelation側をdriving sideとするjoin planを検討できます

unfiltered existence、to-one Relation、`Or` または `Not` 配下のpredicateはhintなしの `EXISTS` を維持します

TiDBの公式仕様は[Optimizer Hints](https://docs.pingcap.com/tidb/stable/optimizer-hints/#semi_join_rewrite)を参照してください

compilerは次の条件をmetadataから証明できるTopN shapeをさらに変換します

- builderにtop-level direct collectionの `Has` が1個だけあり、他のroot predicateがない
- positive `Limit` と `OrderBy` が明示され、orderがroot primary key全体と完全に一致する
- Relation source keyがroot primary key全体である
- direct `has_many` ではRelation target primary keyがtarget keyとconjunctiveな `Equal` predicateで全てcoverされ、1 rootあたり最大1 target rowと証明できる
- pure `many_to_many` ではconjunctiveな `Equal` predicateがtarget primary key全体を固定し、pure-junction contractによってsource-target pairが一意になる
- `SeekAfter` とroot default soft-delete scopeがactiveではない

このshapeではrelation-firstなderived query内で `LIMIT` を適用した後、対象keyだけをroot tableとinline to-one preloadへjoinします

direct `has_many` はtarget tableをfilterしてorderします

pure `many_to_many` はtarget primary keyがRelation key全体で他のtarget条件が不要ならjunction target columnを直接filterし、それ以外は固定したtargetをjoinしてからjunction source keyをorderします

TiDBがroot lookupより前のordered association indexへLimitをpush downできる位置を作るための変換です

data sourceの近くへoperatorを移動する効果はTiDBの[TopN and Limit pushdown guide](https://docs.pingcap.com/tidb/stable/topn-limit-push-down/)を参照してください

物理schemaにforeign keyがなくてもRelation mappingはdata integrity contractです

direct Relationが表すtarget keyは既存source rowを参照する必要があります

pure junctionは既存sourceとtarget rowを参照し、source-target column全体のpairにexact uniqueを設定する必要があります

`go-tidb` はquery実行時にこれらのconstraintを作成または検査しません

relation-first TopNはこのcontractを前提とします

orphan sourceはroot joinより前にpage slotを消費する可能性があり、compilerがtargetをjoinせずjunctionをfilterする場合のorphan targetはRelation existenceを誤ってtrueにする可能性があります

duplicateなpure-junction pairがある場合はroot resultが重複する可能性があります

workloadに応じてschema constraintまたはapplication writeでinvariantを維持してください

compilerは物理indexをofflineでinspectできません

効率的なdirect `has_many` relation-first TopNには通常、equality filter columnの後にroot order順のRelation target keyを置くindexが必要です

例えば `Equal("GenreID", ...)` と `OrderBy(Desc("ID"))` には `(genre_id, video_id)` が該当します

pure `many_to_many` のjunctionには通常target columnの後にsource columnを置くindexが必要で、例えば `(role_id, user_id)` が該当します

empty diagnostic listだけからplanを推定せず、実際のordered range scan、pushed Limit、RUを `ExplainAnalyze` で確認してください

target modelがsoft-delete fieldを持つ場合、`Has` はactive target rowだけを対象にします

deleted targetを明示的に調べる存在条件には `Raw[T]` を使います

Relation predicateは `Build` でDB接続なしにvalidationとcompileを行います

correlationの構築ではRelation keyのGoへの読込、Relation keyのcustom `driver.Valuer` 実行、secondary query、Relationのhydrateを行いません

target predicate argumentはscalar predicateと同じ規則に従い、`Build` は `driver.Valuer` を実行しませんがconnected executionでは `database/sql` が実行する場合があります

Relation全体もhydrateする場合は同じqueryへ `Preload` を明示します

`Preload` のtarget rowは `Has` へ渡したtarget predicateではfilterされません

## Orderとpagination

`OrderBy`は `orm.Asc("Field")` と `orm.Desc("Field")` を受け付けます

fieldの重複、unknown field、unknown directionはcompile時に拒否します

`Limit` と `Offset` はnon-negativeな `int64` とbind parameterを使います

`Offset` には `Limit` が必要です

`SeekAfter`はkeyset paginationを有効にし、`OrderBy` と同じ位置順でvalueだけを受け取ります

```go
query.OrderBy(
    orm.Desc("CreatedAt"),
    orm.Desc("ID"),
).SeekAfter(lastCreatedAt, lastID)
```

`SeekAfter`ではfield名を繰り返しません

offline metadataから一意性を保証できるように、orderにはmodelで宣言した全primary key componentを含めます

`SeekAfter` と `Offset` は併用できません

nilまたはtyped nilのcursor valueはSQL NULLを表します

TiDB既定の固定順序を使い、NULLはASCで先頭、DESCで末尾になります

custom non-pointer valueの `driver.Valuer` をNULL判定のために実行しないため、NULLを表現する場合はnullable pointer representationを使います

## 明示的な実行

`All`、`First`、`Only`、`Exists`、`Count`、`Explain`、`ExplainAnalyze` は既存executorを明示的に渡した場合だけI/Oを行います

```go
orders, err := query.All(ctx, db)
order, err := query.First(ctx, db)
user, err := orm.Query[User]().
    Where(orm.Equal("Email", email)).
    Only(ctx, db)
exists, err := orm.Query[User]().
    Where(orm.Equal("Email", email)).
    Exists(ctx, db)
count, err := orm.Query[Order]().
    Where(orm.Equal("UserID", userID)).
    Count(ctx, db)
plan, err := orm.Query[Order]().
    Where(orm.Equal("UserID", userID)).
    Explain(ctx, db)
runtimePlan, err := orm.Query[Order]().
    Where(orm.Equal("UserID", userID)).
    ExplainAnalyze(ctx, db)
```

`*sql.DB`、`*sql.Conn`、`*sql.Tx` は `orm.QueryExecutor` を実装します

terminalはexecutorのopen、設定、ping、closeを行いません

返された `*sql.Rows` は必ずcloseし、該当するscan、iteration、closeのerrorを確認します

`All`は該当rowが0件の場合にnon-nilのempty sliceを返します

`First`は `LIMIT 1` を適用し、該当rowが0件の場合は `sql.ErrNoRows` を返します

implicit orderは追加しないため、選択するrowに決定性が必要な場合は `OrderBy` を使います

`Only`は `LIMIT 2` で0件、1件、複数件を判定します

0件の場合は `sql.ErrNoRows`、2件以上の場合は `orm.ErrMultipleRows` を返します

どちらのerrorも `errors.Is` で判定します

single-row terminalはbuilderに設定済みの `Limit` を実行時だけ置換し、builder自体は変更しません

`Offset`、predicate、order、projection、keyset stateは維持します

`Exists`は `SELECT 1 ... LIMIT 1` を生成し、modelをscanしません

該当rowが0件の場合は `false, nil` を返します

projectionと通常のorderは存在判定へ影響しないためSQLから除外します

`SeekAfter` がある場合は `OrderBy` をcursor predicateの定義とvalidationに使用します

predicate、`Offset`、keyset stateは維持し、一時的な `LIMIT 1` でbuilder自体を変更しません

`Count`は現在のbuilderが表すrow数を返し、modelをscanしません

predicate、keyset state、`Limit`、`Offset` は維持します

projectionと通常のorderは件数を変えないため除外します

activeな `SeekAfter` がある場合は `OrderBy` をcursor predicateの定義とvalidationに使います

`Limit` と `Offset` がない場合は直接 `COUNT(*)` を使います

`Limit` または `Offset` がある場合はderived `SELECT 1` を数え、paginationを結果へ反映します

条件に一致する全row数が必要な場合は `Limit` と `Offset` を指定しません

`Explain` は `Build` が表すroot SELECTへ `EXPLAIN` を実行し、TiDBのdefault row-format operatorを `[]orm.ExplainRow` として返します

`SelectQuery` だけで利用できるため、mutationとcaller-supplied raw SQLはこのpathへ入りません

inline to-one joinはplanへ含みます

collection preload statementはparent keyを必要とするため含みません

field、runtime boundary、TiDB固有の注意事項は[Statement observation](observability_ja.md#select-explain)を参照してください

`ExplainAnalyze` はcompleteなroot SELECTを実行し、TiDBのruntime planを `orm.ExplainAnalyzePlan` として返すexplicit opt-in terminalです

`Diagnostics` methodは追加のdatabase statementを実行せず、返されたrowから保守的なruntime plan warningを検査します

`ExplainAnalyze` はmappingを一意に判断できるcompiler-owned access aliasをphysical table、Go model、rootからのRelation pathへ解決します

queryを変更するとplanも変わるためprotective `LIMIT` を自動追加しません

実行するSELECTのdatabase resourceとRUを消費し、runtime plan収集のoverheadも追加され得ます

typed mutationとcaller-supplied raw SQLはこのpathへ入りません

collection preload statementは引き続き対象外です

driver登録とconnection securityはcallerの責任です

現在の `go-tidb` はMySQL protocol driverとTiDB Cloud Starter connection constructorを含みません

## Relation preload

`Preload` はexported Go Relation field名を受け取り、parent modelを返すqueryへ明示します

```go
users, err := orm.Query[User]().
    Select("Email").
    Preload("Orders").
    All(ctx, db)
```

`belongs_to`、`has_one`、`has_many`、pure `many_to_many` に対応します

dot区切りのpathでnested Relationをrequestできます

```go
users, err := orm.Query[User]().
    Preload("Orders.User").
    All(ctx, db)
```

`All`、`First`、`Only` はRelation kindとparent query shapeから1つの決定的なstrategyを選びます

- `belongs_to` と `has_one` はinline `LEFT JOIN`
- `has_many` はtarget tableへのsecondary SELECT
- pure `many_to_many` は固定のjunction-to-target JOINを1つ含むsecondary SELECT

collection配下のto-oneはcollection statementへinline joinします

predicate、seek cursor、limit、offset、activeなroot soft-delete scopeがない無制限の `All` はroot collection sourceをkey predicateなしで1回読みます

rootのorder指定は取得rowを制限しないため、このstrategyを維持します

default scopeのsoft-delete root、`First`、`Only`、その他の制約付き `All`、nested collectionはkey batchを使います

soft-delete rootは `WithDeleted` でactive-row scopeを外した場合だけ無制限になります

runtime statisticsまたはresult sizeによるstrategy切替とlazy loadは行いません

collection statementはpreceding statementのrowsをcloseしてから、callerが渡した同じexecutorで実行します

Relationのprojection、collection order、またはdeleted rowが必要な場合は同じ `Preload` にoptionを渡します

```go
users, err := orm.Query[User]().
    Preload(
        "Orders",
        orm.PreloadFields("ID", "Total"),
        orm.PreloadOrderBy(orm.Desc("ID")),
        orm.PreloadWithDeleted(),
    ).
    Preload("Orders.User").
    All(ctx, db)
```

Preload optionのfield名にはtarget modelのGo field名を使います

明示したtarget projectionには必要なtarget Relation keyとnested collection load用のsource keyを自動追加します

optionは指定したpath末尾のRelationへ適用します

`PreloadFields` はto-oneとcollectionの両方に適用できます

`PreloadOrderBy` はcollectionだけに適用でき、to-oneへ指定した場合は `Build` がrejectします

`PreloadWithDeleted` はtargetのsoft-delete fieldを必要とし、指定path末尾のRelationだけへ適用します

指定しない場合はinline JOINとsecondary SELECTの両方でlogical deleted targetを除外します

任意のpreload target predicateには現在未対応です

`Build` はrequestした全Relationをofflineで検証し、inline to-one joinを含む完全なparent SQLを返します

key batch用のcollection bind valueはpreceding rowをscanするまで存在しないため、collection SQLは実行時だけ構築します

明示したprojectionにcollection用のsource keyがなければ末尾へ追加し、通常のfieldとしてhydrateします

inline join keyはSQL columnとして参照するため、lookupだけを目的としてprojectionへ追加しません

`Build` はcustom `driver.Valuer` を実行しません

collection source keyはdatabase rowからreadでき、database argumentとして使える必要があります

custom collection source key typeは `sql.Scanner` と `driver.Valuer` の両方を実装し、`Value` methodは接続済みpreload実行時だけ呼び出します

direct collection target keyもhydration lookupに使うため同じ要件を持ちます

many-to-many target keyはSQL JOINとtarget modelのscanに使いますが、bind argumentまたはmemory上のlookup keyへ変換しません

inline target fieldは通常のresult scanと同じnative representationと `sql.Scanner` typeに対応します

collection hydrationはGo valueまたは `driver.Valuer` resultの完全一致でkeyをgroupingします

`has_many` ではparent source keyとtarget keyが同じrepresentationへround-tripする必要があります

many-to-manyではparent source keyとjunction source columnをsource field typeでscanした値が同じrepresentationになる必要があります

SQL collationでは等しいがbyte representationが異なるstring keyをnormalizeしません

collection source keyはparent順を維持して重複を除きます

NULLのparent source keyはmatchせず、key batchの処理対象にしません

activeなroot soft-delete scopeがない無制限の `All` は各root collection sourceを `IN` predicateなしでSELECTします

decodeできたRelation rowのsource keyがNULLまたはparent resultに存在しない場合はhydrateせずに無視します

default scopeのsoft-delete rootを含む制約付きまたはnested collectionでは、1 batchあたり5,000 bind parameterをbudgetとします

composite key幅に応じて1 batchのkey数を縮小し、生成statementはTiDBの[65,535 placeholder上限](https://docs.pingcap.com/tidb/stable/sql-statement-prepare/)を超えません

composite Relationは完全なkey equalityをORで結び、一部のkey componentだけではmatchしません

collection Relationはrequest順、各nested collection subtreeはdepth-firstで実行します

inline Relationはstatementを追加しません

key batchのsplitがなければ `Preload("Orders").Preload("Roles")` はparent、Orders SELECT、Roles SELECTの3 statementを実行します

`Preload("Orders.User")` はparentと、Userをinline joinしたOrders SELECTの2 statementを実行します

requestまたはjob境界へruntime captureを1回設定すると、実際のroot、inline、preload、split batchのstatement behaviorを自動的に記録します

key batchを使うpure many-to-many SELECTは内部bookkeeping用のjunction source keyを先に選択し、その後にmapped target scalar fieldを全て選択します

```sql
SELECT `j`.`user_id`, `t`.`id`, `t`.`name`
FROM `user_roles` AS `j`
JOIN `roles` AS `t` ON (`t`.`id` = `j`.`role_id`)
WHERE `j`.`user_id` IN (?, ?)
```

無制限のroot collectionでは同じSELECTからkey用の `WHERE` clauseとbind argumentを除きます

targetのsoft-delete filterは独自の `WHERE` conditionを追加する場合があります

junction-to-target JOINは宣言されたtarget key componentを全て使います

返されたjunction rowごとにtarget valueを1件appendし、source-target pairのunique性はdatabase schemaが保証します

junction payloadがapplication behaviorに含まれる場合は通常のedge modelとdirect Relationを使います

生成するpreload statementは明示したmapped fieldを選択し、`SELECT *` を使いません

target rowがないto-many fieldはnilのままです

inline to-one fieldはjoined target keyの全componentがNULLならnilのままとし、一部だけがNULLのcomposite target keyはerrorにします

physical schemaはto-one mappingのtarget側をuniqueにする必要があります

特に `has_one` foreign keyに重複がある場合、cardinality確認用queryでerrorにするのではなくparent result rowが重複します

Relation fieldに独立したloaded stateはありません

collection orderはdatabase resultに従い、`PreloadOrderBy` を指定した場合はそのorder、未指定の場合は順序不定です

`Exists` と `Count` はmodelを返さないためpreload指定を無視します

単一の `*sql.DB` operationでもparentとcollection statementが異なるconnectionを使う可能性があります

全statementで同じtransaction snapshotが必要な場合はTiDBのrepeatable-read snapshot isolationを使う `*sql.Tx` を渡します

`*sql.Tx` は直接作成するか `Transaction` callbackから受け取れます

query methodが暗黙にtransactionを開始することはありません

inline to-one Relationだけを含むpreloadは1 statementで実行するため、cross-statement snapshotを必要としません

## 現在の境界

public query surfaceは `Build`、`All`、`First`、`Only`、`Exists`、`Count`、`Explain`、`ExplainAnalyze`、directまたはpure many-to-many Relation predicate、target projection、collection order、path単位のsoft-delete scopeを指定できるnested directまたはpure many-to-many `Preload` に対応しています

`IDs` は延期しています

scalar builderの範囲外となるJOIN、CTE、aggregateなどにはtyped `Raw[T]` を使います
