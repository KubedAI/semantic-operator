# Trino retail example

The same retail `SemanticModel` served by Trino instead of StarRocks. The
model, the demo data, and the certified metrics are identical to
[starrocks/retail](../../starrocks/retail/README.md) — that is the point: the
semantic layer compiles the same model for a different engine and returns the
same numbers.

## Prerequisites

- The [starrocks/retail](../../starrocks/retail/README.md) demo data loaded
  once (Iceberg tables in Glue under `osi_demo`). The loader runs through
  StarRocks today; Trino then reads the same tables.
- Trino running in the cluster with a Glue-backed Iceberg catalog (the
  [Data on EKS](https://awslabs.github.io/data-on-eks/) Trino add-on ships
  one named `iceberg`).
- Trino's IAM role (pod identity or IRSA) needs **S3 read access to the
  Iceberg warehouse bucket**. Glue listing alone is not enough: without S3
  access, `SHOW TABLES` works but every table read fails with
  `Error accessing metadata file`, which the operator surfaces as
  `DriftDetected=True` / `table not resolvable`.

## Install (or switch) the operator to Trino

One install serves one engine. `engine.type` selects both the SQL dialect and
the connection client:

```bash
helm upgrade --install semantic-operator charts/semantic-operator \
  --namespace semantic-system --create-namespace \
  --set image.repository=<acct>.dkr.ecr.us-west-2.amazonaws.com/semantic-operator \
  --set image.tag=<tag> \
  --set engine.type=trino \
  --set engine.host=trino.trino.svc.cluster.local \
  --set aws.region=us-west-2
```

Trino needs no port or user overrides for the Data on EKS defaults (HTTP
8080, no auth). Setting `engine.passwordSecret` implies HTTPS — the Trino
client refuses basic auth over plain HTTP.

## Apply the model

Trino views live inside a catalog, so `connection.viewDatabase` must be
catalog-qualified. That is the only difference from the StarRocks CR:

```yaml
spec:
  connection:
    catalog: iceberg              # Trino catalog name
    database: osi_demo
    viewDatabase: iceberg.semantic_views
```

Apply and watch it go green:

```bash
kubectl apply -f examples/trino/retail/semanticmodel.yaml
kubectl -n semantic-system get semanticmodels -w
```

## Verify

```bash
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &

# Same request as the StarRocks example; the SQL is Trino-flavored
# (double-quoted identifiers, IS NOT DISTINCT FROM) but the numbers match.
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' | jq '.rows'
```

The governed views are readable through any Trino client:

```sql
SELECT * FROM iceberg.semantic_views.store_productivity_by_state;
```

Verified end to end on EKS (Trino 477, Glue/Iceberg): identical results to
the StarRocks path for every certified metric, including the fan-out-safe
`store_productivity` ratio.
