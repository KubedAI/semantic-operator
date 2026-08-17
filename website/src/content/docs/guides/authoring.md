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

Two sources ship. Prefer `-source engine` when an engine is available. It reads the live
query engine's `information_schema` through an `ENGINE_*` connection. Select Trino or
StarRocks with `-engine`. This works for **any catalog the engine mounts**: Glue, an
Iceberg REST catalog such as Apache Polaris or Nessie, Hive Metastore, or Unity. It also
sees the schema and permissions that queries will use.

```bash
go run ./cmd/ossiectl derive \
  -source engine -engine trino \
  -catalog <engine_catalog> -database <schema> \
  -out model.yaml
# writes to stdout if -out is omitted
```

`-source glue` is the offline bootstrap option. It reads AWS Glue directly over the AWS
SDK and needs AWS credentials with Glue read access. It does not need a running query
engine or Kubernetes cluster.

```bash
go run ./cmd/ossiectl derive \
  -source glue -region us-west-2 \
  -database <your_glue_db> -catalog <future_engine_catalog> \
  -out model.yaml
```

The command currently defaults to `-source glue` for compatibility, so always pass
`-source` explicitly. Both sources produce identical dataset and field structure for the
same tables. Neither source is used at runtime.

Common flags are `-database` (required), `-catalog` (the catalog name as mounted in the
engine), and `-model`, `-name`, and `-namespace` for identifiers. `-region` or
`$AWS_REGION` applies only to the Glue source.

The generated file passes `ossiectl validate` as is, because empty metrics and relationships are legal. It grows richer as you fill in the placeholders.

> `derive` is a convenience, not a requirement. Use `-source engine` for the normal
> connected workflow, `-source glue` to bootstrap without an engine, or hand-write
> `datasets` yourself.

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
    - name: regional_analyst
      allowMetrics: ["*"]
      # {{claim.region}} is filled from the caller's token, so one role covers
      # every region. A caller whose token lacks the claim is refused.
      rowFilters: [{ dataset: store, predicate: "s_state = {{claim.region}}" }]
    - name: admin
      allowMetrics: ["*"]
```

Two things about roles are worth knowing before you write these.

A role with an empty `allowMetrics` grants nothing and is ignored entirely, so it cannot be
used to hold row filters or field denials on its own.

Roles are never combined. A caller holding several roles must have one role that permits
their whole request, every metric and every field together. Write each role so it is
usable on its own rather than expecting two of them to add up.

### Add an external decision gate

OPA or Ranger can add a second authorization decision to a model. Built-in roles remain
mandatory. Both the built-in policy and the external provider must allow the request.

```yaml
governance:
  defaultRole: analyst
  roles:
    - name: analyst
      allowMetrics: ["*"]
  external:
    providerRef: corporate-opa
```

The model stores only a logical provider name. The server administrator owns the provider
type, URL, credentials, resource mapping, and other native settings. A provider with a
bearer token must use HTTPS. A local sidecar can use HTTP when no credential is configured.

```yaml
server:
  authorization:
    providers:
      - name: corporate-opa
        type: opa
        url: https://opa.semantic-system.svc.cluster.local:8181
        timeoutSeconds: 2
        bearerTokenSecret:
          name: opa-client
          key: token
        opa:
          decisionPath: semantic/query/allow
```

Ranger uses the dedicated PDP, not Ranger Admin. Its URL is the complete API base. Standard
PDP deployments use a base ending in `/authz/v1`. The semantic server authenticates with a
service credential and submits the end user's identity, so Ranger must list the server
principal as a delegation user for `serviceName`.

```yaml
server:
  authorization:
    providers:
      - name: corporate-ranger
        type: ranger
        url: https://ranger-pdp.semantic-system.svc.cluster.local:6500/authz/v1
        timeoutSeconds: 2
        bearerTokenSecret:
          name: ranger-service-token
          key: token
        ranger:
          authenticationMode: service
          serviceType: semantic-operator
          serviceName: semantic-prod
          resource: "semantic-model:namespace={namespace},model={resource}"
          permission: query
          contextAttributes:
            environment: production
            clusterName: analytics-prod
```

Static context attributes are administrator-defined strings sent with every decision. They
must not contain secrets. `clientType`, `requestData`, and the `semantic.*` namespace are
managed by the server and cannot be overridden.

Helm writes this provider list as strict YAML in an Opaque Secret. The server mounts
`providers.yaml` read-only and loads it through `AUTHORIZATION_PROVIDERS_FILE`. Bearer-token
values remain in their separately referenced Secrets. They are not copied into the provider
configuration Secret.

A deployment outside this chart can instead set `AUTHORIZATION_PROVIDERS` to inline YAML or
set `AUTHORIZATION_PROVIDERS_FILE` to a YAML file path. The two variables are mutually
exclusive. Any `bearerTokenEnv` must use the dedicated
`AUTHORIZATION_PROVIDER_TOKEN_*` namespace, so provider configuration cannot reference engine,
cache, or other process secrets. Configuration is loaded once during startup, so a changed
file requires a server rollout.

Every provider receives the versioned action, exact model identity, semantic request,
principal, separate groups and roles, configured JWT claims, current access time, and adapter.
It receives no SQL or raw metric expressions. OPA evaluates this input at its configured Data
API path and may return a boolean or `{"allow": true, "revision": "bundle-42"}`. A missing
result denies.

The Ranger adapter sends the configured permission to `/authorize`. It also sends copied
claims as subject attributes, the semantic request as `requestData`, static context
attributes, and model provenance. Only a correlated, obligation-free `ALLOW` succeeds.
`DENY`, `NOT_DETERMINED`, and `PARTIAL` deny. Timeouts, unavailable providers, malformed or
inconsistent results, row filters, masks, and other unsupported obligations fail closed with
503. An explicit policy denial returns 403.

This first integration is an allow or deny gate for query and SQL-plan requests. Discovery
and engine-native views continue to use the built-in policy only.

## Step 6. Governed BI views (optional)

Materialize certified metrics as engine-native views that BI tools can read through StarRocks
or Trino.

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

`validate` runs structural and planner checks and prints the content-addressed model
version. It makes no network calls and does not prove that the physical tables are
available. After deployment, the operator asks the live engine to bind every referenced
table and column. Once the model is Published with Drift=False, the server serves it
within seconds.

## Keeping it current

The current `derive` command writes a new scaffold. It does not merge that scaffold into an
existing model. When the physical schema changes, derive a fresh file and review its
dataset and field differences before copying the intended changes into the certified
model. Keep metrics, relationships, governance, and other authored decisions under version
control.

A missing bound column shows up as `DriftDetected` while the last good compiled artifact
keeps serving, so a schema change never silently breaks queries.

---

Roles and the bigger picture: [OVERVIEW.md](/start/introduction). How compilation and governance work under the hood: [ARCHITECTURE.md](/architecture).

