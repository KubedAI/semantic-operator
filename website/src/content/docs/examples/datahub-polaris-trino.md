---
title: DataHub, Polaris and Trino
description: An open lakehouse walkthrough. Polaris is the Iceberg REST catalog, Trino is the engine, and DataHub supplies the business meaning that enrichment imports into the model.
---

This stack replaces AWS Glue with [Apache Polaris](https://polaris.apache.org/) as the
Iceberg REST catalog and adds DataHub as the source of business meaning. It is the closest
of the walkthroughs to a fully open lakehouse.

Work through [Prerequisites](/examples/prerequisites) first. This walkthrough deploys
Polaris and DataHub itself, so you need room for them.

The full flow on a real EKS cluster: deploy the prerequisites, generate a
semantic model from the Polaris catalog, certify it, deploy it with kubectl,
and query it through the governed semantic server. Every stage ends with a
verification step so you always know where you are.

```
Stage 0   prerequisites      EKS cluster with Trino (Data on EKS)
Stage 1   deploy Polaris     Postgres + Polaris + catalog 'demo' on S3
Stage 2   wire Trino         'polaris' catalog over Iceberg REST + OAuth
Stage 3   load data          CTAS the retail tables into Polaris
Stage 4   install operator   semantic-operator with engine.type=trino
Stage 5   author the model   ossiectl derive -> human certifies -> diff
Stage 6   deploy the model   kubectl apply, watch it reconcile
Stage 7   query              REST + MCP, governance, views, Trino UI
```

DataHub is not part of this flow yet. When it lands it adds discovery and
enrichment on top of the same stages.

---

## Stage 0. Prerequisites

An EKS cluster with Trino installed (this was built against the
[Data on EKS](https://awslabs.github.io/data-on-eks/) Trino blueprint), plus
`kubectl`, `helm`, `aws`, and Go 1.26 on your workstation. One-time AWS
setup, roles only, no IAM users:

- An S3 bucket for the Polaris warehouse.
- An IAM role for Polaris bound with **EKS Pod Identity** to
  `chd/polaris-sa`, holding read/write on that bucket.
- Trino's existing Pod Identity role extended with read/write on the same
  bucket (Trino writes table data. Polaris writes table metadata).

**Verify:**

```bash
kubectl get nodes | head -3
kubectl -n trino get pods          # coordinator + worker Running
aws sts get-caller-identity        # credentials valid
```

## Stage 1. Deploy Polaris

```bash
bash examples/stacks/eks/datahub-polaris-trino/eks-up.sh
```

This creates generated credentials (never committed), Postgres on a PVC, the
Polaris server under `polaris-sa` (Pod Identity supplies AWS access. The pod
holds no keys), bootstraps realm `POLARIS`, and creates catalog `demo` on S3
with `stsUnavailable` so every engine brings its own identity.

**Verify:**

```bash
kubectl -n chd get pods            # postgres + polaris Running
kubectl -n chd port-forward svc/polaris 8181:8181 8182:8182 &
curl -s localhost:8182/q/health | jq .status     # "UP"
```

Polaris has no web UI. Its REST API is the interface. To browse it:

```bash
SECRET=$(kubectl -n chd get secret polaris-credentials -o jsonpath='{.data.ROOT_CLIENT_SECRET}' | base64 -d)
TOKEN=$(curl -s -X POST localhost:8181/api/catalog/v1/oauth/tokens -H 'Polaris-Realm: POLARIS' \
  -d grant_type=client_credentials -d client_id=root -d "client_secret=$SECRET" -d scope=PRINCIPAL_ROLE:ALL | jq -r .access_token)
curl -s -H "Authorization: Bearer $TOKEN" -H 'Polaris-Realm: POLARIS' \
  localhost:8181/api/management/v1/catalogs | jq
```

## Stage 2. Wire Trino to Polaris

```bash
bash examples/stacks/eks/datahub-polaris-trino/trino-catalog.sh
```

Adds a `polaris` catalog to Trino (Iceberg REST connector, OAuth2 client
credentials injected from a Secret via `${ENV:..}`) and restarts the Trino
pods. The script's last step is its own verification: `SHOW CATALOGS` must
list `polaris`. A query may fail with `Cannot obtain metadata` for a few
seconds while the worker rejoins after the restart. That is transient.

## Stage 3. Load the demo data

```bash
bash examples/stacks/eks/datahub-polaris-trino/data-load.sh
```

Copies the five retail tables from the Glue-backed catalog into Polaris with
CTAS. Trino writes the Iceberg data files to S3 under its own Pod Identity.
Polaris tracks the metadata. Idempotent. The script prints row counts as its
verification. Expect `store_sales 200000, date_dim 1096, customer 2000,
item 500, store 12`.

## Stage 4. Install the semantic operator

```bash
helm upgrade --install semantic-operator charts/semantic-operator \
  --namespace semantic-system --create-namespace \
  --set image.repository=<acct>.dkr.ecr.<region>.amazonaws.com/semantic-operator \
  --set image.tag=<tag> \
  --set engine.type=trino \
  --set engine.host=trino.trino.svc.cluster.local
```

**Verify:**

```bash
kubectl -n semantic-system get pods                  # manager + 2 servers Ready
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &
curl -s -o /dev/null -w '%{http_code}\n' localhost:8090/readyz   # 200 (pings Trino)
```

## Stage 5. Author the model (derive, then certify)

Generate the physical skeleton straight from the Polaris catalog. No Glue,
no SDK: the derive command reads Trino's `information_schema`, which sees
exactly what the engine sees.

```bash
kubectl -n trino port-forward svc/trino 8080:8080 &
export SQL_DIALECT=trino ENGINE_HOST=127.0.0.1 ENGINE_PORT=8080
go run ./cmd/ossiectl derive -source engine -catalog polaris -database osi_demo \
  -model tpcds_retail_model -name tpcds-retail -out /tmp/scaffold.yaml
```

Open `/tmp/scaffold.yaml`: every dataset and field is filled in, candidate
joins are suggested from key naming, and the business parts (metrics,
governance, views, primary keys) are `TODO` placeholders. **A human turns
the scaffold into the certified model**. This repo already contains that
certified version, [`semanticmodel.yaml`](https://github.com/KubedAI/semantic-operator/blob/main/examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml). Show the
split between what the machine wrote and what the human added:

```bash
# the machine filled the physical half...
grep -c 'expression:' /tmp/scaffold.yaml         # every column became a field
# ...and left the business half explicitly open
grep -c 'TODO' /tmp/scaffold.yaml                # the gaps a human must certify
grep -c 'TODO' examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml   # 0 in the certified model
# what the human wrote: certified metrics and access policy
grep -B1 -A6 'metrics:' examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml | head -20
grep -A12 'governance:' examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml | head -14
```

That is the demo's honest moment: machines wrote the physical half, people
wrote the metrics and the access rules, and nothing was invented by an LLM.

**Verify:** both files validate offline, no cluster needed.

```bash
go run ./cmd/ossiectl validate -f /tmp/scaffold.yaml
go run ./cmd/ossiectl validate -f examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml
```

## Stage 6. Deploy the model

```bash
kubectl apply -f examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml
kubectl -n semantic-system get semanticmodels -w    # wait: Validated, Published, Drift=False
```

**Verify** what the operator did:

```bash
kubectl -n semantic-system describe semanticmodel tpcds-retail | grep -A8 Conditions
kubectl -n semantic-system get semanticmodel tpcds-retail \
  -o jsonpath='{range .status.bindings[*]}{.dataset}{" -> "}{.table}{"\n"}{end}'
# bindings show polaris.osi_demo.* — bound through Polaris, drift-checked live
kubectl -n semantic-system get cm sm-tpcds-retail-compiled -o jsonpath='{.metadata.labels}'
# the published, versioned compiled artifact
```

## Stage 7. Query it

Discovery, then the certified number, then proof of determinism:

```bash
curl -s localhost:8090/v1/models | jq
curl -s localhost:8090/v1/models/tpcds_retail_model/metrics | jq -r '.metrics[].name'

curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' \
  | jq '{rows, requestHash, cachedResult}'
# run it twice: identical requestHash, cachedResult=true, ~0ms
```

Governance, compiled in, never bolted on:

```bash
# column policy: analyst may not read PII -> 403 before any SQL exists
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["customer.c_email_address"]}' | jq

# row policy: tx_analyst is compiled down to Texas only
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: tx_analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["store.s_state"]}' | jq '.rows'

# the exact SQL, without executing it
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/sql \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["item.i_category"]}' | jq -r '.plan.sql'
```

**Verify from the engine's side:** open the Trino web UI at
<http://localhost:8080/ui> (any username, no password). Every governed query
appears there carrying its `/* semantic-layer model=.. Request=.. */`
comment, and the governed views are plain SQL objects any client can read:

```bash
POD=$(kubectl -n trino get pods -o name | grep coordinator | head -1 | cut -d/ -f2)
kubectl -n trino exec "$POD" -c trino-coordinator -- \
  trino --execute "SELECT * FROM polaris.semantic_views.store_productivity_by_state ORDER BY 1"
```

AI agents use the same server over MCP at `/mcp` (tools: `list_models`,
`list_metrics`, `list_dimensions`, `query_metric`). The agent selects
certified metrics and never writes SQL. The [agent example](https://github.com/KubedAI/semantic-operator/blob/main/examples/stacks/kind/datahub-polaris-starrocks/agent) drives
exactly this.

## Reset / re-run

Every script is idempotent. Port-forward hygiene between runs:

```bash
pkill -f 'kubectl.*port-forward'
```

Known operational notes: the `trino-catalog` ConfigMap may be managed by the
cluster's Terraform, so re-run `trino-catalog.sh` after any infrastructure
apply. The expected certified numbers are NY store_productivity 210176.60
(matches hand-computed ground truth) and TX-only 52485374.78 for the
row-filtered role.

