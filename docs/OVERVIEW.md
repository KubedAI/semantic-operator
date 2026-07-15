# Overview: what this is and why it exists

## One line

A Kubernetes operator that turns certified business metrics into **deterministic, governed** SQL — so AI agents, BI tools, and custom apps all answer the same question the same correct way.

## The blurb

**osi-semantic-operator** is a semantic layer that runs as a Kubernetes operator on EKS, implementing the **Apache Ossie (incubating)** standard — the vendor-neutral semantic-model spec formerly known as Open Semantic Interchange (OSI), which entered the Apache Incubator in July 2026.

You define metrics, dimensions, and joins once as an Apache Ossie `SemanticModel` custom resource under GitOps. The operator validates it against your live StarRocks/Iceberg schema, compiles it, and serves it three ways from a single planner:

- an **MCP server** for LLM agents,
- a **REST API** for apps, and
- **governed SQL views** for BI tools like Apache Superset.

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

See [DEMO.md](DEMO.md) to show this, [TEACHING.md](TEACHING.md) to onboard users, and [ARCHITECTURE.md](ARCHITECTURE.md) for how it works.
