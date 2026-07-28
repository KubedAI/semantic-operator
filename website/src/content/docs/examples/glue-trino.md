---
title: Retail on Glue and Trino
description: The same retail model served by Trino instead of StarRocks, which is the clearest demonstration that the semantics are portable.
---

This walkthrough is deliberately short, because it is the
[StarRocks walkthrough](/examples/glue-starrocks) with two flags changed. That is the
demonstration. The model does not change, the metrics do not change, and the numbers do not
change.

Work through [Prerequisites](/examples/prerequisites) first, with Trino as your engine.

## What actually differs

Three things, and nothing else.

**The engine setting.** `engine.type=trino` selects both the SQL dialect and the connection
client.

**The port.** Trino speaks HTTP on 8080 rather than the MySQL protocol on 9030.

**Where views live.** Trino has no default catalog, so governed views need a catalog
qualified schema such as `iceberg.semantic_views`. On StarRocks a bare `semantic_views` is
enough.

The emitted SQL differs too, but you do not have to care. Trino gets double quoted
identifiers and `IS NOT DISTINCT FROM`, StarRocks gets backticks and `<=>`. The compiler
handles it.

## Install

```bash
helm upgrade --install semantic-operator charts/semantic-operator \
  --set server.auth.allowInsecureHeaderAuth=true \
  --namespace semantic-system --create-namespace \
  --set image.repository=<your-registry>/semantic-operator \
  --set image.tag=0.1.0 \
  --set engine.type=trino \
  --set engine.host=trino.trino.svc.cluster.local
```

**Verify.**

```bash
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &
curl -s -o /dev/null -w '%{http_code}\n' localhost:8090/readyz
```

Expect `200`. If it reports the engine as unreachable, the message names the engine, which
tells you the dialect selection worked and the connection did not.

## Apply the model

Use the same retail model, with `viewDatabase` qualified for Trino.

```yaml
spec:
  connection:
    catalog: iceberg
    database: osi_demo
    viewDatabase: iceberg.semantic_views
```

```bash
kubectl apply -f examples/retail/model/semanticmodel.yaml
kubectl -n semantic-system get semanticmodels -w
```

**Verify.** `VALIDATED=True PUBLISHED=True DRIFT=False`, exactly as on StarRocks.

## Ask the same question

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' | jq
```

**Verify.** New York is `210176.604484`, matching the StarRocks result and the hand
computed ground truth.

Now look at the SQL.

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/sql \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' | jq -r '.plan.sql'
```

**Verify.** Double quoted identifiers throughout and `IS NOT DISTINCT FROM` joining the two
CTEs. Not a backtick anywhere. Different SQL, same answer.

## The rest is identical

Determinism, the 403 on a denied field, the row filtered role, and the governed views all
behave exactly as in the
[StarRocks walkthrough, from step 7 onward](/examples/glue-starrocks). Read the views from
Trino instead.

```sql
SELECT * FROM iceberg.semantic_views.store_productivity_by_state ORDER BY 1;
```

## Watching it from the engine side

Trino ships a web interface, which makes a good demo. Forward it and open it in a browser.

```bash
kubectl -n trino port-forward svc/trino 8080:8080
```

Run a governed query and refresh. Each statement appears with the semantic layer comment as
its identifier, carrying the model version and request hash.

## Clean up

```bash
kubectl delete -f examples/retail/model/semanticmodel.yaml
helm uninstall semantic-operator -n semantic-system
```

## Next

[DataHub, Polaris and Trino](/examples/datahub-polaris-trino) adds an open lakehouse
catalog and imports business meaning from a metadata platform.
