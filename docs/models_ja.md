# Struct model metadata

[English](models.md)

`model` packageはapplicationが所有するGo structをgenerated fileとDB接続なしで解析します

metadataはnon-pointer struct type単位でcacheし、offline toolingとscalar query runtimeで共有します

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

TiDBの `AUTO_RANDOM` primary keyには `auto_random` を追加します

対象は `pk` も指定したnon-pointerのsignedまたはunsigned integerで、1 modelにつき1 fieldだけです

single-rowの `orm.Insert` はこのfieldを省略し、`sql.Result.LastInsertId` をfieldへ反映します

bulk insertもfieldを省略しますが、個別のgenerated IDは反映しません

`COUNT(*) AS order_count` のようなalias付きraw query resultだけでpopulateするfieldには `computed` を使います

computed fieldはbase-table SELECT、INSERT、UPDATEから除外し、primary key、predicate、order、Relation keyには使用できません

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

many-to-many mappingでは物理junction mappingを必ず明示します

```go
Roles []Role `tidbgo:"many_to_many,through=user_roles,source=ID:user_id,target=role_id:ID"`
```

各 `source` optionはsource Go fieldからjunction columnへのmappingです

各 `target` optionはjunction columnからtarget Go fieldへのmappingです

optionを繰り返した順序をcomposite key orderとして保持します

Relation kindはtagの第1要素に指定します

Relation fieldはscalar field listから除外します

Relation valueは通常のGo valueとして直接代入、参照できます

```go
user.Orders = []Order{{ID: 1}}
order.User = &user
```

`go-tidb` はmodel fieldへloaded-state bookkeepingを追加しません

queryを実行するcodeが、requestしたRelationを把握する責任を持ちます

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
softDeleteField, hasSoftDelete := metadata.SoftDeleteField()

for _, relation := range metadata.Relations() {
	fmt.Println(relation.GoName(), relation.Kind())
}
```

`Describe[User]` と `Describe[*User]` は同じcached immutable descriptorを返します

解析時に `User` のmethod実行、environment credentialの読込、network I/Oは行いません

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

## 現在の境界

現在のsliceではSQL column type、index、physical constraintをまだ表現しません

query runtimeはSELECTとmutationをofflineでcompileし、明示的に渡した `database/sql` executorで実行できます

`belongs_to` と `has_one` preloadは決定的なinline `LEFT JOIN`、`has_many` とpure `many_to_many` preloadは決定的なsecondary queryを使います

通常のRelation fieldをpopulateし、dot区切りのnested path、target projection、collection orderに対応します

[Scalar query guide](queries_ja.md)と実行可能な[starter app example](../examples/starter-app/README.md)も参照してください
