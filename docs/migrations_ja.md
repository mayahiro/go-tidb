# SQLマイグレーション

[English](migrations.md)

`tidbgo migrate` はTiDB Cloud Starter向けの明示的なdeployment toolです
新規DB、既存DBへの導入、作成済みup／down SQL、現時点の対象DBを投影する `schema.sql` に対応します
application起動やORM queryではマイグレーションを実行しません

## 接続とファイル

`TIDBGO_DSN` をdeployment環境またはsecret managerで設定します
[go-sql-driver/mysqlのDSN形式](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#dsn-data-source-name)で、DB選択、TCP、TLS検証（`tls=true`）が必要です
`.env` は自動読込しません。`--dsn-env NAME` で別の環境変数を選べます
DSN値をcommand-line引数では受け付けません

CLIはMySQL driverとCLI frameworkを含む独立Go moduleです
root libraryは外部依存を持たず、caller所有の `database/sql` poolを受け取ります
[インストール](../README_ja.md#installation)も参照してください
接続にはautocommitと標準の引用符の扱いが必要です
DSNの任意session変数、複数statement一括実行、安全でないTLS、無制限のlocal file accessは拒否します
`parseTime=true` は任意です。TiDBの識別だけではCloud planを検証できないため、Starter endpointを指定してください

default pathはcurrent directoryを基準にします

```text
migrations/20260921093000123_create_accounts.sql
migrations/20260921104500456_add_flags.sql
schema.sql
log/tidbgo/run-<UTC-time>-<unique-suffix>.log
```

`--dir PATH` でマイグレーションdirectoryを選択します
接続commandの `--schema FILE` はsnapshotの出力先で、マイグレーションdirectoryの外に置きます
`lint` の `--schema` は入力snapshotを指定します

**versionは `.sql` を除くファイル名全体**です
例えば `20260921104500456_add_flags` は数値でなく、大文字小文字を区別する文字列です
ASCIIの英数字、dot、underscore、hyphenを使え、先頭は英数字、最大251 byteです
ファイルは辞書順に並べます。同じtimestamp prefixでも名前が異なれば別versionです
適用済みファイルをrenameすると別versionになるため、適用後のファイル名は維持してください

`new NAME` は `<UTC-milliseconds>_<name>.sql` というtemplateを作成します
nameは小文字の英字で始まり、小文字英字、数字、underscoreを使え、最大128 byteです
完全に同じファイル名が存在する場合は上書きせず失敗します
SQLファイルとsnapshotは各16 MiB、マイグレーション全体は128 MiBかつ20,000ファイルまでです
symlinkは拒否します

各ファイルは `-- tidbgo:up` から始まり、その前にheader commentを置けます
任意で `-- tidbgo:down` を続けます。directiveは引用符やblock commentの外で独立行に置きます
down directiveの直前も含め、statementをsemicolonで区切ってください
存在するsectionにはSQLが必要です。Down section全体を省略すると不可逆になります
各sectionに複数SQLを記載でき、source順に個別送信します

## 適用記録と実行順

`_tidbgo_migrations` は次の2カラムだけを保持します

```sql
version VARCHAR(251) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
```

ファイル登録時にTiDBが `created_at` を決定します
runnerは占有したsessionを一時的にUTCへ設定し、終了時に元のtime zoneへ戻します
tableには所有を示すcommentを付け、同じ予約名を持つ別用途のtableは拒否します

| 操作 | 対象 | 記録の更新 |
| --- | --- | --- |
| `up` | 未登録ファイル名の昇順 | Upの全SQL成功後に登録 |
| `down` | 登録済みversionを `created_at DESC, version DESC` で選択 | Downの全SQL成功後に削除 |
| `baseline` | Downのない確認済み初期ファイル1個 | SQLを実行せずに登録 |

後から追加された古いファイル名も未適用として適用できます
Downは適用日時を遡り、同じ日時の場合だけファイル名降順で決定します。管理されたDBの時計を前提にします
Down済みファイルを再適用すると、新しい登録日時になります
checksum、失敗履歴、SQL単位のDB記録は保持しません

`up` のdefaultは未適用の全ファイルで、`--steps N` は正の個数を指定します
`down` のdefaultは1ファイルで、`--steps N` で変更できます
選択したdownの経路にファイル欠落やDown未定義がある場合、SQL実行や記録削除の前に停止します
選択した経路以外のファイル欠落は実行を妨げません
`status` は未適用ファイル、適用済みversionと日時、ファイルが欠けた適用済みversionを表示します
top-levelの `version` は最後の登録であり、それより小さい名前がすべて適用済みという意味ではありません

## 新規DB

```sh
tidbgo migrate new create_accounts
```

例えば次のようにtemplateを編集します

```sql
-- tidbgo:up
CREATE TABLE IF NOT EXISTS accounts (
    id BIGINT NOT NULL AUTO_RANDOM PRIMARY KEY,
    name VARCHAR(100) NOT NULL
);
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS active BOOL NOT NULL DEFAULT TRUE;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS reviewed BOOL NOT NULL DEFAULT FALSE;

-- tidbgo:down
DROP TABLE IF EXISTS accounts;
```

検査、確認、実行を行います

```sh
tidbgo migrate lint
tidbgo migrate plan
tidbgo migrate up
tidbgo migrate status
tidbgo migrate plan --direction down
tidbgo migrate down
```

`new` と `lint` はofflineです
`plan` はlive DBと適用記録を読み取り、tableを変更せずに実行SQLと管理tableの作成要否を示します
SQLファイルは信頼されたdeployment codeとして扱います
control／session command、管理tableとadvisory-lock関数への直接参照は拒否しますが、軽量なvalidatorは完全なSQL parserやsandboxではありません

## 既存DBへの導入

空のマイグレーションdirectoryから開始します

```sh
tidbgo migrate init
# Review migrations/<UTC-milliseconds>_initial.sql and schema.sql
tidbgo migrate baseline
# Add subsequent migration files after baseline succeeds
```

`init` はmetadataを読み、取得した全SQLをDownなしの初期ファイル1個へ書きます
管理tableの作成やversion登録は行いません
`baseline` はこの1ファイルだけが存在し、適用記録がないことを要求します
Up SQLが現時点のDB snapshotと一致した場合、必要に応じて管理tableを作り、初期ファイル名を登録して `schema.sql` を更新します
application SQLの実行や既存dataの変更は行いません

同じ初期ファイルを空DBへ `up` できます
新規DBでも導入済みDBでも、Downがないため、その記録を変更する前にdownが停止します
記録が残るので後続のupは初期SQLをskipします。生成した初期SQLを既存tableへ繰り返し実行できる必要はありません
結果を別途検討せずにDownを追加したり、記録を削除してこの境界を回避したりしないでください

通常のupは、このtoolの管理tableがない既存DBを拒否します
導入やマイグレーション中は、他toolによるschema変更を調整してください
このtoolのためにapplication DMLを停止する必要はありませんが、metadata queryとDDLはresourceを消費し、latencyへ影響しえます

## Offlineのマイグレーションlint

```sh
tidbgo migrate lint
```

基本検査では全ファイルと存在する両方向を読みます
ファイル形式と、対応する `CREATE TABLE`、`ADD/DROP COLUMN`、単純な `CREATE/ADD/DROP INDEX`、`DROP TABLE` の存在guardを検査します
`IF NOT EXISTS` または `IF EXISTS` の欠落はwarningです
DMLや複雑なALTERなど未対応statementは、未確認として明示します

構造を確認するには、選択したマイグレーションの**適用前**snapshotと、実行順のファイルを明示します

```sh
tidbgo migrate lint --schema before.sql --direction up \
  --file 20260921104500456_add_flags.sql \
  --file 20260921113000789_index_flags.sql
```

Downを確認する場合は `--direction down`、down実行順のファイル、down開始前のsnapshotを指定します
現時点の `schema.sql` から未適用ファイルを推定しません
空のsnapshotは空DBを表します。`dump` が生成したsnapshot、または同等のDB修飾なしCREATE TABLEを使います

schema検査はtable／columnの存在、nullable／unsigned／generated属性、単純なindexの列と一意性を追跡します
明確な不整合はerrorです
型の正規化、長さ、精度、default、式、foreign key、table／index option全体は比較しません
guardによるskip時に定義全体を比較できない場合は未確認とします
未対応変更があれば、その後のschema推定も無効にし、依存するstatementを未確認とします

reportにはschema検査の有無、未確認statement数、DB実行が未検証であることを常に表示します
TiDB固有の制限を個別列挙した検査は行いません
存在guardだけではファイル全体の冪等性や実行成功を保証できないため、新規構築と想定するdown／upを専用TiDBで確認してください
lintの診断は、up／downによる手動復旧を妨げません

## 失敗出力と手動復旧

TiDBのDDLは[通常のtransaction rollbackとは独立してcommitします](https://docs.pingcap.com/tidb/stable/transaction-overview/)
statementと記録更新は別の操作です。Up失敗時にDownを自動実行しません
Downは作成済みSQLを実行し、SQLで削除したdataを復元できません

CLIはup、down、baselineの実行ごとに `log/tidbgo` へ一意のファイルをmode `0600` で作り、成功時も失敗時も保持します
SQL進捗はstderrへ出力し、stdoutは `--json` のために保ちます。結果に `log_file` を含めます
log作成や進捗保存に失敗した場合は実行を停止します
対応SQLの送信前に、開始entryを書き込んでsyncします

entryには日時、ファイル名／version、方向、statement番号、実行時のSQL本文を記録します
開始、成功、serverが返した失敗（`failed`）、切断など結果不明（`unknown`）、未送信（`unexecuted`）を区別します
適用記録の登録／削除は別phaseです
process停止により開始entryだけが残った場合は、結果不明として扱います
DBエラー番号、SQLSTATE、原因はCLI出力とlogの両方へ標準で表示します
接続DSNとpasswordは伏せますが、作成済みSQLとserver messageにはapplicationの値が含まれえます
logはdeployment sourceと同様に保護し、保存期間はtoolの外で管理してください

失敗後は停止してlogとDBを確認し、結果不明ならserverのDDL jobも確認してください
SQLの成功entryは、そのSQLの応答を確認したことを示し、ファイル全体の成功を示しません

| 失敗箇所 | 適用記録 | 復旧 |
| --- | --- | --- |
| Up SQLの途中 | 未登録 | 部分変更を手動で戻すか完了させる、またはguard付きSQLを修正してupを再実行 |
| Down SQLの途中 | 保持 | 部分変更を手動で戻すか完了させる、またはguard付きDown SQLを修正してdownを再実行 |
| 記録更新 | 完了したSQLと異なる可能性 | 構造、data、記録を確認し、手動で一致させる |
| 記録成功後のsnapshot出力 | 更新済み | `dump` でファイルを更新 |

失敗したupは記録がないため、通常のdownの対象になりません
失敗したdownは記録が残るため、通常のupはskipします
自動retry、補償処理、repair commandはありません
手動完了のために記録変更が必要なら、すべてのSQLの影響を確認してから行います
構造だけではdata変更の成功を証明できません
後続ファイルが失敗しても、完了を確認したファイルは完了したままです
Down後はSQLを修正し、再適用できます

接続操作はconnectionを占有し、DB単位の[TiDB advisory lock](https://docs.pingcap.com/tidbcloud/locking-functions/)を使用します
lockは協調するrunner間だけを調整するため、手動SQLや他toolは別途調整してください
CLIのdefault deadlineは30分、lock waitは30秒です
`--timeout` と `--lock-timeout` で変更でき、lock waitは1から3600の整数秒です
SQLは1 statementずつ送信します

## 現時点のschema snapshot

Downも含めて各ファイルの完了後に実DBを読み、`schema.sql` を置き換えます
途中失敗時は最後に完成したsnapshotが残るため、DBの状態と一致しない場合があります
明示的に更新できます

```sh
tidbgo migrate dump
```

Dumpは適用記録を変更しません
snapshotを `schema.Parse`、`check.Schema`、`tidbgo lint . --schema schema.sql` で利用し、[modelとRelationの互換性](schema-checks_ja.md)を検査できます
このsource model検査は `tidbgo migrate lint` とは別です
適用後の孤児参照には[reference audit](reference-audits_ja.md)を使い、`tidbgo audit . --schema schema.sql --dry-run` で事前確認できます

snapshotは精度、default、index、generated column、table option、実行可能なTiDB commentを含む `SHOW CREATE TABLE` の定義を保持します
TiFlash replica設定は別SQLで出力し、非同期のavailabilityは構造へ含めません
`AUTO_INCREMENT=n` と `AUTO_RANDOM_BASE=n` などallocator counterは除外します
snapshotはdata backupではなく、次に採番するIDの復元も保証しません

tableはforeign keyの依存順にし、独立table間も決定的に並べます
同じDBへの修飾を除き、選択したDBへ再実行できるようにします
自己参照には対応し、別DB参照、循環foreign key、view、sequence、未対応object typeは明示的に失敗します
TiFlashはlocation labelなしの0または2 replicaに対応します
vector index SQLはTiDBが返した形で保持します

出力は全体の書込とsync後に置き換え、既存regular fileのpermissionを維持します
書込失敗時は以前の完成したファイルを残します
DB成功とsnapshot成功は別に報告します
完了済みversionがあり `snapshot_updated=false` の場合は出力の確認が必要であり、SQLを再実行する意味ではありません
対象DBに適した保存先を使ってください。Git管理は任意で、toolはGit操作を行いません
snapshotとplan出力には機密のdefault値やcommentが含まれえます

## Goからの利用と出力

```go
runner, err := migrate.New(db, migrate.Config{
    Directory:  "migrations",
    SchemaFile: "schema.sql",
    OnEvent:    persistProgress,
})
if err != nil {
    return err
}
result, err := runner.Apply(ctx, migrate.Up, 0)
// Inspect result.Completed, result.Version, and result.SnapshotUpdated even on error.
```

`persistProgress` はcallerが定義する `func(migrate.Event) error` です
SQLと記録操作の前後に、信頼されたSQLと元のerrorを同期的に受け取ります
開始entryを永続化してからreturnしてください。callback errorで後続実行を停止します
libraryの `error` eventは呼出失敗を示すため、driverのerror型でserver拒否と結果不明を区別します
`OnEvent` は任意で、`log/tidbgo` を自動作成するのはCLIだけです
pool、TLS、cancellation、deployment timingはcallerが所有します
`OperationError.Error()` は元のserver textを含めず、`Unwrap()` で原因を取得できます

全subcommandが `--json` に対応し、versionは文字列です
実行／検証失敗は終了status 1、CLIの不正な使い方はstatus 2です
lintのwarningと未確認だけならstatus 0で、検査範囲を明示します
lintの成功はDB実行の保証ではありません
