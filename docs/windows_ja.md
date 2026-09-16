# 集計結果に対するウィンドウ関数

[English](windows.md) | [集計](aggregates_ja.md)

`AggregateQuery.Window` は `Having` 後、最終的な並び順とページング前の集計結果に関数を適用します
window指定はaliasを含む、選択済み出力の正確なGo名を参照します
source fieldは先に集計／group出力として選択します。window出力から別のwindow出力は参照できません

```go
order := []orm.OrderTerm{orm.Asc("Month")}
q := orm.Aggregate[Order]().
    Select(orm.YearMonth("CreatedAt").As("Month"), orm.Sum("Amount").As("Total")).
    GroupBy("Month").
    Window(
        orm.RowNumber().Over(orm.WindowSpec{OrderBy: order}).As("Position"),
        orm.Lag("Total", 1).Over(orm.WindowSpec{OrderBy: order}).As("Previous"),
        orm.Sum("Total").Over(orm.WindowSpec{
            OrderBy: order,
            Rows: orm.RowsBetween(orm.UnboundedPreceding, orm.CurrentRow),
        }).As("Running"),
    ).OrderBy(orm.Asc("Month")).Limit(12)

var result []struct {
    Month    sql.NullInt64
    Total    sql.NullString
    Position int64
    Previous sql.NullString
    Running  sql.NullString
}
err := q.ScanAll(ctx, db, &result)
```

| 関数 | 契約 |
| --- | --- |
| `RowNumber`、`Rank`、`DenseRank` | windowの並び順が必須、同順位の後にRankは欠番を作り、DenseRankは作らない |
| `Lag(output, offset)`、`Lead(output, offset)` | 並び順と非負offsetが必須、0は現在行、存在しない行はNULL |
| `FirstValue`、`LastValue` | 並び順が必須、frame内の先頭／末尾行を参照 |
| `CountAll`、`Count`、`Sum`、`Avg`、`Min`、`Max` に続く `Over` | frameを集計し、field引数は元の集計出力を参照 |

全window式に `Over` と `As` が必要です。aliasは元の集計出力も含めて一意にします
`PartitionBy: []string{"ShopID"}` は独立したpartitionを作ります
`Over` はsliceとframeをコピーします。distinct／条件付き集計のwindow化には対応しません

`RowsBetween(start, end)` は負数が前方、0が現在行、正数が後方の行数です
partitionの境界には `UnboundedPreceding` と `UnboundedFollowing` を使います
境界はofflineで検証し、明示的なROWS frameには並び順が必要です
ranking／offset関数はframeを使わないため指定を拒否します

frame未指定の場合、並び順なしではpartition全体、並び順ありでは先頭から現在の同順位群までのRANGEを使います
そのため `LastValue` は、frameが `UnboundedFollowing` を含まない限りpartitionの末尾を返すとは限りません
同順位がある入力で行番号、offset、ROWS frameを決定的にするには一意な並び順を指定します

`Where` はsource行を、`Having` は元のgroupを絞り込み、`Having` からwindow出力は参照できません
最終 `OrderBy` は元の集計出力とwindow出力を使えます。window内の並び順だけでは最終結果の順序は決まりません
group付きの空入力は0行になり、nullableな集計値や存在しない前後行はSQL NULLを維持します

`Build`、`ScanAll`、明示的なplan取得、`Compare` で利用できます
compilerは集計をderived queryにし、storage hintを物理tableへ、MPP設定を外側に1回だけ配置します
operatorの制約でpushdownできない場合があるため、同一statementの警告と `plan.Summary()` を確認します
[TiDB window functions](https://docs.pingcap.com/tidbcloud/window-functions/)を参照してください
