# 解析とdiagnostic

go-tidbはapplicationへqueryごとのdiagnostic登録を要求せず、必要なevidenceによってcheck経路を分けます

| Evidence | APIまたはcommand | DB access |
| --- | --- | --- |
| Go model metadata | `check.Model[T]` | なし |
| SQL snapshot compatibility | `check.Schema[T]` | なし |
| Go sourceのquery patternとprojection利用 | `tidbgo lint` | なし |
| 実行されたquery shapeとstatement behavior | RuntimeCaptureと `tidbgo analyze` | ServerRU収集を有効にしない限りcaptureはapplication statementだけを実行 |
| TiDB optimizer estimate | `SelectQuery.Explain` | あり |
| TiDB runtime plan | `SelectQuery.ExplainAnalyze` と `ExplainAnalyzePlan.Diagnostics` | あり、SELECTも実行 |

## Modelとschemaのcheck

application所有のmodel typeとschema snapshotは通常のGo testで確認します

```go
func TestUserMapping(t *testing.T) {
    if diagnostics := check.Model[User](); len(diagnostics) != 0 {
        t.Fatalf("model diagnostics: %#v", diagnostics)
    }

    sqlText, err := os.ReadFile("testdata/schema.sql")
    if err != nil {
        t.Fatal(err)
    }
    catalog, err := schema.Parse(string(sqlText))
    if err != nil {
        t.Fatal(err)
    }
    if diagnostics := check.Schema[User](catalog); len(diagnostics) != 0 {
        t.Fatalf("schema diagnostics: %#v", diagnostics)
    }
}
```

どちらもofflineで動作し `[]check.Diagnostic` を返します

`check.Model` は `MOD001` から `MOD007` でmodel intentとtagを検証します

`check.Schema` はquery rewriteが使うcandidate unique-key宣言を含め、`CMP001` から `CMP015` で方向付きcompatibility ruleを適用します

## 実行済みqueryのdiagnostic

request、job、analysis testのboundaryでRuntimeCaptureを1回設定し、既存のORM callへderived contextを渡します

```go
capture := orm.NewRuntimeCapture(writer)
ctx = orm.WithRuntimeCapture(ctx, capture)

if err := runOperation(ctx); err != nil {
    return err
}
if err := capture.Err(); err != nil {
    return err
}
```

生成されたJSON Lines artifactをofflineで解析します

```sh
tidbgo analyze runtime.jsonl
tidbgo analyze runtime.jsonl --schema schema.sql
```

captured statementがtyped QueryShapeを持つ場合、analyzerは次のquery ruleを自動適用します

| Code | 内容 |
| --- | --- |
| `QRY002` | positive OFFSETがpageを返す前にrowをskipする |
| `QRY003` | positive LIMITにdeterministicなorderがない |
| `QRY004` | LIKE predicateがwildcardから始まる |
| `QRY005` | Relation filter付きTopNがEXISTS fallbackを使った |
| `QRY006` | 渡したschemaが解析対象のindex accessを表現できない |
| `QRY007` | ordered limited accessに一致するindex prefixがない |

`QRY006` と `QRY007` には `--schema` が必要です

fingerprintとQueryShapeはbind valueを除外しますが、SQL templateとerrorにはapplication dataが含まれる場合があります

runtime解析はmetadata不足、runtime N+1 SELECT候補、ServerRU収集failure、ServerRU baseline regressionもreportします

明示的な `--workload` ではscope単位のRUとDML statement数のbudgetも比較し、回帰を `RU003`、比較不能を `RU004` として既存のfingerprint ruleに加えsuppress不可能なerrorで示します

scopeとcoverageの条件は[操作単位のbaseline](workload-baselines_ja.md)を参照してください

`RUN004` は同一scopeのtypedな単行 `Insert` または `Upsert` の反復をapplication review候補として示し、attempt数、target duration、取得済みServerRUとmeasurement coverageを付けます

全てのMany callと自動batch分割を除外し、schemaやquery登録は不要で、writeの書き換えやtransaction境界の変更は行いません

