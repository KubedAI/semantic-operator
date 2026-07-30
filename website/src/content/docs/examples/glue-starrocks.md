---
title: Retail on Glue and StarRocks
description: The reference walkthrough. Install the operator, load demo data, generate and certify a model, deploy it, and prove that governance and determinism hold.
---

This is the reference path. It takes about thirty minutes and ends with a governed query
service answering questions over Iceberg tables, with numbers you can check against ground
truth.

Work through [Prerequisites](/examples/prerequisites) first. You need a cluster, StarRocks,
and images pushed to a registry.

Each step below finishes with a verification. If a verification does not match, stop there
rather than continuing, because later steps build on it.

## Step 1. Create the Iceberg catalog in StarRocks

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

## Step 2. Load the demo data

StarRocks reads Iceberg tables from Glue whoever wrote them, so there are two
ways to get demo data. Both end with the same five tables.

### Option A. Reuse tables another engine already wrote

If you have run the [Glue and Trino walkthrough](/examples/glue-trino), its
tables are already registered in Glue and StarRocks can read them directly.
Point the model's `database` at that schema and skip ahead. Verified: StarRocks
returns the same numbers from Iceberg files Trino produced.

### Option B. Let StarRocks create them

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

## Step 3. Install the operator and server

```bash
helm upgrade --install semantic-operator charts/semantic-operator \
  --set server.auth.allowInsecureHeaderAuth=true \
  --namespace semantic-system --create-namespace \
  --set image.repository=public.ecr.aws/data-on-eks/semantic-operator \
  --set image.tag=v0.1.1 \
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

## Step 4. Generate a model from the catalog

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
grep -c 'expression:' tmp/scaffold.yaml   # fields the machine filled in
grep -c 'TODO' tmp/scaffold.yaml          # decisions left for a person
go run ./cmd/ossiectl validate -f tmp/scaffold.yaml
```

Expect roughly 70 fields and around 23 placeholders. The scaffold is already valid, because
a model with no metrics is legal. It just cannot answer anything yet.

This is the honest split. The machine wrote every dataset and column. A person still has to
certify what revenue means and who may read what.

## Step 5. Apply the certified model

The repository contains the filled in version, with seven metrics, governance roles, and
two governed views.

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

## Step 6. Ask a question

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' | jq
```

**Verify.** Six rows, one per state. New York should be `210176.60448413`.

That number is the point of the whole exercise. `store_productivity` is sales divided by
headcount, and headcount lives on the store dimension. The obvious query joins store to
sales and sums the employee count, which repeats each store's headcount once per sales row
and inflates the denominator enormously. Check it yourself.

```sql
-- The wrong version, which looks completely reasonable
SELECT s.s_state,
       SUM(ss.ss_ext_sales_price) / SUM(s.s_number_employees) AS naive
FROM iceberg.osi_demo.store_sales ss
JOIN iceberg.osi_demo.store s ON ss.ss_store_sk = s.s_store_sk
GROUP BY s.s_state;
```

That returns about `12.54` for New York against the correct `210176.60`. The compiler
avoids it by splitting the ratio into two aggregations and deduplicating the denominator on
the store primary key.

<details>
<summary>Expected output, and the same question on Trino</summary>

```json
[["TN","644567.24130897"],["TX","1826350.37571947"]]
```

The [Glue and Trino walkthrough](/examples/glue-trino) returns
`644567.241309` and `1826350.375719` for the same model. The values agree. The
strings differ because the two engines render decimals to a different number of
places, which is a formatting difference and not a disagreement about the
number.

The SQL differs properly, though:

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

Backticks and `<=>`. Trino gets double quotes and `IS NOT DISTINCT FROM` for
exactly the same model.

</details>

## Step 7. Prove it is deterministic

Run exactly the same request again.

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' \
  | jq '{requestHash, cachedResult}'
```

**Verify.** The same `requestHash` as before. `cachedResult` is `true` only when the
install configured `valkey.addr`; without Valkey, it remains `false` and the identical
hash still proves deterministic planning.

To see the SQL without running it, use the dry run endpoint.

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/sql \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["item.i_category"]}' | jq -r '.plan.sql'
```

Note the leading comment carrying the model name, version, and request hash. That is what
lets you trace a query in the StarRocks log back to the request that caused it.

## Step 8. Prove governance holds

An analyst asks for an email address.

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["customer.c_email_address"]}' | jq
```

**Verify.** A 403 with a message naming the field. No SQL was generated and nothing reached
the database, because the policy was checked while the statement was still being built.

Now a role that is restricted to one state.

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: tx_analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["store.s_state"]}' | jq '.rows'
```

**Verify.** One row, Texas only. The row filter was compiled into the WHERE clause rather
than applied to the results afterwards.

## Step 9. Check the BI path

The operator created governed views in StarRocks. Any SQL client can read them, with no
semantic server involved.

```sql
SELECT * FROM semantic_views.store_productivity_by_state ORDER BY 1;
```

**Verify.** The same numbers as step 6. One definition, two access paths, no disagreement.

## Optional. Compare against raw text to SQL

If you have Amazon Bedrock available, run the same business question both ways.

```bash
export MCP_ENDPOINT=http://localhost:8090/mcp
export BEDROCK_MODEL_ID=<your enabled model id>
make demo-nl QUESTION="What is our sales per employee by state?"
```

It prints both queries and both results. The raw path writes the fan out join and reports
about twelve dollars per employee. The semantic path calls the certified metric and reports
the correct figure.

The full measured comparison is in [Benchmark results](/examples/benchmark-results).

## Clean up

Delete the model first. Its finalizer drops the governed views it created.

```bash
kubectl delete -f examples/retail/model/semanticmodel.yaml
helm uninstall semantic-operator -n semantic-system
kubectl delete namespace semantic-system
```

The demo data stays in Glue and S3. Drop the `osi_demo` database if you want it gone.

## If something did not work

**The model sits at `DRIFT=True`.** Read the message with `kubectl describe`. It names the
dataset and the missing table or column. The usual cause is the external catalog not being
readable, which StarRocks reports as a table resolution failure.

**StarRocks forgot the catalog after a restart.** External catalogs do not always survive a
front end restart. Recreate it with step 1.

**A query returns `no policy for role`.** Pass a role that exists in the model. This one
defines `analyst`, `tx_analyst`, and `admin`.

## Next

[The same model on Trino](/examples/glue-trino), which is the same walkthrough with two
different flags and identical results.
