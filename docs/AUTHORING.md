# Authoring a semantic model

How to go from raw tables in your catalog to a certified `SemanticModel` — start
from a generated template, then layer in joins, metrics, and business meaning.

**Who does this:** a data / analytics engineer (the *metric author*). Consumers
(AI agents, apps, BI) never write this — they read it through MCP/REST/views.

**The shape of a model** (see [`examples/starrocks/retail/model/semanticmodel.yaml`](../examples/starrocks/retail/model/semanticmodel.yaml)
for a complete worked example):

```
spec:
  connection: { catalog, database }     # where the physical tables live
  osi:
    datasets:      [...]                 # logical tables + fields (mostly generated)
    relationships: [...]                 # the join graph (you confirm)
    metrics:       [...]                 # certified definitions (you write)
  governance: {...}                      # optional: roles, denyFields, rowFilters
  views:      [...]                      # optional: governed BI views
```

The mechanical parts (datasets, fields, candidate joins) are **generated**; you
supply **metrics, join confirmation, and business meaning**.

---

## Step 1 — Generate the physical scaffold

`osictl derive` reads your catalog (Glue today) and prints a starting CR:
datasets with one field per column, `is_time` flagged on date/timestamp columns,
and **candidate relationships** inferred from key-name conventions (commented out
for you to confirm). It needs AWS credentials + Glue read access; no cluster.

```bash
# writes to stdout — redirect to a file
go run ./cmd/osictl derive -region us-west-2 -database <your_glue_db> > model.yaml
```

Flags: `-region` (or `$AWS_REGION`), `-database` (required), `-catalog` (the
StarRocks external-catalog name, default `iceberg`), `-model` / `-name` /
`-namespace` (identifiers). Output ends with the inferred candidate joins and a
reminder of what to add next.

> No Glue? You can hand-write `datasets` instead — `derive` is a convenience, not
> a requirement. A catalog-agnostic `information_schema` source is on the
> [roadmap](ROADMAP.md).

## Step 2 — Confirm the join graph

`derive` prints candidate relationships commented out, e.g.:

```yaml
#  - name: store_sales_to_item
#    from: store_sales
#    to: item
#    from_columns: [ss_item_sk]
#    to_columns: [i_item_sk]
```

Move the correct ones into `spec.osi.relationships` (uncommented). Each edge is
**many → one** (`from` is the fact/child, `to` is the dimension/parent). Add
`join_type: LEFT` where you need it (default is `INNER`). The join graph must be
a **tree** (star/snowflake) — cycles are a validation error.

## Step 3 — Define metrics (the certified part)

This is the value: the definitions the compiler will always use, so nobody
re-derives them wrongly. Each metric carries a per-dialect expression:

```yaml
metrics:
  - name: total_sales
    expression: { dialects: [{ dialect: ANSI_SQL, expression: SUM(store_sales.ss_ext_sales_price) }] }
    description: Total sales revenue (sum of extended sales price)
  - name: store_productivity          # fan-out-safe ratio across a join
    expression:
      dialects: [{ dialect: ANSI_SQL,
        expression: "SUM(store_sales.ss_ext_sales_price) / NULLIF(SUM(store.s_number_employees), 0)" }]
```

The metric grammar is intentionally small (checked by `osictl validate`):

- Aggregations: `SUM`, `COUNT`, `COUNT(DISTINCT ...)`, `AVG`, `MIN`, `MAX` over a
  `dataset.field` reference (every column **must** be qualified as `dataset.field`).
- Ratios: `<agg> / <agg>`, optionally `NULLIF(denominator, 0)` (the emitter wraps
  the denominator in `NULLIF` anyway).
- **Fan-out safety:** if a ratio's denominator aggregates a *dimension* across a
  join (like `SUM(store.s_number_employees)` over the sales fact), that dataset
  needs a `primary_key` so the planner can deduplicate before aggregating.
  `validate` tells you when it's missing. This is the class of query raw
  text-to-SQL gets wrong; the planner handles it by construction.

## Step 4 — Add business meaning (`ai_context` + `description`)

This is what lets an agent map a user's words to the right metric. Put a
`description` and `ai_context.synonyms` on datasets, fields, and metrics:

```yaml
  - name: total_sales
    expression: {...}
    description: Total sales revenue
    ai_context:
      synonyms: ["revenue", "gross sales", "sales amount", "total revenue"]
```

`list_metrics` / `list_dimensions` return these to the agent, so "what's our
revenue" grounds onto `total_sales` deterministically — you are not prompt-stuffing
schema. (Future: import descriptions, glossary terms, and PII tags straight from
OpenMetadata/DataHub — see the [roadmap](ROADMAP.md).)

## Step 5 — Governance (optional)

Row/column/metric policies, enforced at compile time (an unauthorized request
fails to compile — it never returns filtered rows):

```yaml
governance:
  defaultRole: analyst
  roles:
    - name: analyst
      allowMetrics: ["*"]
      denyFields: ["customer.c_email_address"]     # PII — 403 if requested
      rowFilters: [{ dataset: store, predicate: "s_state = 'TX'" }]
    - name: admin
      allowMetrics: ["*"]
```

## Step 6 — Governed BI views (optional)

Materialize certified metrics as StarRocks views any MySQL-protocol BI tool can
read:

```yaml
views:
  - name: sales_by_category_year
    metrics: [total_sales, total_profit]
    dimensions: [item.i_category, date_dim.d_year]
    role: admin
```

## Step 7 — Validate and deploy

```bash
go run ./cmd/osictl validate -f model.yaml        # offline, instant, no cluster
kubectl apply -f model.yaml                        # or git push → ArgoCD
kubectl -n semantic-system get semanticmodels -w   # Validated / Published / Drift=False
```

`validate` runs the full structural + planner-subset checks and prints the
content-addressed model version. Once `Published` with `Drift=False`, the server
serves it within seconds.

## Keeping it current

You maintain only **metrics, joins, and synonyms**. When the physical schema
changes, re-run `derive` (or the controller's catalog sync) to regenerate field
lists — existing fields, metrics, and relationships are never overwritten. A
missing bound column surfaces as `DriftDetected` while the last-good compiled
artifact keeps serving, so a schema change never silently breaks queries.

---

Roles and the bigger picture: [OVERVIEW.md](OVERVIEW.md). How compilation and
governance work under the hood: [ARCHITECTURE.md](ARCHITECTURE.md).
