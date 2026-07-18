# Roadmap

Planned work and follow-ups. This is a living list, not a commitment. Items are
grouped by theme; nothing here is required for the current StarRocks deployment.

## Documentation & repository

- [x] **Architecture diagrams.** Hand-authored SVGs live in [`img/`](img/), with
  Mermaid sources in [`diagrams/`](diagrams/) (`architecture-overview`,
  `system-overview`, `reconcile-loop`, `serving-sequence`). Refresh them if the
  component layout changes.
- [ ] **Docs review pass before external sharing.** Re-read every guide end to
  end for accuracy against the code once the diagrams land — the code and
  `ARCHITECTURE.md` are meant to agree line-for-line (see the note at the top of
  that doc). Verify all cross-links resolve and every command runs as written.
- [ ] **Per-example READMEs stay authoritative.** As engines/use cases are added,
  keep each example's end-to-end in *its* README; the three top-level guides
  (`OVERVIEW`, `ARCHITECTURE`, `DEVELOPER`) stay engine-agnostic.

## Semantics from existing catalogs (priority)

The layer should reuse the business semantics teams already maintain, not make them
re-author everything. Today only *physical* schema is imported (from Glue, behind
the `catalog.Source` interface).

- [ ] **Import business semantics from OpenMetadata / DataHub.** Add a semantics
  source (a sibling to `catalog.Source`) that pulls what data catalogs already hold
  and maps it into the model:
  - table/field descriptions + glossary terms → dataset/field/metric `description`
    and `ai_context` (so metrics carry business meaning, and agents ground user
    vocabulary on it);
  - PII / sensitivity tags → `governance.denyFields`;
  - ownership / ACLs → `governance` roles.

  This answers "where does the business meaning come from," and makes the project
  **complementary** to existing catalogs instead of a parallel island — which also
  lowers the barrier to internal adoption. `osictl` gains an import step; the
  compiled model stays the single source of truth the planner enforces.

## Faster onboarding

- [ ] **Local `kind` quickstart** — a one-command local stack (kind + StarRocks
  FE/BE + Valkey) so users can try the operator without an EKS cluster or AWS
  account. The open design question is the lake: the current demo binds to an
  **AWS Glue** external catalog on S3, which `kind` can't provide. Options to
  resolve first: (a) point StarRocks at a **local Iceberg REST catalog + MinIO**
  (S3-compatible) instead of Glue, via the existing `catalog.Source` seam, or
  (b) a `kind` profile that uses **StarRocks-native tables** (no external
  catalog) for the demo data. Either is a self-contained example, not a doc tweak.
- [ ] **Prebuilt public images** so the quickstart can skip `docker build`.

## More examples

- [ ] **ClickHouse example** (`examples/clickhouse/<usecase>/`) once the
  ClickHouse dialect + client land (see [EXTENDING-ENGINES.md](EXTENDING-ENGINES.md)).
- [ ] **Trino example** pointed at the same Glue/Iceberg catalog StarRocks uses,
  to show catalog reuse across engines.
- [ ] A loader for the flights example so it becomes runnable end to end, not
  model-only.

## Engines & catalogs (behind existing interfaces)

- [ ] **Additional `emitter.Dialect` implementations** — Trino/ClickHouse/DuckDB.
  The two seams and per-engine deltas are specified in
  [EXTENDING-ENGINES.md](EXTENDING-ENGINES.md).
- [ ] **`SQL_DIALECT`-driven client factory** (`internal/dbclient`) so one env var
  selects both the dialect and the DB client in `cmd/server` and `cmd/manager`.
- [ ] **Additional `catalog.Source` implementations** — Unity, Polaris, Hive, or a
  portable `information_schema` source so `osictl derive` works without Glue.

## Explicitly out of scope (for now)

- Full MetricFlow semantics (multi-hop, cumulative, saved queries). The
  `planner.Planner` boundary is documented as the integration point, not built.
- Multi-engine federation and a bundled BI tool.
- Tier-2 engines without joins/views (real-time OLAP). See the scope guardrail at
  the end of [EXTENDING-ENGINES.md](EXTENDING-ENGINES.md).
