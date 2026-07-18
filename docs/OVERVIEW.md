# Overview: what this is and why it exists

## One line

A Kubernetes operator that turns certified business metrics into **deterministic, governed** SQL — so AI agents, BI tools, and custom apps all answer the same question the same correct way.

## The blurb

**osi-semantic-operator** is a semantic layer that runs as a Kubernetes operator on EKS, implementing the **Apache Ossie (incubating)** standard — the vendor-neutral semantic-model spec formerly known as Open Semantic Interchange (OSI), which entered the Apache Incubator in July 2026.

You define metrics, dimensions, and joins once as an Apache Ossie `SemanticModel` custom resource under GitOps. The operator validates it against your live StarRocks/Iceberg schema, compiles it, and serves it three ways from a single planner:

- an **MCP server** for LLM agents,
- a **REST API** for apps, and
- **governed SQL views** for BI tools (any MySQL-protocol client).

The key idea: **the LLM never writes SQL.** It only selects certified metrics and dimensions; a deterministic compiler turns that selection into exactly one SQL statement, with row/column governance applied *before* the query is emitted.

## The gaps it fills

| Problem without it | What this fixes |
|---|---|
| **LLM text-to-SQL is nondeterministic** — same question → different SQL → different numbers | The planner is a compiler: same semantic request → byte-identical SQL every time |
| **LLMs are confidently wrong on business logic** — they re-derive "CLV" or "sales per employee" and fan-out joins inflate denominators | Metrics are certified definitions the model *must* use; it cannot re-invent them |
| **Governance is bolted on after the query** — post-hoc row/column filtering is easy to get wrong | Governance is applied *inside* the compiler; an unauthorized request **fails to compile** (HTTP 403), it never leaks |
| **Business logic tangled with physical schema** — rename a table, break every dashboard | Metric definitions live in a versioned CR; physical bindings derived from Glue. Schema drift is *detected*, and the last-good compiled artifact keeps serving |
| **Every tool reimplements the metric** — BI, agents, apps each define "revenue" differently | One planner, three adapters (MCP/REST/views) → one definition everywhere |

## Why now

Every enterprise is pointing LLM agents at their warehouse, and raw text-to-SQL is the default — unsafe and inconsistent. This is the governed, deterministic alternative, built on the emerging industry standard (**Apache Ossie**, 50+ vendors including Snowflake, Databricks, dbt Labs, Salesforce) so your definitions are portable, not locked to one vendor.

## Proven end-to-end

Verified on a live EKS deployment (StarRocks + Iceberg/Glue + Valkey):

- CR reconciles to `Validated / Compiled / Published / DriftDetected=False` with governed views created.
- Compiled model version matches the offline `osictl validate` hash — **determinism holds end-to-end**.
- REST query returns real Iceberg rows plus the exact SQL; repeat calls yield an identical request hash and a cache hit.
- Governance denial (analyst → PII column) returns **HTTP 403** at compile time.
- The BI view path and the REST path return **identical numbers**.

See the [retail example](../examples/starrocks/retail/README.md) to run and show this, the onboarding guide below to bring people on, and [ARCHITECTURE.md](ARCHITECTURE.md) for how it works.

---

## Onboarding by role

> **The LLM selects; the compiler writes.**

Three different people touch this system. Everyone chooses only *certified metrics
and dimensions*; a deterministic planner turns that choice into exactly one
governed SQL statement. Nobody hand-writes analytical SQL. Everything else hangs
on that idea.

### A. The metric author (data engineer)

Owns the `SemanticModel` CR — the only role that edits Apache Ossie YAML.

1. Start from the worked example: [`examples/starrocks/retail/model/semanticmodel.yaml`](../examples/starrocks/retail/model/semanticmodel.yaml).
2. Learn the authoring loop:
   ```bash
   osictl validate -f model.yaml        # offline, instant feedback — no cluster
   kubectl apply -f model.yaml          # or git push, and ArgoCD applies it
   kubectl -n semantic-system get semanticmodels -w   # Validated / Compiled / Published / DriftDetected
   ```
3. You maintain only **metrics, joins, and synonyms**; physical field lists come
   from Glue via `osictl derive -region <r> -database <db> -out model.yaml`
   (writes to stdout if `-out` is omitted). The generated file is a full,
   schema-valid Apache Ossie scaffold: datasets and fields populated from the
   catalog, with `metrics`, `relationships`, synonyms, `primary_key`, governance,
   and views left as clearly-marked `TODO` placeholders. Candidate joins are
   inferred from key-name conventions and emitted commented-out for a human to
   confirm; metrics and relationships are never auto-modified.
4. Golden rules: a metric is a *certified definition* (disagreements about
   "revenue" are resolved in the CR, once); drift is *detected*, not silently
   served; the `spec.osi` block round-trips byte-for-byte as portable Apache Ossie.

### B. The AI / app developer (consumer)

Never edits the model; consumes the planner.

- **Contract:** request `{metrics, dimensions, filters, identity}` → response
  `{columns, rows, sql, requestHash, ...}`.
- **Agents (MCP):** point the MCP client at `/mcp`. Tools are self-describing —
  `list_metrics`, `list_dimensions` (names, descriptions, `ai_context` synonyms so
  the model grounds user vocabulary), and `query_metric(...)` (returns data *and*
  the emitted SQL). The agent learns the model by calling these; you do not
  prompt-stuff the schema.
- **Apps (REST):** `POST /v1/models/{m}/query`, and `POST /v1/models/{m}/sql` for a
  dry-run that returns SQL without executing.
- Pass identity (`X-Semantic-Role` for REST); governance is enforced server-side —
  a forbidden field is a 403, not an empty result.

### C. The BI analyst (any BI tool)

Touches no YAML at all.

- Connect your BI tool to StarRocks over MySQL protocol (StarRocks speaks the
  MySQL wire protocol, so most BI tools connect natively).
- Use the governed `semantic_views.*` views (e.g. `sales_by_category_year`), **not**
  the raw Iceberg tables.
- The one rule: the view already computed the metric correctly (including
  fan-out-safe ratios). Building your own aggregate over raw tables is how you get
  the wrong number. The naive-vs-governed contrast is shown in
  [the retail example](../examples/starrocks/retail/README.md#5-with-vs-without-the-semantic-layer-the-money-shot).

### Suggested path

Everyone: read this doc → run the with/without demo in the
[retail example](../examples/starrocks/retail/README.md) → branch by role above.
Authors finish with `osictl validate` on a metric of their own; consumers with one
successful `query_metric`; analysts with one governed view in a BI chart.
