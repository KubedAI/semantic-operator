# Roadmap

Where this project can go next, and what is open to work on. Not a commitment. Everything here is optional. The current StarRocks deployment needs none of it.

## Priority: reuse existing catalog semantics

Today the operator imports only physical schema, from Glue, behind the `catalog.Source` interface. It does not know your business meaning, so someone has to write metric descriptions, synonyms, and access rules by hand.

- **Import semantics from OpenMetadata or DataHub.** Add a semantics source that reads what these catalogs already hold and maps it into the model. Descriptions and glossary terms become `description` and `ai_context`. PII and sensitivity tags become `governance.denyFields`. Ownership and ACLs become `governance` roles. This makes the project complementary to existing catalogs instead of a parallel island, and it is the biggest lever for adoption.

## More engines (behind the two interfaces)

All engine-specific code lives behind `emitter.Dialect` and a small query client. See [EXTENDING-ENGINES.md](EXTENDING-ENGINES.md).

- **Trino, ClickHouse, and DuckDB dialects** (`emitter.Dialect`). About 40 lines each. The per-engine deltas are already written up.
- **A `SQL_DIALECT`-driven client factory** (`internal/dbclient`). Today `SQL_DIALECT` selects the emitter but the DB client is still constructed directly. One factory would let a single env var pick both.
- **More `catalog.Source` implementations.** Unity, Polaris, Hive, or a portable `information_schema` source. The last one also lets `ossiectl derive` work without Glue.

## More examples

- **ClickHouse and Trino examples**, once their dialects land.
- **A data loader for the flights example** so it runs end to end, not model-only. Good first task.

## Easier onboarding

- **A local `kind` quickstart** so people can try the operator without EKS or an AWS account. The open question is the lake. Either point StarRocks at a local Iceberg REST catalog plus MinIO, or use StarRocks-native tables for the demo data.
- **Prebuilt public images** so the quickstart can skip `docker build`. Good first task.

## Bigger bets (design first)

- **A `planner.Planner` interface.** The compiler is currently `planner.Build`, a package-level function. Extracting it behind an interface would let an alternative compiler, for example a MetricFlow-backed one, slot in behind the same MCP, REST, and views adapters without touching them.

## Out of scope (for now)

- Full MetricFlow semantics: multi-hop metrics, cumulative metrics, saved queries.
- Multi-engine federation, and a bundled BI tool.
- Real-time OLAP engines without joins or views (Tier 2). See the scope guardrail in [EXTENDING-ENGINES.md](EXTENDING-ENGINES.md).
