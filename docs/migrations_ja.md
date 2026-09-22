# バージョン付きSQLマイグレーション

[English](migrations.md)

`tidbgo migrate` はTiDB Cloud Starter向けの明示的なデプロイツールです
新規DB、既存DBの取り込み、up／down SQL、**操作対象DBの現時点の構造**を表す `schema.sql` を扱います
アプリケーション起動やORMのqueryからマイグレーションを実行することはありません

生成したsnapshotは `schema.Parse` と `check.Schema` による[offlineのスキーマ互換性検査](schema-checks_ja.md)や、`tidbgo lint --schema` に利用できます
downでもsnapshotは操作対象DBの現時点の構造に更新されます

## 接続とファイル

デプロイ環境またはsecret managerで `TIDBGO_DSN` を設定します
[go-sql-driver/mysqlのDSN形式](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#dsn-data-source-name)を使用し、DBの選択、TCP、検証付きTLSである `tls=true` が必要です
`.env` は自動読込しません。`--dsn-env NAME` で別の環境変数を選べますが、DSN値をコマンド引数では受け付けません

CLIは独立したGo moduleで、MySQL driverとCLI frameworkを含みます
rootのlibrary moduleにthird-party依存はありません。`migrate` と `orm` packageはcaller所有の `database/sql` pool／executorを受け取り、driverを選びません
CLIのcheckoutからのbuildは [Installation](../README_ja.md#installation) を参照してください
autocommitと既定のquote解釈が必要です
DSNの任意session変数、複数statement実行、安全性を検証しないTLS、無制限のlocal file参照は拒否します
migration自体には `parseTime=true` は必須ではありません
TiDBのversion確認だけではCloud planを判定できないため、Starterの接続先は利用者が指定します

既定のpathはcurrent directoryからの相対pathです

```text
migrations/20260921093000123_create_accounts.sql
migrations/20260921104500456_add_label.sql
schema.sql
```

`--dir PATH` と `--schema FILE` で入力と出力を変更できます。snapshotはmigration directoryの外に置きます
filenameはUTCの年月日時分秒とミリ秒を含む17桁の `YYYYMMDDHHMMSSmmm` versionと、小文字英字で始まり小文字英字・数字・underscoreを含む128 byte以内のnameを使用し、versionの数値順に適用します
不正な日時、version重複、symlink、空のSQL sectionは拒否します
SQLファイルとsnapshotは各16 MiB、migration入力全体は128 MiBまでです

ファイルは任意のheader commentの後に `-- tidbgo:up` で開始し、必要に応じて `-- tidbgo:down` sectionを続けます
区切りは引用文字列とblock commentの外にある独立した行に記述し、downの前のup SQLはsemicolonで終えます
down section全体を省略すると不可逆変更を意味し、sectionがある場合はSQLの記入が必要です
down済みのversionも含め、記録されたmigrationは変更せず保持します。checksumは区切り、comment、空白を含むファイル全体を対象にします

`new` と `init` はUTC時計のミリ秒未満を切り捨ててversionを生成します
同じミリ秒の衝突を含め、最新のlocal version以下になる作成は拒否します。時計を確認するか、後のミリ秒に再実行します
localの同時作成にはmigration directory内の `.tidbgo-create.lock` を使用します
作成が中断した場合は部分的なファイルを確認し、作成処理が動いていないことを確認してからlockを削除します
既存SQLを上書きすることはありません

## 新規DB

```sh
tidbgo migrate new create_accounts
```

生成された1ファイルの両sectionを、例えば次のように記入します

```sql
-- tidbgo:up
CREATE TABLE accounts (
    id BIGINT NOT NULL AUTO_RANDOM PRIMARY KEY,
    name VARCHAR(100) NOT NULL
);

-- tidbgo:down
DROP TABLE accounts;
```

検証、確認、実行を行います

```sh
tidbgo migrate lint
tidbgo migrate plan
tidbgo migrate up
tidbgo migrate status
tidbgo migrate plan --direction down
tidbgo migrate down
```

`new` と `lint` はofflineです
`plan` は実DBと履歴を読み取り、applicationや履歴tableを変更しません。実行するSQLと履歴tableの作成要否を返します
`up` は未適用の全versionを適用し、`--steps N` で正の件数を明示できます
`down` の既定は1 versionで、`--steps N` も使えます。選択した逆方向の全経路を実行前に検証します
既に記録されたversionより小さい番号のmigrationを後から挿入することはできません

## 既存DBへの導入

空のmigration directoryから開始します

```sh
tidbgo migrate init
# migrations/<UTC-timestamp>_initial.sqlとschema.sqlを確認する
tidbgo migrate baseline
```

`init` はDB metadataの読み取りとファイル出力だけを行い、up sectionだけの移植可能な初期migrationを生成します
`baseline` は実DBを再取得し、初期SQLと一致することを確認してから履歴tableを作成し、最初のversionを導入地点として登録します
初期SQLは実行せず、既存applicationのtable、index、データを作り直しません
未適用の後続ファイルがあっても構いませんが、導入地点にできるのは最初のversionだけです
成功時の `baseline` は実DBから `schema.sql` も生成または更新します。`init` で書いたファイルがなくても再生成します

同じ初期up SQLで空DBを初期化でき、以後は共通のmigration列を使えます
既存DBではdownで取り込んだbaselineの日時versionを越えられません
`schema.sql` が更新されても初期SQLは固定です
通常のupは履歴のない空でないDBを自動取り込みせず、処理を停止します

導入・実行中に他のツールが行うschema変更は調整してください
通常のapplication DMLを止めることはツールの導入条件ではありませんが、metadata取得、履歴table作成、DDLはDB resourceを使います
latencyへの影響がないことは保証しません

## 現時点のschema snapshot

downを含め、各migrationの正常完了後に実DBを取得して `schema.sql` を置換します
再適用に必要なmigrationファイルと実行履歴は保持します
snapshotだけを更新する場合は次を実行します

```sh
tidbgo migrate dump
```

`SHOW CREATE TABLE` が返す列精度、default式、index、generated column、table option、TiDBの実行可能commentを保持します
TiFlash replica設定は別のSQLとして出力し、非同期のavailabilityやprogressは構造に含めません
`AUTO_INCREMENT=n` と `AUTO_RANDOM_BASE=n` などの採番現在値は除外し、通常のINSERTで構造fingerprintが変わらないようにします
このsnapshotはデータbackupではなく、次に割り当てるIDの復元を保証しません

tableは外部keyの依存順に並べ、独立したtableの順序も決定的にします
同じDB内の外部keyからDB名の修飾を除去し、再適用先として選択したDBを参照するようにします
自己参照は扱えますが、DBをまたぐ外部keyと循環参照は拒否します
view、sequence、非対応のobjectは黙って省略せずエラーにします
TiFlashは0または2 replica、location labelなしに対応します
vector index定義はTiDBの出力を保持します

出力は完全な書き込みとfile sync後に置換し、既存のregular fileのpermissionを維持します
取得・出力失敗時は前の完全なファイルを残します
DB変更成功後に出力が失敗した場合は `snapshot_updated=false` と非ゼロ終了状態を返し、dumpでSQLを再実行せずに同期できます
dirty状態でのdumpは部分適用の構造を表す場合があり、履歴を修復しません
driftのあるDBをdumpしても、期待するmigration結果として承認した扱いにはしません

接続先DBに対応した出力先を使用します
schema.sqlはGit管理できますが、ツールはGit操作を行いません
CIの照合ではsnapshotと同じ適用versionを再現します
snapshotには機微なdefault値やcommentが含まれる可能性があり、planはSQLファイルの内容を表示するため、application sourceと同様に管理します

## 途中失敗と復旧

TiDBのDDLは[通常のtransaction rollbackとは独立してcommitされます](https://docs.pingcap.com/tidb/stable/transaction-overview/)
downは明示的な逆方向SQLの実行であり、削除した値を復元するものではありません
up失敗時にdownを自動実行することはありません

SQL実行前に各attemptを `_tidbgo_migrations` へ記録します
進捗は完了応答を確認したstatement数です
失敗や応答喪失時はfailedまたはrunningを残し、後続のup／downを停止します
確認済み件数の次のstatementも実行済みかもしれません
status、server DDL job、データを確認し、手動で完了または取り消してから結果を記録します

```sh
tidbgo migrate status --json
tidbgo migrate dump --schema reviewed.sql
# 構造、データ、server DDL jobの完了を独立して確認する
tidbgo migrate repair 20260921113000789 --state applied --expected-schema reviewed.sql --reason 'Verified the completed operation'
```

指定したversionが適用されていないことを確認した場合は `--state reverted` を使います
確認したsnapshotは実DBの構造と一致する必要があり、失敗前の状態へ戻す場合は元の構造fingerprintとの一致も必要です
構造一致だけではbackfillやDMLの成功を証明できないため、利用者が別に確認します
repairはmigration SQLを実行せず、失敗attemptをresolvedにする操作と、理由付きの独立した監査event追加をatomicに行います

次の変更操作では直近に記録した構造fingerprintと実DBを比較し、driftを拒否します
履歴とファイルの不一致、記録済みファイルの削除・改変でも停止します
repairは途中失敗を対象とし、任意のdriftやchecksumの書換えを黙って受け入れません
SQLファイルは信頼するデプロイコードとして扱い、軽量validatorは完全なSQL文法や別DB参照のsandboxを提供しません

接続する全操作は同じconnectionをpinし、DB単位の[TiDB advisory lock](https://docs.pingcap.com/tidbcloud/locking-functions/)を使います
同じ規約に従うrunner同士を排他し、別のツールや手動SQLを排他するものではありません
CLIの操作期限は既定30分、lock待ちは30秒で、`--timeout` と `--lock-timeout` で変更できます
lock待ちは1から3600秒の整数です
SQLは1 statementずつ送信し、自動retryしません
session／transaction制御、履歴tableやadvisory lock関数への直接参照は拒否します

全subcommandで `--json` を使えます
migrationの実行・検証失敗とdirty／driftのあるstatusは終了状態1、CLIの使用方法エラーは2を返します
JSONのversionは整数です。17桁を浮動小数点へ変換せず、64-bit整数または数値文字列を保持するdecoderを使用します
libraryのOperationErrorメッセージにはserverの生のerrorを含めず、制御された診断にはerrors.Unwrapで元のerrorを取得できます

## Goのデプロイツールから使う

```go
runner, err := migrate.New(db, migrate.Config{
    Directory:  "migrations",
    SchemaFile: "schema.sql",
})
if err != nil {
    return err
}
result, err := runner.Apply(ctx, migrate.Up, 0)
// Inspect result even when err != nil, especially Dirty and SnapshotUpdated
```

pool、TLS、cancel、実行時期はcallerが管理します
`migrate.Load` と `migrate.Create` はDB不要で、ORMのAPIからrunnerを呼び出すことはありません
