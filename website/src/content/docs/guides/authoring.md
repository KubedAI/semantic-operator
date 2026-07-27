---
title: "Authoring a model"
description: "Go from raw tables to a certified SemanticModel, starting from a generated scaffold."
---
How to go from raw tables in your catalog to a certified `SemanticModel`. Start from a generated template, then add joins, metrics, and business meaning.

**Who does this.** A data or analytics engineer, the metric author. Consumers (AI agents, apps, BI) never write this. They read it through MCP, REST, and views.

**The shape of a model.** See [`examples/retail/model/semanticmodel.yaml`](https://github.com/KubedAI/semantic-operator/blob/main/examples/retail/model/semanticmodel.yaml) for a complete worked example.

```
spec:
  connection: { catalog, database }     # where the physical tables live
  ossie:
    datasets:      [...]                 # logical tables and fields (mostly generated)
    relationships: [...]                 # the join graph (you confirm)
    metrics:       [...]                 # certified definitions (you write)
  governance: {...}                      # optional: roles, denyFields, rowFilters
  views:      [...]                      # optional: governed BI views
```

The mechanical parts (datasets, fields, candidate joins) are generated. You supply metrics, join confirmation, and business meaning.

---

## Step 1. Generate the scaffold

`ossiectl derive` reads your catalog and writes a full, schema-valid `SemanticModel`. Datasets and fields come from the catalog, with `is_time` flagged on date and timestamp columns. `metrics`, `relationships`, `primary_key`, synonyms, governance, and views are left as clearly marked `TODO` placeholders, with a worked metric example. Candidate joins are inferred from key-name conventions and left commented out for you to confirm.

Two sources ship. `-source glue` (the default) reads AWS Glue directly over the AWS SDK. It needs AWS credentials and Glue read access, and no cluster. `-source engine` reads the live query engine's `information_schema` through the same connection the operator uses (`ENGINE_*` env, `-engine trino` or `-engine starrocks`), which makes derivation work for **any catalog the engine mounts**: Glue, an Iceberg REST catalog such as Apache Polaris or Nessie, Hive Metastore, or Unity. Both sources produce identical dataset and field structure for the same tables.

```bash
go run ./cmd/ossiectl derive -region us-west-2 -database <your_glue_db> -out model.yaml
# writes to stdout if -out is omitted
```

Flags: `-region` (or `$AWS_REGION`), `-database` (required), `-catalog` (the StarRocks external-catalog name, default `iceberg`), and `-model`, `-name`, `-namespace` for identifiers.

The generated file passes `ossiectl validate` as is, because empty metrics and relationships are legal. It grows richer as you fill in the placeholders.

> No Glue? Use `-source engine` against your running engine, or hand-write `datasets`. `derive` is a convenience, not a requirement.

### Import business meaning from a metadata catalog

`-source` fills in physical structure. `-enrich` adds the meaning a steward
already curated elsewhere, so you write less by hand:

```bash
export DATAHUB_TOKEN=<personal access token>
go run ./cmd/ossiectl derive \
  -source engine -catalog polaris -database osi_demo \
  -enrich datahub -datahub-url http://localhost:8080 \
  -datahub-platform trino -datahub-dataset-prefix polaris \
  -out model.yaml
```

What it imports, and what it deliberately does not:

| DataHub | Becomes | Note |
|---|---|---|
| dataset and column descriptions | `description` | steward edits win over ingested text |
| glossary terms | `ai_context.synonyms` | how an agent grounds "revenue" onto a metric |
| PII / sensitivity tags | `governance.denyFields` | a request for the field then fails to compile |
| deprecation | a `REVIEW` comment | flagged for a human, never silently dropped |
|. | **metrics** | never imported. Certifying a formula stays a human decision |

Physical truth still comes from `-source`, because a metadata platform serves an
*ingested copy* of the schema that can lag reality. Enrichment is additive and
best effort: if DataHub is unreachable the command reports it, and a scaffold
derived without enrichment is still valid. The URN path depends on the
ingestion source. DataHub's `trino` source names datasets
`<catalog>.<schema>.<table>`, hence `-datahub-dataset-prefix`. The `iceberg` and
`hive` sources need no prefix.

## Step 2. Confirm the join graph

`derive` prints candidate relationships commented out, for example:

```yaml
#  - name: store_sales_to_item
#    from: store_sales
#    to: item
#    from_columns: [ss_item_sk]
#    to_columns: [i_item_sk]
```

Move the correct ones into `spec.ossie.relationships` and uncomment them. Each edge is many-to-one. `from` is the fact or child, `to` is the dimension or parent. The join graph must be a tree (star or snowflake). Cycles are a validation error.

Relationships carry no join type, so the `spec.ossie` block stays pure Ossie. Joins default to `INNER`. To make one a `LEFT` join, add a `spec.joins` override (an operator extension, sibling to `governance` and `views`):

```yaml
joins:
  - relationship: store_sales_to_customer   # a relationship name
    type: LEFT
```

## Step 3. Define metrics (the certified part)

This is the value. These are the definitions the compiler will always use, so nobody re-derives them wrongly. Each metric carries a per-dialect expression.

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

The metric grammar is small, and `ossiectl validate` checks it.

- Aggregations: `SUM`, `COUNT`, `COUNT(DISTINCT..)`, `AVG`, `MIN`, `MAX` over a `dataset.field` reference. Every column must be qualified as `dataset.field`.
- Ratios: `<agg> / <agg>`, optionally with `NULLIF(denominator, 0)`. The emitter wraps the denominator in `NULLIF` anyway.
- Fan-out safety. If a ratio's denominator aggregates a dimension across a join, like `SUM(store.s_number_employees)` over the sales fact, that dataset needs a `primary_key` so the planner can deduplicate before aggregating. `validate` tells you when it is missing. This is the class of query raw text-to-SQL gets wrong. The planner handles it by construction.

## Step 4. Add business meaning (`ai_context` and `description`)

This is what lets an agent map a user's words to the right metric. Put a `description` and `ai_context.synonyms` on datasets, fields, and metrics.

```yaml
  - name: total_sales
    expression: {...}
    description: Total sales revenue
    ai_context:
      synonyms: ["revenue", "gross sales", "sales amount", "total revenue"]
```

`list_metrics` and `list_dimensions` return these to the agent, so "what is our revenue" grounds onto `total_sales`. You are not prompt-stuffing the schema. If your organization already curates this in DataHub, import it instead of retyping it: see [Import business meaning from a metadata catalog](#import-business-meaning-from-a-metadata-catalog) above. OpenMetadata is on the roadmap.

## Step 5. Governance (optional)

Row, column, and metric policies, enforced at compile time. An unauthorized request fails to compile. It never returns filtered rows.

```yaml
governance:
  defaultRole: analyst
  roles:
    - name: analyst
      allowMetrics: ["*"]
      denyFields: ["customer.c_email_address"]     # PII, returns 403 if requested
      rowFilters: [{ dataset: store, predicate: "s_state = 'TX'" }]
    - name: admin
      allowMetrics: ["*"]
```

## Step 6. Governed BI views (optional)

Materialize certified metrics as StarRocks views that any MySQL-protocol BI tool can read.

```yaml
views:
  - name: sales_by_category_year
    metrics: [total_sales, total_profit]
    dimensions: [item.i_category, date_dim.d_year]
    role: admin
```

## Step 7. Validate and deploy

```bash
go run ./cmd/ossiectl validate -f model.yaml        # offline, instant, no cluster
kubectl apply -f model.yaml                        # or git push, then ArgoCD
kubectl -n semantic-system get semanticmodels -w   # Validated, Published, Drift=False
```

`validate` runs the full structural and planner-subset checks and prints the content-addressed model version. Once the model is Published with Drift=False, the server serves it within seconds.

## Keeping it current

You maintain only metrics, joins, and synonyms. When the physical schema changes, re-run `derive` (or the controller's catalog sync) to regenerate field lists. Existing fields, metrics, and relationships are never overwritten. A missing bound column shows up as `DriftDetected` while the last good compiled artifact keeps serving, so a schema change never silently breaks queries.

---

Roles and the bigger picture: [OVERVIEW.md](/start/introduction). How compilation and governance work under the hood: [ARCHITECTURE.md](/architecture).

