# Windows over aggregate results

[日本語](windows_ja.md) | [Aggregates](aggregates.md)

`AggregateQuery.Window` evaluates functions over grouped results, after `Having`
and before final ordering and pagination. Window specifications reference exact
selected output Go names, including aliases. Source fields must first be selected
as aggregate/group outputs. Window outputs cannot reference other window outputs.

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

| Functions | Contract |
| --- | --- |
| `RowNumber`, `Rank`, `DenseRank` | Require window ordering; rank has gaps after ties, dense rank does not |
| `Lag(output, offset)`, `Lead(output, offset)` | Require ordering and a nonnegative offset; zero means the current row, missing rows return NULL |
| `FirstValue`, `LastValue` | Require ordering and read the first/last row of the frame |
| `CountAll`, `Count`, `Sum`, `Avg`, `Min`, `Max` followed by `Over` | Aggregate over the frame; field arguments refer to base aggregate outputs |

Every window expression requires `Over` and `As`. Aliases must be unique across
base and window outputs. `PartitionBy: []string{"ShopID"}` creates independent
partitions. `Over` copies its slices and frame. Distinct and conditional
aggregates are not supported as window functions.

`RowsBetween(start, end)` uses negative offsets for preceding rows, zero for
the current row, and positive offsets for following rows. Use
`UnboundedPreceding` and `UnboundedFollowing` for partition boundaries. Bounds
are validated offline; an explicit ROWS frame requires ordering. Ranking and
offset functions reject frames because they do not use them.

With no explicit frame, SQL uses the whole partition if there is no ordering,
or a RANGE frame from the partition start through the current peer group if
there is ordering. `LastValue` therefore does not generally return the partition's
last row unless the frame includes `UnboundedFollowing`. Use a unique ordering
key for deterministic row numbers, offsets, or ROWS frames with tied values.

`Where` filters source rows; `Having` filters base groups and cannot reference
window outputs. Final `OrderBy` accepts base and window outputs. Window ordering
alone does not order the final result. Grouped empty input produces no rows;
nullable aggregate values and missing neighbors retain SQL NULL semantics.

`Build`, `ScanAll`, explicit plans, and `Compare` support these queries. The
compiler uses a derived aggregate query, places storage hints on physical tables,
and emits MPP settings once on the outer statement. Operator support can prevent
pushdown; inspect same-statement warnings and `plan.Summary()`. See
[TiDB window functions](https://docs.pingcap.com/tidbcloud/window-functions/).
