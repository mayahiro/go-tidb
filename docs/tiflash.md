# TiFlash policy and explicit preparation

[日本語](tiflash_ja.md) | [Aggregates](aggregates.md) | [Vector search](vector-search.md)

`ReadFrom(orm.TiKV/orm.TiFlash)` and `MPP(orm.MPPAuto/orm.MPPEnforce)` are
available on ordinary `Query`, `Aggregate`, and `Nearest` builders. They request
optimizer hints, not guaranteed execution. Omitted components leave session
behavior unchanged. No session SET or automatic capability probe is issued.
TiKV plus enforced MPP is rejected; ordinary `ForceIndex` plus TiFlash is rejected.

For ordinary queries, the policy follows `All`, `First`, `Only`, `ScanAll`,
`Count`, `Exists`, and explicit plan calls. It applies to physical aliases after
relation rewrites, inline joins, nested EXISTS, and each separate preload SELECT.
MPP settings appear once per statement. A root-lookup-eliminating Count hints
the remaining association table. Cached SQL and other builders are not changed.
Runtime query-shape fingerprints include the requested policy. Scalar index
prefix checks do not claim row-index coverage for an explicit TiFlash request.

## Replica preparation

Package `tiflash` makes preparation explicit and independent of runtime queries:

```go
ddl, err := tiflash.BuildEnableReplica("app", "orders")
if err != nil { return err }
// The caller decides when to execute this DDL, including its operational cost.
if _, err = db.ExecContext(ctx, ddl); err != nil { return err }

prepareCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
defer cancel()
replica, err := tiflash.WaitReplicaReady(prepareCtx, db, "app", "orders", time.Second)
if err != nil { return err }
_ = replica
```

DDL generation quotes schema and table separately and requests two replicas,
the Starter count. `InspectReplica` performs one metadata query and distinguishes
an absent/invisible base table (`ErrTableNotFound`) from a visible table with
`Configured=false`. Permission, connectivity, and unsupported-metadata errors
remain errors. `WaitReplicaReady` requires a context deadline, polls initial
availability, and returns `ErrReplicaNotConfigured` immediately if no replica is
enabled. Zero interval means one second; negative intervals are rejected. Later
errors retain the last successful inspection.

`Available` is sticky initial-readiness metadata, not ongoing health or freshness.
`Progress=1` does not establish synchronization of every replica. Prepare every
referenced table, including relation targets and junctions. See
[replica semantics](https://docs.pingcap.com/tidb/stable/create-tiflash-replicas/).

## Capabilities and plan evidence

`tiflash.ProbeCapabilities(ctx, executor)` explicitly performs five small read-only
checks for replica metadata, MPP setting presence, window functions, vector
functions, and vector-index metadata. It changes no settings. `Supported` means
the narrow probe succeeded; it does not promise support for every operator/table.
MPP settings absent from a successful SHOW yield `Unsupported`. Failed, denied,
or unchecked probes remain `Unknown`, with individual errors and a joined error;
successful results are retained. Errors can contain unredacted server text.

Use aggregate/vector `Explain` or `ExplainAnalyze` to inspect same-session warnings.
Ordinary Select plan terminals retain their existing return types.
`AggregatePlan.Summary()` and `VectorPlan.Summary()` describe every operator's
processing task, estimated/actual output, and immediate child outputs. Actual
counts belong only to Analyze. Children are available inputs, not a measured
consumption count or sum of all descendant scans. Unknown tree formats retain
individual operators but leave `TreeKnown=false`, children and result unavailable.
Unknown task strings remain unknown. These summaries do not infer transfer bytes,
ordinary-query latency, billed RU, or missing pushdown from a root final aggregate.
