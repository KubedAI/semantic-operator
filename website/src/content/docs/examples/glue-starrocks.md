---
title: Retail on Glue and StarRocks
description: The reference walkthrough. Install the operator, load demo data, generate and certify a model, deploy it, and prove that governance and determinism hold.
---

This is the reference walkthrough for running Semantic Operator with Glue,
Iceberg, and StarRocks. You will load retail data, deploy a certified semantic
model, and query it through the semantic server.

An AI agent selects certified metrics and dimensions instead of generating
SQL. Semantic Operator validates the request, applies governance, and uses its
Go planner to generate SQL for StarRocks. No LLM is involved in SQL generation.

Install `kubectl`, `helm`, `aws`, `jq`, `git`, `mysql`, and Go 1.26 on your
workstation.

## Stage 1. Deploy the cluster

Clone the
[`semantic-on-eks`](https://github.com/awslabs/data-on-eks/tree/main/data-stacks/semantic-on-eks)
stack from Data on EKS. In `terraform/data-stack.tfvars`, enable StarRocks:

```hcl
enable_starrocks = true
```

Deploy the stack:

```bash
./deploy.sh
```

Verify that the StarRocks front end and back end pods are running:

```bash
kubectl -n starrocks get pods
```

## Stage 2. Create the Iceberg catalog

StarRocks reads Iceberg tables through an external catalog. This is the one manual data
step and you do it once.

Connect to the StarRocks front end.

```bash
kubectl -n starrocks port-forward svc/kube-starrocks-fe-service 9030:9030 &
mysql -h 127.0.0.1 -P 9030 -u root
```

Create the catalog.

```sql
CREATE EXTERNAL CATALOG iceberg
PROPERTIES (
  "type" = "iceberg",
  "iceberg.catalog.type" = "glue",
  "aws.glue.use_aws_sdk_default_behavior" = "true",
  "aws.glue.region" = "us-west-2",
  "aws.s3.use_aws_sdk_default_behavior" = "true",
  "aws.s3.region" = "us-west-2"
);
```

Use `use_aws_sdk_default_behavior` rather than `use_instance_profile`. The default chain
covers IRSA and pod identity, which is how a pod on EKS actually gets credentials. The
instance profile setting talks only to the node metadata service, which pods usually cannot
reach.

**Verify.** The catalog should be listed and queryable.

```sql
SHOW CATALOGS;
SHOW DATABASES FROM iceberg;
```

If this fails with a Glue permission error, the StarRocks pods need an IAM role with Glue
and S3 access on the warehouse bucket.

## Stage 3. Load the demo data

The loader creates the tables through StarRocks itself, so there is no Spark
job. It is idempotent and skips tables that already have rows.

:::caution[The database needs a location first]
A Glue database created without a location cannot hold tables. StarRocks
reports `Failed to find location in database`. Create it with an explicit S3
prefix before running the loader.

```sql
CREATE DATABASE iceberg.osi_demo
PROPERTIES ('location' = 's3://<your-warehouse-bucket>/osi_demo/');
```

The bucket must be one the StarRocks pods' IAM role can write to.
:::

```bash
kubectl -n starrocks port-forward svc/kube-starrocks-fe-service 9030:9030 &
export STARROCKS_HOST=127.0.0.1
make demo-data
```

**Verify.** Query the row count through the catalog.

```sql
SELECT count(*) FROM iceberg.osi_demo.store_sales;
```

:::note[If StarRocks cannot see a database you know exists]
The external catalog caches Glue metadata, so a database created or dropped
outside StarRocks can take a moment to appear. Re-connecting or recreating the
catalog refreshes it. Checking Glue directly with
`aws glue get-database --name <db>` tells you which side is stale.
:::

## Stage 4. Install Semantic Operator

The chart deploys two images. The manager validates and publishes semantic
models. The stateless server loads those models and serves REST and MCP
requests. Both images are built from this repository and share the planner,
governance, and engine integrations.

```bash
helm upgrade --install semantic-operator charts/semantic-operator \
  --set server.auth.allowInsecureHeaderAuth=true \
  --namespace semantic-system --create-namespace \
  --set engine.type=starrocks \
  --set engine.host=kube-starrocks-fe-service.starrocks.svc.cluster.local
```

**Verify.** Three pods running, and the server reporting ready. Readiness means the model
store has synced and the engine answered a ping.

```bash
kubectl -n semantic-system get pods
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &
curl -s -o /dev/null -w '%{http_code}\n' localhost:8090/readyz
```

Expect `200`.

## Stage 5. Derive the model

You could write the model by hand. It is faster to generate the mechanical part.

```bash
export SQL_DIALECT=starrocks ENGINE_HOST=127.0.0.1 ENGINE_PORT=9030
mkdir -p tmp
go run ./cmd/ossiectl derive -source engine \
  -catalog iceberg -database osi_demo \
  -model tpcds_retail_model -name tpcds-retail \
  -out tmp/scaffold.yaml
```

**Verify.** Look at what it produced and what it left for you.

```bash
grep -c '^          - name:' tmp/scaffold.yaml   # fields the machine filled in
grep -c 'TODO' tmp/scaffold.yaml          # decisions left for a person
go run ./cmd/ossiectl validate -f tmp/scaffold.yaml
```

The scaffold is valid because a model with no metrics is legal. It cannot
answer metric requests until a person completes the business definitions.

This is the honest split. The machine wrote every dataset and column. A person still has to
certify what revenue means and who may read what.

## Stage 6. Deploy the certified model

The repository contains the completed version with seven metrics, governance
roles, and five governed views.

```bash
kubectl apply -f examples/retail/model/semanticmodel.yaml
kubectl -n semantic-system get semanticmodels -w
```

**Verify.** Within about thirty seconds all conditions should be satisfied.

```bash
kubectl -n semantic-system get semanticmodel tpcds-retail
```

Expect `VALIDATED=True PUBLISHED=True DRIFT=False`.

Look at what the operator worked out.

```bash
kubectl -n semantic-system get semanticmodel tpcds-retail \
  -o jsonpath='{range .status.bindings[*]}{.dataset}{" -> "}{.table}{"\n"}{end}'
```

Each dataset should map to a physical table in `iceberg.osi_demo`.

## Stage 7. Compare direct SQL with a semantic query

Ask the same business question through both paths:

> **How much sales revenue is generated per employee in each state?**

Without a semantic model, a reasonable query joins sales to stores and sums
employee headcount across the joined rows:

```sql
SELECT s.s_state,
       SUM(ss.ss_ext_sales_price) / SUM(s.s_number_employees) AS naive
FROM iceberg.osi_demo.store_sales ss
JOIN iceberg.osi_demo.store s ON ss.ss_store_sk = s.s_store_sk
GROUP BY s.s_state;
```

The SQL is valid, but it counts each store's employees once for every sale. For
New York it returns about `12.54`.

With Semantic Operator, the AI agent maps the question to the certified
`store_productivity` metric and the `store.s_state` dimension:

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: demo-user' -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' | jq
```

The semantic query returns six rows, one per state. New York returns
`210176.60448413`. The Go planner calculates revenue and employee headcount
separately, so each store's employees are counted once.

<details>
<summary>Generated StarRocks SQL</summary>

```sql
WITH `m_store_productivity_num` AS (
  SELECT `store`.`s_state` AS `d0`,
         SUM(`store_sales`.`ss_ext_sales_price`) AS `val`
  FROM `iceberg`.`osi_demo`.`store_sales` AS `store_sales`
  INNER JOIN `iceberg`.`osi_demo`.`store` AS `store`
          ON `store_sales`.`ss_store_sk` = `store`.`s_store_sk`
  GROUP BY 1
), ...
INNER JOIN `m_store_productivity_den`
        ON `m_store_productivity_num`.`d0` <=> `m_store_productivity_den`.`d0`
```

StarRocks uses backticks for identifiers and `<=>` for null-safe equality.

</details>

## Stage 8. Verify deterministic planning

Run exactly the same request again.

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: demo-user' -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' \
  | jq '{requestHash, cachedResult}'
```

**Verify.** The same `requestHash` as before. `cachedResult` is `true` only when the
install configured `valkey.addr`. Without Valkey, it remains `false` and the identical
hash still proves deterministic planning.

To see the SQL without running it, use the dry run endpoint.

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/sql \
  -H 'X-Semantic-User: demo-user' -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["item.i_category"]}' | jq -r '.plan.sql'