`RUN005` は同一capture scope・fingerprint・terminalのtypedな `Update` または `UpdateWhere` の2回以上の試行を、同じ件数・時間・RUのevidenceとともにreportします

suppress可能なwarningであり、loop、異なるrow、一括化可能性、回帰の証明ではありません

raw SQL、soft-deleteの `Delete`／`DeleteWhere`、Relation mutation、1行の呼び出しと自動分割を含む `UpdateMany` は対象外です

行ごとの値、lease条件、atomic increment、実行順、transaction境界、retryを確認してからoperationを変更してください

`UpdateMany` はprimary keyで異なるrowを指し、rowごとの追加条件やapplicationが定めた更新順を要求しない場合の候補です

schema、baseline、`--workload`、application codeの追加は不要です

`--schema` を指定するとcaptured `UpdateWhere` と `DeleteWhere` のscalar predicateもindexと照合します

`QRY008` はsupportedな先頭列boundに対応するindexがない場合のwarning、`QRY009` は未確定coverageのinfo、`QRY006` はschema不足のerrorです

query登録は不要で、source `lint` では適用しません

mutation shape数、判定済みcheck数、未確定check数を分離して境界を示し、一致するprefixがあってもindex利用や低RUを保証しません

capture scope、artifact security、ServerRU cost、baseline比較は[Statement observation](observability_ja.md)を参照してください

## Go source解析

applicationをcompileまたは実行せずsourceを解析します

```sh
tidbgo lint .
tidbgo lint . --json
tidbgo lint . --schema schema.sql
```

source解析は解決済みの `Build`、`All`、`ScanAll`、`First`、`Only`、`Explain`、`ExplainAnalyze` query terminalへ `QRY002` から `QRY005` を適用します

fluent chain、1個のlocal builder定義、local query helper、integerとstring literal、同じfile内の単純なconstantを解決します

関連するpagination、order、leading wildcard有無を全て解決できたterminalは `analyzed_patterns` へ数えます

dynamicな `Limit` または `Offset`、variadic order、未解決predicate helper、別statementで変更されたbuilder、captureされたbuilderは推測せず `uncertain_patterns` へ数えます

orderedかつpositive Limitのroot `Has` では、runtime compilerと同じcollection Relation metadataを解決し、共通の正規化済みrelation-first TopN decisionを適用します

`relation_topn_patterns`、`analyzed_relation_topn_patterns`、`uncertain_relation_topn_patterns` がこのruleのcoverageを表します

解決済みfallbackは `QRY005` を出力し、解決できないRelation名、model、key、order、builder flowは推測せずuncertainとします

source decisionはruntime model metadataと同じ規則で `unique=<group>` candidate keyを認識します

読み取り専用viaではedgeのsource-target pairが宣言済みprimary keyまたはcandidate keyを完全にcoverすることも確認します。証明できないpairは無効なRelationとはせず、理由付き `QRY005` fallbackとして扱います

`--schema` では下記の構造検査の対象modelについて宣言したkeyの物理的な裏付けを確認します
sourceのmodel coverageへ独立して含まれないtargetやedgeを含め、Relation全体の契約は `check.Schema` で検証します

`--schema` を指定するとsource解析はruntime model descriptorと同じ `tidbgo` metadataとdefault naming ruleから物理table名とcolumn名も導出します

明示的なpositive `Limit`、同じ方向の `OrderBy`、conjunctiveな `Equal` filterを解決できたroot shapeだけをruntime解析と共通のneutral index-prefix checkerへ渡します

解決済みのrelation-first TopN decisionがある場合はassociation accessも同じcheckerへ渡します

direct `has_many` accessはtarget equality columnの後にRelation keyが続くindexを検査し、証明済みviaを含む `many_to_many` accessはjunction target columnの後にjunction source columnが続くindexを検査します

default active soft-delete columnはroot query上で `WithDeleted` を解決できない限りequality prefixへ含め、direct Relation targetのsoft-delete columnはassociation equality prefixへ含めます

via edgeのactiveなsoft-delete columnもjunction equality prefixへ含めます

