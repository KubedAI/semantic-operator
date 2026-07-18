# Overview: what this is and why it exists

## One line

A Kubernetes operator that turns certified business metrics into deterministic, governed SQL. AI agents, BI tools, and apps all answer the same question the same correct way.

## What it is

osi-semantic-operator is a semantic layer that runs as a Kubernetes operator. It implements the Apache Ossie (incubating) standard, the vendor-neutral semantic-model spec once called Open Semantic Interchange (OSI). Ossie entered the Apache Incubator in July 2026.

You define metrics, dimensions, and joins once, in an Apache Ossie `SemanticModel` resource under GitOps. The operator validates it against your live StarRocks/Iceberg schema, compiles it, and serves it three ways from one planner:

- an **MCP server** for LLM agents
- a **REST API** for apps
- **governed SQL views** for BI tools (any MySQL-protocol client)

The key idea is that the LLM never writes SQL. It only selects certified metrics and dimensions. A compiler turns that choice into one SQL statement, and applies row and column governance before the query runs.

## The gaps it fills

| Problem without it | What this fixes |
|---|---|
| LLM text-to-SQL is not repeatable. The same question gives different SQL and different numbers. | The planner is a compiler. The same request gives byte-identical SQL every time. |
| LLMs are confidently wrong on business logic. They re-derive "CLV" or "sales per employee," and fan-out joins inflate the numbers. | Metrics are certified definitions the model must use. It cannot re-invent them. |
| Governance is bolted on after the query. Filtering rows and columns after the fact is easy to get wrong. | Governance runs inside the compiler. An unauthorized request fails to compile (HTTP 403) and never leaks. |
| Business logic is tangled with the physical schema. Rename a table and every dashboard breaks. | Metric definitions live in a versioned resource. Physical bindings come from Glue. Schema drift is detected, and the last good build keeps serving. |
| Every tool reimplements the metric. BI, agents, and apps each define "revenue" differently. | One planner, three adapters. One definition everywhere. |

## Why now

Every enterprise is pointing LLM agents at their warehouse, and raw text-to-SQL is the default. It is unsafe and inconsistent. This is the governed, deterministic alternative. It is built on an emerging industry standard (Apache Ossie, backed by 60+ organizations including Snowflake, Databricks, dbt Labs, and Salesforce), so your definitions stay portable and are not locked to one vendor.

## Proven end-to-end

Verified on a live EKS deployment (StarRocks, Iceberg on Glue, Valkey):

- The resource reconciles to Validated, Compiled, Published, DriftDetected=False, with governed views created.
- The compiled model version matches the offline `osictl validate` hash. Determinism holds end-to-end.
- A REST query returns real Iceberg rows plus the exact SQL. Repeat calls return the same request hash and a cache hit.
- A governance denial (an analyst asks for a PII column) returns HTTP 403 at compile time.
- The BI view path and the REST path return identical numbers.

See the [retail example](../examples/starrocks/retail/README.md) to run this, the onboarding guide below to bring people on, and [ARCHITECTURE.md](ARCHITECTURE.md) for how it works.

---

## Onboarding by role

> The LLM selects. The compiler writes.

Three different people touch this system. Everyone chooses only certified metrics and dimensions. A compiler turns that choice into one governed SQL statement. Nobody hand-writes analytical SQL.

### A. The metric author (data engineer)

Owns the `SemanticModel` resource. This is the only role that edits Apache Ossie YAML.

1. Start from the worked example: [`examples/starrocks/retail/model/semanticmodel.yaml`](../examples/starrocks/retail/model/semanticmodel.yaml).
2. The authoring loop:
   ```bash
   osictl validate -f model.yaml        # offline, instant feedback, no cluster
   kubectl apply -f model.yaml          # or git push, and ArgoCD applies it
   kubectl -n semantic-system get semanticmodels -w   # Validated, Compiled, Published, DriftDetected
   ```
3. You maintain metrics, joins, and synonyms. Physical field lists come from Glue with `osictl derive -region <r> -database <db> -out model.yaml` (writes to stdout if `-out` is omitted). The generated file is a full, schema-valid Apache Ossie scaffold. Datasets and fields are populated from the catalog. Metrics, relationships, synonyms, `primary_key`, governance, and views are left as clearly marked `TODO` placeholders. Candidate joins are inferred from key-name conventions and left commented out for you to confirm. Metrics and relationships are never changed for you.
4. Golden rules. A metric is a certified definition, so disagreements about "revenue" are settled in the resource, once. Drift is detected, not silently served. The `spec.osi` block round-trips byte-for-byte as portable Apache Ossie.

Full walkthrough: [AUTHORING.md](AUTHORING.md).

### B. The AI / app developer (consumer)

Never edits the model. Consumes the planner.

- Contract: a request of `{metrics, dimensions, filters, identity}` returns `{columns, rows, sql, requestHash, ...}`.
- Agents (MCP): point the MCP client at `/mcp`. The tools describe themselves. `list_metrics` and `list_dimensions` return names, descriptions, and `ai_context` synonyms so the model grounds user vocabulary. `query_metric(...)` returns the data and the emitted SQL. The agent learns the model by calling these. You do not prompt-stuff the schema.
- Apps (REST): `POST /v1/models/{m}/query`. Use `POST /v1/models/{m}/sql` for a dry run that returns SQL without executing.
- Pass identity (`X-Semantic-Role` for REST). Governance is enforced server-side. A forbidden field is a 403, not an empty result.

### C. The BI analyst (any BI tool)

Touches no YAML.

- Connect your BI tool to StarRocks over the MySQL protocol. StarRocks speaks the MySQL wire protocol, so most BI tools connect natively.
- Use the governed `semantic_views.*` views (for example `sales_by_category_year`), not the raw Iceberg tables.
- The one rule: the view already computed the metric correctly, including fan-out-safe ratios. Building your own aggregate over the raw tables is how you get the wrong number. The naive-vs-governed contrast is shown in [the retail example](../examples/starrocks/retail/README.md#5-with-vs-without-the-semantic-layer-the-money-shot).

### Suggested path

Everyone: read this doc, run the with/without demo in the [retail example](../examples/starrocks/retail/README.md), then follow your role above. Authors finish with `osictl validate` on a metric of their own. Consumers finish with one successful `query_metric` call. Analysts finish with one governed view in a BI chart.
