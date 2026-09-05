# Relation synchronization benchmarks

The integration module compares three ways to reach a desired relation membership and payload using the existing public mutation APIs:

| Candidate | Operations after locking the parent |
| --- | --- |
| `replace` | Delete every edge for the source, then insert the desired edges |
| `set_based` | Delete only edges outside the desired target set, then upsert the desired edges |
| `read_diff` | Read the current edges, compare fixture values in Go, then delete missing targets and insert or upsert changed edges |

These are test candidates, not additional public APIs or automatic compiler rewrites

All candidates use one caller-owned pessimistic transaction and lock the same existing parent row with `SELECT ... FOR UPDATE`, including when the relation is empty

The read-diff candidate also uses a current read (`FOR UPDATE`) for edges, so an earlier transaction snapshot does not become the basis of its diff

TiDB does not provide gap locking, so locking only the existing edge range does not serialize writers that insert new edges into that range. All cooperating writers must follow the same parent-lock protocol. See [TiDB pessimistic transactions](https://docs.pingcap.com/tidb/stable/pessimistic-transaction/)

The fixture uses either a pure junction with primary key `(s, t)` or an edge model with an `AUTO_RANDOM` primary key, unique `(s, t)`, an integer payload, and a 32-byte string payload

All candidates must produce the same membership and payload and leave another source untouched. The differential candidates must also preserve existing edge IDs. Replacement can recreate IDs, so these strategies are not interchangeable when identity or database-managed values matter

Upserts in this fixture can conflict only on the relation pair, because the generated primary key is omitted from input. Do not generalize this to arbitrary unique constraints: TiDB can update a row on any primary or unique-key conflict. See [TiDB update guidance](https://docs.pingcap.com/developer/dev-guide-update-data/)

## Running the comparison

Use the dedicated TiDB test database and DSN described in [Development](development.md#tidb-cloud-starter-integration-tests)

The harness validates the selected database prefix and TiDB version, requires existing `pessimistic` mode and session autocommit, and never changes those settings

It creates `tidbgo_it_sync_roots`, `tidbgo_it_sync_pairs`, and `tidbgo_it_sync_edges`, and drops only tables it successfully created. A pre-existing table is an error, not a cleanup target

```sh
# TIDBGO_TEST_DSN must already point to the dedicated test database.
go -C integration test -run '^TestTiDBCloudStarterRelationSyncCandidates$' -count=1 ./tidbcloud
go -C integration test -run '^$' -bench '^BenchmarkTiDBCloudStarterRelationSync$' -benchmem -benchtime=1x -count=1 ./tidbcloud
```

The matrix has 42 cases: 10 or 100 edges, pure or payload-bearing edges, three strategies, and unchanged, partially changed, or entirely changed membership. Payload-bearing edges also have a payload-only case

Partial changes replace 10% of target keys and change the integer payload on a separate 10%. Payload-only changes keep every target and change 10% of integer payloads

Each case resets and seeds its data before warm-up, each timed operation, and three independent RU samples. Use fixed iteration counts and narrow filters: setup, repeated writes, verification, and RU probes all consume resources

```sh
go -C integration test -run '^$' -bench '^BenchmarkTiDBCloudStarterRelationSync$/^rows_100$/^payload_true$' -benchmem -benchtime=3x -count=3 ./tidbcloud
```

The connected test also checks empty desired sets, repeat execution, rollback, preservation of another source, parent-lock contention starting from an empty relation, and a second transaction with an older snapshot. The snapshot test explicitly requests `REPEATABLE READ` and verifies that a normal SELECT still sees the old empty set before synchronization

## Measurement boundaries

- `ns/op` and `B/op` include the transaction, parent lock, optional edge read and comparison, mutations, and commit, but exclude setup, verification, and RU probes
- `Statement-ServerRU/op` sums the same-session ServerRU of all target SELECT and DML statements, including parent locking and optional diff reads, averaged across three independent samples
- This RU metric excludes BEGIN, COMMIT, diagnostic queries, setup, verification, and cleanup. It is neither transaction-total RU nor billed RU, and cannot establish commit-inclusive savings
- `statements/op` counts SELECT and DML; `writes/op` counts attempted mutation statements, not changed rows or physical storage writes. `tx-controls/op` counts BEGIN and COMMIT separately
- `B/op` is total Go allocation, not peak heap or server memory
- Timings come from a sequential, single-client experiment, not simultaneous randomized trials or a concurrency-throughput test

## Observed trade-offs

On 2026-09-05, Go 1.27.1, darwin/arm64, Apple M1 Max, default parallelism 10, and the project test TiDB with mysql driver v1.10.0, `interpolateParams=true`, and `clientFoundRows=false`, the 100-edge payload workload produced these results

Each cell is the median of three runs' three-sample mean `Statement-ServerRU/op`

| Change | Replace | Set-based | Read-diff |
| --- | ---: | ---: | ---: |
| Unchanged | 10.83 | 6.937 | 6.808 |
| Partial membership and payload | 11.55 | 12.17 | 13.53 |
| Entire membership | 11.02 | 11.71 | 13.86 |
| Payload only | 11.03 | 7.065 | 10.69 |

Differential strategies preserved existing IDs and reduced the measured statement RU for unchanged payload-bearing edges. Read-diff issued no mutation when nothing changed, but issued four statements when membership required both removal and insertion, versus three for the other candidates

Neither differential strategy reduced the measured RU in every shape. These measurements do not justify an always-faster replacement policy or predict an application's total RU from its edge count alone

## Offline planning and profiles

```sh
go -C integration test -run '^$' -bench '^BenchmarkRelationSyncPlanning$' -benchmem -benchtime=100ms -count=3 ./tidbcloud
go -C integration test -run '^$' -bench '^BenchmarkRelationSyncPlanning$/^rows_100$/^payload_true$/^partial$/^set_based$' -benchmem -benchtime=3s -cpuprofile /tmp/tidbgo-relation-sync.cpu -memprofile /tmp/tidbgo-relation-sync.mem -o /tmp/tidbgo-relation-sync.test ./tidbcloud
go -C tools tool pprof -top /tmp/tidbgo-relation-sync.test /tmp/tidbgo-relation-sync.cpu
go -C tools tool pprof -top -alloc_space /tmp/tidbgo-relation-sync.test /tmp/tidbgo-relation-sync.mem
```

Replace `set_based` with `replace` or `read_diff` for another candidate

Offline planning includes key preparation, the fixture-specific Go diff, and mutation SQL construction. It excludes driver conversion, row scanning, locking, transaction control, and network I/O

The Go comparison is intentionally limited to fixture integer keys and payloads. It is not a general equality contract for database collations, NULL values, custom Scanner/Valuer types, or server-normalized values