```

Note the leading comment carrying the model name, version, and request hash. That is what
lets you trace a query in the StarRocks log back to the request that caused it.

## Stage 9. Test governance policies

An analyst asks for an email address.

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: demo-user' -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["customer.c_email_address"]}' | jq
```

**Verify.** A 403 with a message naming the field. No SQL was generated and nothing reached
the database, because the policy was checked while the statement was still being built.

Now a role that is restricted to one state.

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: demo-user' -H 'X-Semantic-Role: tx_analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["store.s_state"]}' | jq '.rows'
```

**Verify.** One row, Texas only. The row filter was compiled into the WHERE clause rather
than applied to the results afterwards.

## Stage 10. Query a governed view

The operator created governed views in StarRocks. Any SQL client can read them, with no
semantic server involved.

```sql
SELECT * FROM semantic_views.store_productivity_by_state ORDER BY 1;
```

**Verify.** The view returns the same numbers as Stage 7.

## Clean up

Delete the model first. Its finalizer drops the governed views it created.

```bash
kubectl delete -f examples/retail/model/semanticmodel.yaml
helm uninstall semantic-operator -n semantic-system
kubectl delete namespace semantic-system
```

From the `data-stacks/semantic-on-eks` directory, delete the EKS stack and its
AWS resources:

```bash
./cleanup.sh
```

## Troubleshooting

**The model sits at `DRIFT=True`.** Read the message with `kubectl describe`. It names the
dataset and the missing table or column. The usual cause is the external catalog not being
readable, which StarRocks reports as a table resolution failure.

**StarRocks forgot the catalog after a restart.** External catalogs do not always survive a
front end restart. Recreate it with Stage 2.

**A query returns `no policy for role`.** Pass a role that exists in the model. This one
defines `analyst`, `tx_analyst`, and `admin`.
