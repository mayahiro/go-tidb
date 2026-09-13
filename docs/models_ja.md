# Struct model metadata

[English](models.md)

`model` packageはapplicationが所有するGo structをgenerated fileとDB接続なしで解析します

metadataはnon-pointer struct type単位でcacheし、offline toolingとscalar query runtimeで共有します

`Query[T]().ScanAll(ctx, executor, &destination)`の受け取り先には、model metadataを持たない別の結果structを使えます

選択fieldは取得元のGo名で対応付け、受け取り先のtagは参照しません。table、column、Relation、論理削除のmetadataは引き続き`T`に属します

詳細は[部分取得結果のscan](queries_ja.md#部分取得結果をsliceで受け取る)を参照してください

## Modelの定義

```go
package app

import (
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type User struct {
	model.Meta `tidbgo:"table=users"`
	ID         int64 `tidbgo:",pk,auto_random"`
	Email      string `tidbgo:"email_address"`
	DeletedAt  time.Time `tidbgo:",soft_delete"`
	OrderCount int64 `tidbgo:"order_count,computed"`
	Password   string `tidbgo:"-"`
	Orders     []Order `tidbgo:"has_many"`
}
```

exported fieldを宣言順にmappingします

scalarの `tidbgo` tagは次の固定grammarを使います

```text
tidbgo:"[column_name][,option...]"
```

第1要素は省略可能なcolumn名です

emptyの第1要素はGo field名の決定的なsnake_caseを使うため、`ID` は `id`、`CreatedAt` は `created_at` にmappingします

第2要素以降はoptionです

`db` を含む `tidbgo` 以外のstruct tag namespaceは無視します

それらのtagはcolumn名の変更やfieldの除外には使用せず、どちらも `tidbgo` tagで指定します

次の宣言はどちらも有効であり、意図するpolicyが異なります

```go
ID uint64 `tidbgo:",pk"`   // Go fieldからid columnを推定する
ID uint64 `tidbgo:"id,pk"` // 物理column名を明示的に固定する
```

すべての物理名を明記したいapplicationでは、推定結果と同じcolumn名も明示できます

したがって `tidbgo:"pkk"` は `pkk` というcolumn名を意味し、`tidbgo:",pkk"` のようなunknown optionは拒否します

意図を推定するdiagnosticはparserのheuristicではなく、独立したlint toolingで扱います

field全体の除外には `tidbgo:"-"` を単独で使用します

unexported fieldは無視します

default table名は宣言したGo type名を決定的なsnake_caseへ変換した値です

例えば `User` は `user`、`UserRole` は `user_role` になります

物理名が異なる場合だけzero-sizeの `model.Meta` markerを埋め込み、`tidbgo:"table=name"` で指定します

table名には64 byte以内のsimple SQL identifierだけを使用できます

primary key fieldにはcolumn位置より後へ `pk` optionを指定し、例は `tidbgo:",pk"` または `tidbgo:"account_id,pk"` です

複数の指定はstruct宣言順のcomposite primary keyになります

field名からprimary keyを暗黙推定せず、primary keyを宣言しないmodelもmetadata解析では有効です

primary keyとは独立したcandidate unique keyは、対象scalar fieldへ同じ論理group名を繰り返して宣言します

```go
type VideoGenre struct {
    ID       int64 `tidbgo:",pk"`
    VideoID  int64 `tidbgo:",unique=video_genre"`
    GenreID  int64 `tidbgo:",unique=video_genre"`
    Priority int64
}
```

group名は64 byte以内のsimple identifierであり、物理SQL index名ではありません

key内のfield順はstruct宣言順です

1 fieldを複数keyへ含める場合は、異なるgroup名の `unique=<group>` を繰り返します

この宣言はprimary keyを置き換えず、完全なgroupのnon-NULL equality valueが最大1 rowを識別することをquery compilerへ伝えます

`check.Schema` はこのcorrectness contractをunconditionalな物理primary keyまたはunique keyと照合し、SQL snapshotから証明できない場合は `CMP015` を報告します

TiDBの `AUTO_RANDOM` primary keyには `auto_random` を追加します

対象は `pk` も指定したnon-pointerのsignedまたはunsigned integerで、1 modelにつき1 fieldだけです

single-rowの `orm.Insert` はこのfieldを省略し、`sql.Result.LastInsertId` をfieldへ反映します

bulk insertもfieldを省略しますが、個別のgenerated IDは反映しません

`COUNT(*) AS order_count` のようなalias付きraw query resultだけでpopulateするfieldには `computed` を使います

computed fieldはbase-table SELECT、INSERT、UPDATEから除外し、primary key、candidate unique key、predicate、order、Relation keyには使用できません

nullableな削除時刻でlogical deleteを制御する場合は、1個までの `time.Time` または `*time.Time` fieldへ `soft_delete` を指定します

```go
DeletedAt time.Time `tidbgo:",soft_delete"`
```

non-pointer fieldではwrite時にGoのzero timeをSQL `NULL` へ、read時にSQL `NULL` をzero timeへmappingします

独立した `nullzero` optionは追加しません

pointer fieldは通常のnullable Go semanticsに従い、nilは `NULL`、zero timeを指すものを含むnon-nil pointerは明示値になります

他のscalar fieldにはzero-to-NULL変換を適用しないため、通常のnullable columnにはpointerまたは `sql.Scanner` typeを使います

対象はphysicalなnon-primary-key fieldである必要があり、`auto_random` または `computed` と併用できません

queryとmutationの挙動は[Query guide](queries_ja.md)と[Mutation guide](mutations_ja.md)で説明します

## Relationの定義

`belongs_to` と `has_one` には `*T`、`has_many` と `many_to_many` には `[]T` または `[]*T` を使います

```go
type Order struct {
	model.Meta `tidbgo:"table=orders"`
	ID         int64 `tidbgo:",pk,auto_random"`
	UserID     int64
	User       *User `tidbgo:"belongs_to"`
}
```

direct Relationに関連するprimary keyが1 fieldの場合、`join` optionを省略すると次の決定的なGo field conventionを使います

- `belongs_to`: `<Relation field><target primary key>` からtarget primary keyへmappingし、例は `UserID:ID`
- `has_one` と `has_many`: source primary keyから `<source type><source primary key>` へmappingし、例は `ID:UserID`

いずれかの名前が異なる場合やcomposite keyの場合はordered joinを宣言します

```go
Records []Record `tidbgo:"has_many,join=TenantID:TenantID,join=ID:ParentID"`
```

pure many-to-many mappingでは物理junction mappingを明示します

```go
Roles []Role `tidbgo:"many_to_many,through=user_roles,source=ID:user_id,target=role_id:ID"`
```

各 `source` optionはsource Go fieldからjunction columnへのmappingです

各 `target` optionはjunction columnからtarget Go fieldへのmappingです

optionを繰り返した順序をcomposite key orderとして保持します

Relation kindはtagの第1要素に指定します

Relation fieldはscalar field listから除外します

payload付きedgeからtarget collectionを読み取る場合は、物理column mappingを繰り返さず既存のRelationを再利用できます

```go
type Clip struct {
    ID         int64       `tidbgo:",pk"`
    ClipGenres []ClipGenre `tidbgo:"has_many,join=ID:ClipID"`
    Genres     []Genre     `tidbgo:"many_to_many,via=ClipGenres.Genre"`
}

type ClipGenre struct {
    ID       int64 `tidbgo:",pk,auto_random"`
    ClipID   int64
    GenreID  int64
    Priority int
    Genre    *Genre `tidbgo:"belongs_to"`
}

type Genre struct {
    ID   int64 `tidbgo:",pk"`
    Name string
}
```

`via` はexported Go relation field名で `has_many`、`belongs_to` の2段を指定します

最終target typeはcollection elementと一致する必要があります。各段の既存の推定または明示joinをcomposite keyも含めて再利用し、fieldの宣言順には依存しません

`via` は `join`、`through`、`source`、`target` と併用できません。`Relation.Via()` でpath、`Relation.Junction()` で導出した物理mappingを参照できます

これはpure-junction contractではなく読み取り専用のprojectionなので、必須payload、surrogate ID、同じsource-target pairの複数edgeを許容します

edge自体の読み書きには通常のqueryとmutationを使います。`AddRelation`、`RemoveRelation`、`ClearRelation` はvia Relationをrejectします

orderingとsoft-deleteの挙動は[edge preload guide](queries_ja.md#payload付きedgeのpreload)を参照してください

Relation valueは通常のGo valueとして直接代入、参照できます

```go
user.Orders = []Order{{ID: 1}}
order.User = &user
```

`go-tidb` はmodel fieldへloaded-state bookkeepingを追加しません

queryを実行するcodeが、requestしたRelationを把握する責任を持ちます

direct Relation mappingはdata integrity contractでもあります

mappingが表すnon-NULL foreign keyまたはtarget key valueは、反対側の既存rowを参照する必要があります

`go-tidb` はruntimeで物理foreign keyを作成、inspect、enforceしません

preloadとRelation predicateは到達できないrowを自然に無視し、relation-first TopN query optimizationはroot joinより前にLimitを適用できるため、orphan target rowがpageをLimit未満にする可能性があります

条件を満たすrelation-only Countはroot joinを省略するため、contract違反のorphan targetまたはjunction rowがあると件数を過大に数える可能性があります

物理constraintまたはapplication write ruleでcontractを維持してください

Relation fieldはI/Oとlazy loadingを行わず、fieldへの代入でRelationを永続化しません

exported anonymous structはdepth-firstでflattenします

duplicate column、invalid SQL identifier、recursive embedding、unsupported field type、不正なmodel marker配置、unsupported tag optionはvalidation errorになります

## Metadataの解析

```go
metadata, err := model.Describe[User]()
if err != nil {
	return err
}

for _, field := range metadata.Fields() {
	fmt.Println(field.GoName(), field.ColumnName(), field.IsPrimaryKey())
}

fmt.Println(metadata.TableName())
primaryKey := metadata.PrimaryKeyFields()
uniqueKeys := metadata.UniqueKeys()
softDeleteField, hasSoftDelete := metadata.SoftDeleteField()

for _, relation := range metadata.Relations() {
	fmt.Println(relation.GoName(), relation.Kind())
}
```

`Describe[User]` と `Describe[*User]` は同じcached immutable descriptorを返します

解析時に `User` のmethod実行、environment credentialの読込、network I/Oは行いません

## Model intentのcheck

`model.Describe` は安全に実行できないmetadataを拒否します

独立した `check` packageは、有効ではあるもののapplicationの意図と異なる可能性がある宣言も報告します

```go
diagnostics := check.Model[User]()
```

callerがruntimeの `reflect.Type` を既に持つ場合は `check.ModelType` を使用できます

どちらのAPIも結果が決定的で、user methodの実行、configurationの読込、DB I/Oを行いません

applicationがmodel typeを明示的に列挙するため、source scanとgenerated registryは不要です

| Code | Severity | 意味 |
| --- | --- | --- |
| `MOD001` | error | model typeまたは実行可能なmetadataが不正 |
| `MOD002` | warning | exported fieldにgo-tidbが無視する `db` tagがある |
| `MOD003` | warning | unexported fieldに未使用の `tidbgo` metadataがある |
| `MOD004` | warning | column位置が既知optionまたは1 editだけ異なる値に見える |
| `MOD005` | info | primary keyがなくprimary-key updateとdeleteを使用できない |
| `MOD006` | warning | custom fieldをscanできるがdatabase argumentとしてbindできない |
| `MOD007` | warning | custom fieldをbindできるがscanできない |

runtimeがmodelをcompileできないため、`MOD001` はsuppressibleではありません

他のdiagnosticは有効または無視される宣言を対象とし、`Suppressible` をtrueにします

`check.Model` が直接返すdiagnosticの判定policyはapplication testが所有します

CLIのsuppressionは `tidbgo analyze` と `tidbgo lint` が生成するdiagnosticだけを対象にします

詳細は[解析guide](checks_ja.md)を参照してください

`MOD004` はmapping behaviorを変更しません

tagの第1要素は引き続きcolumn名です

このruleは値が推定columnと異なり、`pk` などの既知optionと最大1 editだけ異なる場合に限ってwarningを出すため、defaultと同じcolumn名を明示してもwarningになりません

warning対象と同名の物理columnも有効です

物理column type、index、constraintはこのmodel-intent checkの対象外です

これらの事実をofflineで比較する場合はTiDB `CREATE TABLE` snapshotを `schema.Parse` でparseし、そのcatalogを `check.Schema` へ渡します

詳細は[Schema compatibility guide](schema-checks_ja.md)を参照してください

## 対応するscalar representation

現在は次を認識します

- bool
- signedとunsigned integer
- float
- string
- `json.RawMessage` を含むbyte slice
- `time.Time`
- 上記のnamed typeとpointer
- `sql.Scanner` または `driver.Valuer` を実装するtype

applicationは任意のDecimalまたはidentifier libraryを選択できます

`go-tidb` はfieldまたはfield addressが標準database interfaceを実装するか記録し、ユーザー所有modelへDecimal packageをimportしません

## SQL引数とタイムゾーン

`go-tidb` はSQLのplaceholderと引数を分けて扱います

`O'Reilly` などの通常の文字列は、引用符やescape処理を加えず値として渡します

parameterのencodingと、有効にした場合のinterpolationはdatabase driverが処理します

`Raw` と `RawExec` の引数にも同じ規則が適用され、SQL本文は呼び出し側が所有します

通常の `time.Time` はmutationとpredicateのbind引数として保持します

`go-tidb` はSQL literalへの文字列化、UTCへの変換、接続のタイムゾーン変更を行いません

同じdriverと接続設定では、通常の日時引数は `database/sql` による直接実行と同じ意味を持ちます

送信時の表現と保存精度はdriverとSQLの列型で決まります

`go-sql-driver/mysql` v1.10.0では、次の設定がそれぞれ異なる責務を持ちます

| 設定 | 効果 |
| --- | --- |
| `loc` | 送信する非ゼロの `time.Time` をこのlocationへ変換し、parseした日時の読取結果にもこのlocationを設定する、既定はUTC |
| `parseTime=true` | 対応する日時の読取結果を `time.Time` にする、送信時のlocation変換の有効・無効は切り替えない |
| `interpolateParams` | driverによるinterpolationかparameterized executionかを選択する、どちらも通常の日時引数を `loc` へ変換する |
| Sessionの `time_zone` | TiDBの `TIMESTAMP` の解釈と表示、およびsessionに依存するSQL日時関数を制御する、`loc` はこの設定を変更しない |

例えば `2026-09-13 00:30:00 JST` を表す通常の日時引数は、`loc=UTC` では `2026-09-12 15:30:00`、`loc=Asia%2FTokyo` では `2026-09-13 00:30:00` として送信されます

この変換は書き込みだけでなく範囲検索のpredicateにも適用されます

driverの[`loc` と `parseTime` の設定](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#loc)、[interpolationの実装](https://github.com/go-sql-driver/mysql/blob/v1.10.0/connection.go)、[prepared executionの実装](https://github.com/go-sql-driver/mysql/blob/v1.10.0/packets.go)も参照してください

TiDBの `DATETIME` はタイムゾーンを持たず年月日時分秒を保存します

`TIMESTAMP` は入力をsessionの `time_zone` で解釈してUTCで保存し、読み取り時にsessionのタイムゾーンへ戻します

瞬間を表す値では、driverとsessionのタイムゾーンを、例えば両方UTCに揃えます

これらの設定が異なる場合、同じ接続による書き込みと読み戻しの成功だけでは、`TIMESTAMP` が意図した瞬間を表しているとは確認できません

[TiDBのタイムゾーンの仕様](https://docs.pingcap.com/tidb/stable/data-type-date-and-time/#timezone-handling)も参照してください

fieldが瞬間を表すのか現地の年月日時分秒を表すのかはapplicationが決定し、列型だけでは判断できません

明示的な表現が必要な場合はapplicationが選択する `sql.Scanner` / `driver.Valuer` typeで定義できます

`Build` はValuerの `Value` を呼ばずに保持し、実行時に通常の `database/sql` の変換が行われます

Valuerが日時文字列を返す場合、その文字列を値として渡し、通常の日時引数に対する `loc` 変換は適用されません

nullable pointerは通常のNULL規則に従います

文書化された例外として、non-pointerの `soft_delete` fieldのゼロ `time.Time` はSQL NULLとして書き込みます

詳細は[mutation guide](mutations_ja.md#insert)を参照してください

## 現在の境界

model metadataはSQL column type、index、physical constraintを意図的に重複記載しません

独立した `schema` と `check` packageがmodelを変更せずSQL snapshotからこれらの事実を比較できます

query runtimeはSELECTとmutationをofflineでcompileし、明示的に渡した `database/sql` executorで実行できます

`belongs_to` と `has_one` preloadは決定的なinline `LEFT JOIN`、`has_many` と `many_to_many` preloadは決定的なsecondary queryを使います

通常のRelation fieldをpopulateし、dot区切りのnested path、target projection、collection orderに対応します

[Scalar query guide](queries_ja.md)と実行可能な[starter app example](../examples/starter-app/README.md)も参照してください