`ForceIndex` がある対応済みroot shapeでは指定したindexだけを検査し、不在は `QRY006`、不適切なprefixは他に適切なindexがあっても `QRY007` とします

source analysisはliteralまたはlocal constantの名前を解決し、動的な名前や不確かな変更はuncertain coverageへ含めます。rootの明示指定時はruntime compileと同じくrelation-first TopNの分析対象から外します

`index_patterns` はordered positive-limit候補を数え、`analyzed_index_patterns` と `uncertain_index_patterns` は照合できたshapeとできなかったshapeを分離します

Relation fallback、associationのnon-equality filter、mixed direction、unknown field、embedded model shape、別statementで変更されたbuilderには推測したindex diagnosticを出さずuncertainとします

`SRC001` は1 function内でresultの全利用を証明できた場合だけprojectionの限定を提案します

repository return、alias、model method、解決できないresult flowは別の `analyzed` と `uncertain` projection counterへ反映します

`ScanAll`もquery patternとschema付きindex checkの対象です。明示した`Select`はexplicit projectionへ計上します。`Select`がない場合はdestination pointerのresult flowを`uncertain`へ計上し、`SRC001`を提案しません

### Modelのスキーマ構造検査

`--schema` は解析したsource内で `model.Meta` を宣言したmodelを、queryがなくても検査します
認識したSELECTと集計terminalのsource modelも対象で、SELECTの `Count` と `Exists` を含みます
無関係なstruct、raw-result struct、`ScanAll` の格納先はmodelの登録として扱いません
queryのprojection・条件・順序・LIMITによらず、computed以外の全mapped列を検査し、同じmodelを使うqueryが複数あっても検査は一度です

| Code | Severity | Modelの構造検査 |
| --- | --- | --- |
| `CMP002` | error | mapped tableが存在しない |
| `CMP003` | error | mapped列が存在しない |
| `CMP007` | error | 宣言した順序付き主キーが一致しない |
| `CMP015` | error | 宣言したcandidate unique keyを裏付ける無条件の物理一意制約がない |
| `CMP010` | warning | 未mapped必須列によりINSERTが失敗する可能性がある |
| `SRC002` | info | sourceからmodel mapping全体を解決できない |

keyの検査は `check.Schema` と同じ証明規則を使います
DB側だけの列でもNULL・default・DB生成で値を補える場合は許容します
読み取り専用または部分的なmodelでは、理由付きで `CMP010` を抑制できます。mappingのerrorは抑制できません
診断はGoの宣言位置を示し、取得できる場合はSQL snapshotの位置も根拠へ含めます

`schema_models`、`analyzed_schema_models`、`uncertain_schema_models` はquery／indexとは別のcoverageを表します
analyzedには照合の結果errorを検出したmodelも含みます
alias、generic宣言、未対応の埋込み、曖昧なtag、入力範囲外のmodelは未確認として `SRC002` を出します
infoのためcommand自体は失敗せず、終了statusが0でも全modelの検査完了を意味しません

対象はtable・列・keyの構造です。custom scalar fieldもmappingが分かれば列を照合できますが、表現形式は推測しません
型family、Go／SQLのNULL許容、生成列への書込み、`AUTO_RANDOM` の一致、Relation全体の契約は引き続き `check.Schema` で確認します
mutationだけで使うmodelや、認識できるterminalがないquery helperのmodelは、`model.Meta` を明示してsourceの検査対象に含めます

schema付きLintは到達可能なRelation targetも辿り、SQL keyの基本型・符号、一意なidentity、to-oneの一意性、pure junction pairの一意性、検索用index prefixを検査します
物理FKは要求せず、未解決のRelationを `SRC003` と3つの `schema_relations` counterで報告します
実データの孤児参照は明示的な `tidbgo audit` で検査します。診断と制限は[論理参照のAudit](reference-audits_ja.md)を参照してください

migrationまたはrollback先を確認する場合は、そのDBを利用するapplication版に対して対象のsnapshotを渡します

```sh
tidbgo lint . --schema schema-before.sql
tidbgo lint . --schema schema-after.sql
```

各snapshotは別途用意します。Lintはmigration SQLのreplay、データ変換の検証、snapshotと実DBの照合を行いません

