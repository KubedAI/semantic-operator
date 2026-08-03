---
title: Retail on Glue and Trino
description: The same retail model served by Trino instead of StarRocks, which is the clearest demonstration that the semantics are portable.
---

This walkthrough runs the retail semantic model with Glue, Iceberg, and Trino.
It demonstrates that the metric definitions and governance rules remain the
same when the query engine changes.

An AI agent selects certified metrics and dimensions. Semantic Operator applies
governance and uses its Go planner to generate Trino SQL. No LLM is involved in
SQL generation.

Install `kubectl`, `helm`, `aws`, `jq`, `git`, and Go 1.26 on your workstation.

## Stage 1. Deploy the cluster

Clone the
[`semantic-on-eks`](https://github.com/awslabs/data-on-eks/tree/main/data-stacks/semantic-on-eks)
stack from Data on EKS. Trino is enabled by default in this stack.

Deploy the stack:

```bash
./deploy.sh
```

Verify that the Trino coordinator and worker pods are running:

```bash
kubectl -n trino get pods
```

## What changes with Trino

**The engine setting.** `engine.type=trino` selects both the SQL dialect and the connection
client.

**The port.** Trino speaks HTTP on 8080 rather than the MySQL protocol on 9030.

**Where views live.** Trino has no default catalog, so governed views need a catalog
qualified schema such as `iceberg.semantic_views`. On StarRocks a bare `semantic_views` is
enough.

Trino SQL uses double-quoted identifiers and `IS NOT DISTINCT FROM` for
null-safe equality. The planner handles these dialect differences.

## Stage 2. Load the demo data

```bash
bash examples/stacks/eks/glue-trino/data-load.sh
```

Builds the five retail tables in a Glue-backed Iceberg schema with CTAS from
Trino's built-in `tpcds` connector. Nothing outside the cluster is read, so
this needs no warehouse of your own and no extra S3 grant. Trino writes the
Parquet and Iceberg metadata to the warehouse its `iceberg` catalog is already
configured with, and Glue tracks the tables.

Set `GLUE_SCHEMA` if `osi_demo` is taken in your Glue catalog.

<details>
<summary>Expected output</summary>

```
[data-load] creating schema iceberg.osi_demo
CREATE SCHEMA
[data-load] loading date_dim (2000-2002)
CREATE TABLE: 1096 rows
[data-load] loading item
CREATE TABLE: 18000 rows
[data-load] loading customer
CREATE TABLE: 100000 rows
[data-load] loading store
CREATE TABLE: 12 rows
[data-load] loading store_sales (the big one, a few minutes)
CREATE TABLE: 1591154 rows
[data-load] spreading stores with sales across two states
UPDATE: 12 rows
[data-load]   TX stores: 2,7,10
UPDATE: 3 rows
[data-load] verify: row counts
"customer","100000"
"date_dim","1096"
"item","18000"
"store","12"
"store_sales","1591154"
[data-load] verify: stores by state, with sales
"TN","9","796806"
"TX","3","794348"
```

On a re-run the `CREATE TABLE` lines report `0 rows`, which is the
`IF NOT EXISTS` idempotency, not a failure. Both states must show a non-zero
sales count or the row-filter check below returns nothing.

</details>

## Stage 3. Install Semantic Operator

The chart deploys the manager and server images. The manager validates and
publishes models. The server loads those models and serves REST and MCP
requests.

```bash
kubectl create namespace semantic-system --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install semantic-operator charts/semantic-operator \
  --namespace semantic-system --create-namespace \
  --set server.auth.allowInsecureHeaderAuth=true \
  --set engine.type=trino \
  --set engine.host=trino.trino.svc.cluster.local
```

`engine.port` is not set because the Trino client defaults to 8080.

**Verify.**

```bash
pkill -f 'port-forward.*8090:8090' 2>/dev/null
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &
sleep 3
curl -s -o /dev/null -w '%{http_code}\n' localhost:8090/readyz
```

Expect `200`. If it reports the engine as unreachable, the message names the engine, which
tells you the dialect selection worked and the connection did not.

## Stage 4. Deploy the model

This example ships a certified model configured for Trino. Its `viewDatabase`
is catalog qualified, which is the structural difference from the StarRocks
model.

```yaml
spec:
  connection:
    catalog: iceberg
    database: osi_demo
    viewDatabase: iceberg.semantic_views   # Trino: views live inside a catalog
```

```bash
kubectl apply -f examples/stacks/eks/glue-trino/semanticmodel.yaml
kubectl -n semantic-system get semanticmodels -w
```

**Verify.** `VALIDATED=True PUBLISHED=True DRIFT=False`, exactly as on StarRocks.

## Stage 5. Query sales per employee by state

Ask the business question:

> **How much sales revenue is generated per employee in each state?**

The AI agent maps this question to the certified `store_productivity` metric
and the `store.s_state` dimension.

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' | jq -c '.rows'
```

<details>
<summary>Expected output</summary>

```json
[["TN","644567.241309"],["TX","1826350.375719"]]
```

These are the governed results for the two-state dataset loaded in Stage 2.

</details>

Now look at the SQL.

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/sql \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' | jq -r '.plan.sql'
```

<details>
<summary>Expected SQL</summary>

```sql
/* semantic-layer model=tpcds_retail_model version=... request=... */
WITH "m_store_productivity_num" AS (
  SELECT "store"."s_state" AS "d0",
         SUM("store_sales"."ss_ext_sales_price") AS "val"
  FROM "iceberg"."osi_demo"."store_sales" AS "store_sales"
  INNER JOIN "iceberg"."osi_demo"."store" AS "store"
          ON "store_sales"."ss_store_sk" = "store"."s_store_sk"
  GROUP BY 1
),
"m_store_productivity_den" AS (
  SELECT "store"."s_state" AS "d0",
         SUM("store"."s_number_employees") AS "val"
  FROM "iceberg"."osi_demo"."store" AS "store"
  GROUP BY 1
)
SELECT "m_store_productivity_num"."d0" AS "store.s_state",
       "m_store_productivity_num"."val" / NULLIF("m_store_productivity_den"."val", 0)
         AS "store_productivity"
FROM "m_store_productivity_num"
INNER JOIN "m_store_productivity_den"
        ON "m_store_productivity_num"."d0" IS NOT DISTINCT FROM "m_store_productivity_den"."d0"
ORDER BY 1
LIMIT 1000
```

Double quoted identifiers throughout and `IS NOT DISTINCT FROM` joining the two CTEs. Not
a backtick anywhere. StarRocks gets backticks and `<=>` for the same model.

The ratio is split into two CTEs on purpose. Summing headcount across the sales
join would multiply it by the number of sales rows, so the denominator is
aggregated separately over `store` alone.

</details>

## Stage 6. Test governance and views

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["customer.c_email_address"]}' | jq -r '.error'

curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: tx_analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["store.s_state"]}' | jq -c '.rows'

kubectl -n trino exec deploy/trino-coordinator -c trino-coordinator -- \
  trino --execute "SHOW TABLES FROM iceberg.semantic_views" 2>/dev/null
```

<details>
<summary>Expected output</summary>

```
unauthorized: role "analyst" may not read field "customer.c_email_address"

[["TX","1510391760.72"]]

"clv_by_year"
"monthly_sales"
"sales_by_brand_year"
"sales_by_category_year"
"store_productivity_by_state"
```

The 403 arrives before any SQL is built. The `tx_analyst` row filter is
compiled into the statement rather than applied afterwards. The five views are
plain Iceberg views in Glue that any client can read without going through the
server.

</details>

## Stage 7. Inspect queries in Trino

Trino ships a web interface, which makes a good demo. Forward it and open it in a browser.

```bash
kubectl -n trino port-forward svc/trino 8080:8080
```

Run a governed query and refresh. Each statement appears with the semantic layer comment as
its identifier, carrying the model version and request hash.

## Clean up

```bash
kubectl delete -f examples/stacks/eks/glue-trino/semanticmodel.yaml
helm uninstall semantic-operator -n semantic-system
kubectl delete namespace semantic-system
```

From the `data-stacks/semantic-on-eks` directory, delete the EKS stack and its
AWS resources:

```bash
./cleanup.sh
```