## Runtime plan diagnostic

`Explain` はroot SELECTを実行せずTiDBへestimate planを要求します

`ExplainAnalyze` はroot SELECTを実行して `orm.ExplainAnalyzePlan` を返します

planの `Diagnostics` は追加のDB callなしで、不完全なstatistics、大きなestimate divergence、大規模full scan、positive disk useを `PLN001` から `PLN004` でreportします

```go
plan, err := query.ExplainAnalyze(ctx, connection)
if err != nil {
    return err
}
diagnostics := plan.Diagnostics()
```

planの選択はdata distributionとstatisticsに依存するため、plan diagnosticと実測ServerRU baselineはstatic ruleを補完しますが将来のplanを保証しません

## Suppressionとexit status

`tidbgo analyze` と `tidbgo lint` はreason付きsuppressionを繰り返し受け付けます

```sh
tidbgo analyze runtime.jsonl --suppress 'RUN002=bounded retry loop'
tidbgo analyze runtime.jsonl --suppress 'RUN004=single inserts are required for generated IDs'
tidbgo analyze runtime.jsonl --suppress 'RUN005=intentional per-row lease boundary'
tidbgo lint . --suppress 'SRC001=full row is intentionally returned'
```

codeは現在のresultに存在し、diagnostic側がsuppressibleである必要があります

未使用、重複、reasonなし、non-suppressibleな指定は拒否します

suppressed diagnosticはtextとJSON outputへ残ります

active error diagnosticがある場合はstatus `1`、warningとinfoだけの場合はsuccessです

invalid inputはstatus `2`、I/Oまたはinternal failureはstatus `5` です

## 現在のcoverage境界

- RuntimeCaptureはderived contextを使ってgo-tidbから実行されたstatementだけを対象にする
- source lintは静的に解決できたbuilder flowとRelation metadataだけへ `QRY002` から `QRY005` を適用する
- source lintへ `--schema` を指定した場合は解決済みrootまたはrelation-first ordered-limit accessだけへ `QRY006` と `QRY007` を適用する
- `--schema` のmodel構造検査はindex patternとは独立して行い、未解決modelは `SRC002` とschema coverage counterへ反映する
- source metadataから証明できないdynamicなRelation名とRelation shapeはRelation uncertainty counterへ反映する
- `EXPLAIN ANALYZE` はSELECTを実行してRUを消費する
- ServerRU収集はrecognized DML statementごとにsame-session diagnostic round tripを1回追加する
- query planとRUは現在のstatistics、data distribution、workloadに依存する

## 集計とvectorの根拠

source lintは集計の `Build`、`ScanAll`、`Explain`、`ExplainAnalyze`、`Compare` terminalを認識します
`AGG001` は静的に確定した元の出力名とGROUP BYについて、exported／一意なalias、必要なgrouping、有効なgroup出力を確認します
等価な選択式はgroup keyを共有できます
`aggregate_patterns`、`analyzed_aggregate_patterns`、`uncertain_aggregate_patterns` がこの限定的なcoverageを表します
動的な式list／名前、escape／alias／別statementで変更されたbuilderは未確定です
predicate、物理型、Relationの一意性、window指定、optimizer対応は `Build`、schema確認、明示実行で検証します

明示的なTiFlash指定にはscalar row-index prefixの助言を適用せず、動的なstorage選択はindex coverage未確定です
集計／vectorのruntime recordはscalar QueryShapeを推定せずSQL fingerprintを持ちます
VEC001からVEC003は[vector診断](vector-search_ja.md#index定義と確認)、operatorの事実は[plan summary](tiflash_ja.md#機能とplanの確認)を参照してください

## サーバー警告の診断

`WRN001` は一部でMPPを使うplanも含めてTiDBのMPP制約警告を通知します
`WRN002` はその他の警告、エラー、noteを値なしで要約し、`WRN003` は警告収集の未完了を通知します
planの `Diagnostics()` と `tidbgo analyze` で利用でき、suppressionにも対応します
[サーバー警告](warnings_ja.md)を参照してください
