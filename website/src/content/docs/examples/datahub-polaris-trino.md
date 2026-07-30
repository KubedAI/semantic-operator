---
title: DataHub, Polaris and Trino
description: An open lakehouse walkthrough. Polaris is the Iceberg REST catalog, Trino is the engine, and DataHub supplies the business meaning that enrichment imports into the model.
---

This stack replaces AWS Glue with [Apache Polaris](https://polaris.apache.org/) as the
Iceberg REST catalog and adds DataHub as the source of business meaning. It is the closest
of the walkthroughs to a fully open lakehouse.

Work through [Prerequisites](/examples/prerequisites) first. This walkthrough expects
DataHub to be running already in the `datahub` namespace, with its system credential in
`datahub-auth-secrets`. The scripts discover the GMS service themselves, because the
DataHub chart names it after the release. Installing DataHub is not automated here.

The full flow on a real EKS cluster: deploy the prerequisites, generate a
semantic model from the Polaris catalog, certify it, deploy it with kubectl,
and query it through the governed semantic server. Every stage ends with a
verification step so you always know where you are.

| Stage | What it does | On the semantic-on-eks stack |
|---|---|---|
| **0** Prerequisites | EKS cluster with Trino, Polaris, DataHub | Check only |
| **1** Deploy Polaris | Postgres, Polaris, catalog `demo` on S3 | Already done, skip |
| **2** Wire Trino | `polaris` catalog over Iceberg REST and OAuth | Already done, skip |
| **3** Load data | CTAS the retail tables into Polaris | **Start here** |
| **4** Install operator | `semantic-operator` with `engine.type=trino` | Run |
| **5** DataHub metadata | Ingest tables, add stewardship metadata | Run |
| **6** Author the model | Derive, enrich from DataHub, human certifies | Run |
| **7** Deploy the model | `kubectl apply`, watch it reconcile | Run |
| **8** Query it | REST, MCP, governance, views, Trino UI | Run |

Every stage here has been run end to end on a live EKS cluster with Polaris,
Trino, DataHub, and Valkey, and each one prints its own verification. The
DataHub stages still assume DataHub is already installed. Installing it is not
automated here.

---

## Stage 0. Prerequisites
**Run this stage: no, check only.** Nothing here changes your cluster.


You need `kubectl`, `helm`, `aws`, and Go 1.26 on your workstation, and a
cluster running Trino, Polaris, DataHub, and optionally Valkey.

### The quickest route

The [Data on EKS](https://awslabs.github.io/data-on-eks/) **semantic-on-eks**
stack deploys all of that in one go, along with the S3 bucket and the Pod
Identity roles this walkthrough needs. It is what the walkthrough was tested
against.

:::caution[This stack costs real money]
It provisions roughly 10 to 15 EC2 instances, plus EKS, S3, and networking.
That is a substantial hourly cost. Deploy it when you are ready to work
through the walkthrough, and run its `cleanup.sh` as soon as you are done.
Check the cost yourself for your region before you start.
:::

Once it reports Ready, Stages 1 and 2 are already done for you. Polaris runs in
the `polaris` namespace and Trino already has the `polaris` catalog, so **go
straight to Stage 3**. Running Stages 1 and 2 anyway builds a second, unused
Polaris.

### Or bring your own cluster

Nothing here depends on that stack. Any EKS cluster works if it has:

- **Trino**, with an Iceberg REST catalog connector available.
- **Apache Polaris**, reachable in the cluster, with a catalog on S3.
- **DataHub**, for Stages 5 and 6. The enrichment stages need it. The rest of
  the walkthrough does not.
- **Valkey** or any Redis-protocol cache, optional. Without it the semantic
  server still answers, it just reports `cachedResult: false` every time.

Plus this one-time AWS setup, roles only, no IAM users:

- An S3 bucket for the Polaris warehouse.
- An IAM role for Polaris bound with **EKS Pod Identity** to
  `<namespace>/polaris-sa`, holding read/write on that bucket.
- Trino's Pod Identity role with read/write on the same bucket. Trino writes
  the table data and Polaris writes the table metadata, so both need it.

If you are deploying Polaris yourself, start at **Stage 1**.

### Either way

Nothing in this walkthrough is exposed outside the cluster. Every service is
reached with `kubectl port-forward`, and no stage creates a load balancer.

These are the local ports it uses. Trino is on 8081 rather than 8080 because
8080 is a busy port, and the semantic-on-eks stack already suggests it for
ArgoCD.

| Local port | Service | Needed from |
|---|---|---|
| 8181 | Polaris catalog API | Stage 1 |
| 8182 | Polaris health and management | Stage 1 |
| 8081 | Trino, mapped from its 8080 | Stage 6 |
| 8091 | DataHub GMS, mapped from its 8080 | Stage 5 |
| 8090 | Semantic server | Stage 4 |

:::note[About the port-forwards]
Every port-forward below is preceded by a `pkill` for that specific port. A
forward left running from an earlier stage would otherwise make the new one
exit with `address already in use`, which looks like a failure and is not.

The `pkill` targets one port mapping, so it will not disturb your other
forwards. To see what holds a port, or to clear them all:

```bash
lsof -ti tcp:8181
pkill -f 'kubectl.*port-forward'
```
:::

**Verify** whichever route you took:

```bash
kubectl get nodes | head -3
# coordinator + worker Running
kubectl -n trino get pods
# Running
kubectl get pods -A | grep -E 'polaris|datahub-gms'
# credentials valid
aws sts get-caller-identity
```

## Stage 1. Deploy Polaris

**Run this stage: only if your cluster has no Polaris.**

:::caution[On the semantic-on-eks stack, skip this stage and Stage 2]
That stack already deploys Polaris and already wires Trino to it. Go to
[Stage 3](#stage-3-load-the-demo-data).
:::

Check before you run anything.

```bash
kubectl -n polaris get pods
```

If Polaris is already Running, skip to Stage 2. Only run the script below when
there is no Polaris on the cluster.

It deploys into the `polaris` namespace, the same one the semantic-on-eks stack
uses. That is deliberate. Whichever way Polaris got there, every command from
here on is identical. Set `POLARIS_NS` if you need a different namespace, and
substitute it in the commands that follow.

```bash
bash examples/stacks/eks/datahub-polaris-trino/eks-up.sh
```

This creates generated credentials (never committed), Postgres on a PVC, the
Polaris server under `polaris-sa` (Pod Identity supplies AWS access. The pod
holds no keys), bootstraps realm `POLARIS`, and creates catalog `demo` on S3
with `stsUnavailable` so every engine brings its own identity.

**Verify:**

```bash
# postgres + polaris Running
kubectl -n polaris get pods
pkill -f 'port-forward.*8181:8181' 2>/dev/null
kubectl -n polaris port-forward svc/polaris 8181:8181 &
pkill -f 'port-forward.*8182:8182' 2>/dev/null
kubectl -n polaris port-forward svc/polaris-mgmt 8182:8182 &
# "UP"
curl -s localhost:8182/q/health | jq .status
```

<details>
<summary>What a successful Stage 1 run looks like</summary>

```
namespace/polaris created
secret/postgres-credentials created
[eks-up] created postgres-credentials
secret/polaris-credentials created
[eks-up] created polaris-credentials
configmap/postgres-init created
persistentvolumeclaim/postgres-data created
deployment.apps/postgres created
service/postgres created
deployment "postgres" successfully rolled out
serviceaccount/polaris-sa created
job.batch/polaris-bootstrap created
deployment.apps/polaris created
service/polaris created
service/polaris-mgmt created
[eks-up] waiting for bootstrap job (creates realm POLARIS + root principal)
job.batch/polaris-bootstrap condition met
deployment "polaris" successfully rolled out
[eks-up] creating catalog 'demo' (base s3://<your-polaris-bucket>/demo)
CREATE_HTTP=201
[eks-up] polaris up: realm POLARIS, catalog 'demo' on s3://<your-polaris-bucket>/demo
```

`CREATE_HTTP=201` is the line that matters. A `409` means the catalog already
exists, which is fine on a re-run.

</details>

Polaris exposes two ports on two services. `polaris` serves the catalog API on
8181, and `polaris-mgmt` serves health and management on 8182, which is why
there are two port-forwards.

Polaris has no web UI. Its REST API is the interface. To browse it:

```bash
SECRET=$(kubectl -n polaris get secret polaris-credentials -o jsonpath='{.data.ROOT_CLIENT_SECRET}' | base64 -d)
TOKEN=$(curl -s -X POST localhost:8181/api/catalog/v1/oauth/tokens -H 'Polaris-Realm: POLARIS' \
  -d grant_type=client_credentials -d client_id=root -d "client_secret=$SECRET" -d scope=PRINCIPAL_ROLE:ALL | jq -r .access_token)
curl -s -H "Authorization: Bearer $TOKEN" -H 'Polaris-Realm: POLARIS' \
  localhost:8181/api/management/v1/catalogs | jq
```

<details>
<summary>Expected catalog listing</summary>

```json
{
  "catalogs": [
    {
      "type": "INTERNAL",
      "name": "demo",
      "properties": {
        "default-base-location": "s3://<your-polaris-bucket>/demo"
      },
      "entityVersion": 1,
      "storageConfigInfo": {
        "region": "us-west-2",
        "stsUnavailable": true,
        "storageType": "S3",
        "allowedLocations": [
          "s3://<your-polaris-bucket>/demo"
        ]
      }
    }
  ]
}
```

`stsUnavailable: true` is deliberate. Polaris hands out no credentials, so
every engine arrives with its own identity.

</details>

## Stage 2. Wire Trino to Polaris

**Run this stage: only if Trino has no `polaris` catalog.**

:::caution[On the semantic-on-eks stack, skip this stage too]
Trino already has the `polaris` catalog. Go to
[Stage 3](#stage-3-load-the-demo-data).
:::

Check first. If this prints `polaris`, the catalog exists and you are done with
this stage.

```bash
kubectl -n trino exec deploy/trino-coordinator -c trino-coordinator -- \
  trino --execute "SHOW CATALOGS" 2>/dev/null | grep polaris
```

If it prints `"polaris"`, the catalog exists and this stage is done. Skip to
[Stage 3](#stage-3-load-the-demo-data). If it prints nothing, run the script
below.

Without the `2>/dev/null` the Trino CLI also prints
`WARNING: Unable to create a system terminal`. That is the CLI noticing it has
no interactive terminal, it is harmless, and it appears on every `trino
--execute` in this walkthrough.

Only if it printed nothing:

```bash
bash examples/stacks/eks/datahub-polaris-trino/trino-catalog.sh
```

Adds a `polaris` catalog to Trino (Iceberg REST connector, OAuth2 client
credentials injected from a Secret via `${ENV:..}`) and restarts the Trino
pods. The script's last step is its own verification: `SHOW CATALOGS` must
list `polaris`. A query may fail with `Cannot obtain metadata` for a few
seconds while the worker rejoins after the restart. That is transient.

## Stage 3. Load the demo data
**Run this stage: yes, always.** This is where everyone starts on the
semantic-on-eks stack.


```bash
bash examples/stacks/eks/datahub-polaris-trino/data-load.sh
```

Builds the five retail tables in Polaris with CTAS from Trino's built-in
`tpcds` connector. The data is generated inside the coordinator, so this stage
needs no pre-existing warehouse and no S3 access beyond the Polaris bucket
Trino already writes to. Trino writes the Iceberg files, Polaris tracks the
metadata. Idempotent, so a second run skips tables that exist.

Two things the script does that are worth knowing, because a plain copy of
`tpcds` will not give you a working demo.

It derives `d_month_name`, which the certified model uses as a dimension and
`tpcds` does not provide. And it moves half the stores to Texas, choosing only
from stores that actually carry sales, because `tpcds` puts every store in one
state and populates only a subset of store keys. Without that, a breakdown by
state is a single row and the `tx_analyst` row filter matches nothing.

**Verify:** the script prints its own checks at the end.

<details>
<summary>Expected load output, first run</summary>

```
[data-load] creating schema polaris.osi_demo
CREATE SCHEMA
[data-load] loading date_dim (2000-2002)
CREATE TABLE: 1096 rows
[data-load] loading item
CREATE TABLE: 18000 rows
[data-load] loading customer
CREATE TABLE: 100000 rows
[data-load] loading store
CREATE TABLE: 12 rows
[data-load] loading store_sales (this is the big one, a few minutes)
CREATE TABLE: 1591154 rows
[data-load] spreading stores with sales across two states, so row filters have an effect
UPDATE: 12 rows
[data-load]   TX stores: 2,7,10
UPDATE: 3 rows
[data-load] verify: row counts in Polaris
"customer","100000"
"date_dim","1096"
"item","18000"
"store","12"
"store_sales","1591154"
[data-load] verify: stores by state, with sales, so both states return rows
"TN","9","796806"
"TX","3","794348"
[data-load] verify: no orphan foreign keys
"0","0"
```

</details>

<details>
<summary>Expected load output, running it again</summary>

```
[data-load] creating schema polaris.osi_demo
CREATE SCHEMA
[data-load] loading date_dim (2000-2002)
CREATE TABLE: 0 rows
[data-load] loading item
CREATE TABLE: 0 rows
[data-load] loading customer
CREATE TABLE: 0 rows
[data-load] loading store
CREATE TABLE: 0 rows
[data-load] loading store_sales (this is the big one, a few minutes)
CREATE TABLE: 0 rows
[data-load] spreading stores with sales across two states, so row filters have an effect
UPDATE: 12 rows
[data-load]   TX stores: 2,7,10
UPDATE: 3 rows
[data-load] verify: row counts in Polaris
"customer","100000"
"date_dim","1096"
"item","18000"
"store","12"
"store_sales","1591154"
[data-load] verify: stores by state, with sales, so both states return rows
"TN","9","796806"
"TX","3","794348"
[data-load] verify: no orphan foreign keys
"0","0"
```

**`CREATE TABLE: 0 rows` is success, not failure.** Every create is
`IF NOT EXISTS`, so a table that already exists is left alone and reports zero
rows written. Nothing was skipped by mistake and nothing was lost. The counts
underneath are read back from Polaris and are the real numbers, which is why
they are identical to the first run.

The two `UPDATE` lines run every time on purpose. The first resets all 12
stores to `TN`, the second moves the ones that carry sales to `TX`. Doing it in
that order means repeated runs always land on the same split rather than
drifting.

</details>

**Whichever run you are on, these must hold.** Both states show a non-zero
sales count, and both orphan counts are `0`. If `TX` shows zero sales, the row
filter in Stage 8 will return nothing.

### Check it yourself before moving on

The script grades its own homework, so run these two against Trino directly.

**1. Is everything loaded?**

```bash
kubectl -n trino exec deploy/trino-coordinator -c trino-coordinator -- trino --execute "
SELECT 'store_sales' AS table_name, count(*) AS row_count FROM polaris.osi_demo.store_sales
UNION ALL SELECT 'customer', count(*) FROM polaris.osi_demo.customer
UNION ALL SELECT 'item',     count(*) FROM polaris.osi_demo.item
UNION ALL SELECT 'date_dim', count(*) FROM polaris.osi_demo.date_dim
UNION ALL SELECT 'store',    count(*) FROM polaris.osi_demo.store
ORDER BY 1" 2>/dev/null
```

```
"customer","100000"
"date_dim","1096"
"item","18000"
"store","12"
"store_sales","1591154"
```

**2. Do the joins the model needs actually resolve?**

This walks the same four joins the semantic model compiles, so if it returns
sensible numbers, Stages 7 and 8 will work.

```bash
kubectl -n trino exec deploy/trino-coordinator -c trino-coordinator -- trino --execute "
SELECT s.s_state,
       count(DISTINCT s.s_store_sk)          AS stores_with_sales,
       count(*)                              AS sales_rows,
       round(sum(ss.ss_ext_sales_price), 2)  AS revenue
FROM polaris.osi_demo.store_sales ss
JOIN polaris.osi_demo.store    s ON s.s_store_sk    = ss.ss_store_sk
JOIN polaris.osi_demo.date_dim d ON d.d_date_sk     = ss.ss_sold_date_sk
JOIN polaris.osi_demo.item     i ON i.i_item_sk     = ss.ss_item_sk
JOIN polaris.osi_demo.customer c ON c.c_customer_sk = ss.ss_customer_sk
GROUP BY s.s_state ORDER BY s.s_state" 2>/dev/null
```

```
"TN","3","796806","1516666718.80"
"TX","3","794348","1510391760.72"
```

Two rows, both with a non-zero revenue, is what you need. One row means the
`TX` split did not take and the row-filter demo in Stage 8 will come back
empty.

`stores_with_sales` is 3 for each because an inner join only sees stores that
have rows. Twelve stores exist, six of them carry sales. That is how tpcds
generates the data and it is not a problem.

Keep that `TN` revenue. In Stage 8 the semantic server returns exactly
`1516666718.80` for `total_sales` in `TN`, which is the whole point: the
governed path and hand written SQL agree.

**3. Is it really Iceberg on S3?**

Worth proving rather than assuming, since the whole point of this stack is an
open table format you are not locked into.

```bash
kubectl -n trino exec deploy/trino-coordinator -c trino-coordinator -- trino --execute "
SELECT * FROM polaris.osi_demo.\"store_sales\$properties\"" 2>/dev/null
```

```
"format","iceberg/PARQUET"
"provider","iceberg"
"format-version","2"
"location","s3://<your-polaris-bucket>/demo/osi_demo/store_sales-<uuid>"
```

Take that `location` and look at it directly. Iceberg tables are a `data/`
directory of Parquet plus a `metadata/` directory holding the table metadata,
the manifests, and the snapshot log.

```bash
aws s3 ls s3://<your-polaris-bucket>/demo/osi_demo/store_sales-<uuid>/metadata/
```

```
00000-....metadata.json          the table metadata
....-m0.avro                     a manifest, listing data files
snap-....avro                    a snapshot, the atomic commit
```

The snapshot log is queryable too, and `$snapshots` only exists on an Iceberg
table.

```bash
kubectl -n trino exec deploy/trino-coordinator -c trino-coordinator -- trino --execute "
SELECT committed_at, operation, summary['total-records'] AS records
FROM polaris.osi_demo.\"store_sales\$snapshots\" ORDER BY committed_at" 2>/dev/null
```

```
"2026-07-30 17:22:01.532 UTC","append","1591154"
```

Polaris is the catalog of record, not Glue. Nothing here writes to Glue, and
any `osi_demo` tables you see there belong to a different stack.

Scale is adjustable. `TPCDS_SCALE=sf10 bash .../data-load.sh` gives a larger
fact table, at the cost of a longer load.

## Stage 4. Install the semantic operator
**Run this stage: yes, always.** The stack does not install the operator.


The semantic-on-eks stack ships Valkey, so turn on result caching while you are
here. A `secretKeyRef` can only read a Secret in its own namespace, so copy the
password across first.

```bash
kubectl create namespace semantic-system --dry-run=client -o yaml | kubectl apply -f -
kubectl -n semantic-system create secret generic valkey-auth \
  --from-literal=password="$(kubectl -n valkey get secret valkey-auth -o jsonpath='{.data.default}' | base64 -d)"
```

Then install.

```bash
helm upgrade --install semantic-operator charts/semantic-operator \
  --namespace semantic-system --create-namespace \
  --set server.auth.allowInsecureHeaderAuth=true \
  --set image.repository=public.ecr.aws/data-on-eks/semantic-operator \
  --set image.tag=v0.1.1 \
  --set engine.type=trino \
  --set engine.host=trino.trino.svc.cluster.local \
  --set valkey.addr=valkey.valkey.svc.cluster.local:6379 \
  --set valkey.passwordSecret.name=valkey-auth \
  --set valkey.passwordSecret.key=password
```

`engine.port` is not set because the Trino client already defaults to 8080.

<details>
<summary>Expected install output</summary>

```
Release "semantic-operator" has been upgraded. Happy Helming!
NAME: semantic-operator
LAST DEPLOYED: Thu Jul 30 13:06:45 2026
NAMESPACE: semantic-system
STATUS: deployed
REVISION: 1
TEST SUITE: None
NOTES:
semantic-operator installed in namespace semantic-system.

Endpoints (in-cluster):
  MCP  : http://semantic-operator-server.semantic-system.svc:8090/mcp
  REST : http://semantic-operator-server.semantic-system.svc:8090/v1/models

Next steps:
  1. Apply a model. Pick the one matching the engine you installed:
       examples/retail/model/semanticmodel.yaml                        (Glue + StarRocks)
       examples/stacks/eks/glue-trino/semanticmodel.yaml               (Glue + Trino)
       examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml    (Polaris + Trino)

     kubectl apply -f <model>.yaml

  2. kubectl get semanticmodels -n semantic-system -w
     Wait for VALIDATED=True PUBLISHED=True DRIFT=False.

  3. Query it. Discovery is governed, so send a role:
     kubectl port-forward -n semantic-system svc/semantic-operator-server 8090:8090
     curl -s localhost:8090/v1/models -H 'X-Semantic-Role: analyst' | jq
```

`STATUS: deployed` is the line that matters. On a first install it says
`has been installed` with `REVISION: 1`. Re-running the command says
`has been upgraded` and the revision climbs, which is normal and not a sign
anything went wrong.

For this walkthrough the model to apply in Stage 7 is the Polaris and Trino
one, the third in that list.

</details>

Without a Valkey, drop the three `valkey.*` flags. Everything still works, the
server just reports `cachedResult: false` on every request.

:::note[Helm resets what you leave out]
`helm upgrade` without `--reuse-values` returns any flag you omit to its chart
default. Re-run the command in full every time rather than passing only what
changed, or caching and other settings will switch off without saying so.
:::

**Verify:**

```bash
kubectl -n semantic-system get pods
pkill -f 'port-forward.*8090:8090' 2>/dev/null
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &
curl -s -o /dev/null -w '%{http_code}\n' localhost:8090/readyz
```

<details>
<summary>Expected verification output</summary>

```
NAME                                         READY   STATUS    RESTARTS   AGE
semantic-operator-manager-78494d67cd-jbtrf   1/1     Running   0          45s
semantic-operator-server-845f47764d-mnrc7    1/1     Running   0          45s
semantic-operator-server-845f47764d-ppq9b    1/1     Running   0          45s

200
```

The `200` from `/readyz` means more than "the process is up". The server has
synced the model store and successfully pinged Trino. A `503` here is almost
always the engine host or port.

Confirm caching is wired while you are here.

```bash
kubectl -n semantic-system get deploy semantic-operator-server -o json \
  | jq -r '.spec.template.spec.containers[0].env[] | select(.name|startswith("VALKEY")) | .name'
```

```
VALKEY_ADDR
VALKEY_DB
VALKEY_PASSWORD
```

Nothing printed means the `valkey.*` flags did not make it into the release, so
Stage 8 will report `cachedResult: false` every time.

</details>

## Stage 5. Import metadata from DataHub
**Run this stage: yes, if you want the DataHub part of the story.** Skip it and
Stage 6 still works, the model just arrives without imported descriptions,
synonyms, or the PII tag.


DataHub must already be running in the `datahub` namespace. The DataHub chart
prefixes its services with the release name, so GMS is `<release>-datahub-gms`
rather than one fixed name. Both scripts discover it, and `DATAHUB_GMS_SVC`
overrides that if discovery picks the wrong one.

```bash
kubectl -n datahub get svc | grep datahub-gms
```

Ingest the Polaris
datasets through Trino:

```bash
bash examples/stacks/eks/datahub-polaris-trino/datahub-ingest.sh
kubectl -n datahub logs job/datahub-ingest-polaris --tail=20
```

For a reproducible demo, the annotation script creates a small glossary, documents
selected datasets and fields, and marks the customer email field as PII. Start a
port-forward first:

```bash
GMS=$(kubectl -n datahub get svc -o name | grep -m1 datahub-gms | cut -d/ -f2)
pkill -f 'port-forward.*8091:8080' 2>/dev/null
kubectl -n datahub port-forward "svc/$GMS" 8091:8080 &
bash examples/stacks/eks/datahub-polaris-trino/datahub-annotate.sh
```

### See it in the DataHub UI

This is the stage worth showing on a screen. Everything the scripts just wrote
is what a data steward would normally curate by hand over weeks.

```bash
FE=$(kubectl -n datahub get svc -o name | grep -m1 datahub-frontend | cut -d/ -f2)
pkill -f 'port-forward.*9002:9002' 2>/dev/null
kubectl -n datahub port-forward "svc/$FE" 9002:9002 &
```

Open <http://localhost:9002> and sign in with `datahub` / `datahub`.

Search for `osi_demo` and open **customer** on the **Trino** platform. Look for
three things, because these are exactly what Stage 6 imports into the model.

| In the UI | Becomes in the model |
|---|---|
| The dataset's glossary term, `Buyer` | `ai_context.synonyms`, how an agent grounds a phrase |
| Column descriptions | Field documentation |
| The `PII` tag on `c_email_address` | `governance.denyFields`, a 403 before any SQL runs |

Search results also show datasets on other platforms if something else has
ingested into this DataHub. The ones this walkthrough created are on the
**Trino** platform, named `polaris.osi_demo.*`.

**Verify** without the UI if you prefer, straight from GMS:

```bash
SECRET=$(kubectl -n datahub get secret datahub-auth-secrets -o jsonpath='{.data.system_client_secret}' | base64 -d)
URN='urn:li:dataset:(urn:li:dataPlatform:trino,polaris.osi_demo.customer,PROD)'
curl -s -X POST localhost:8091/api/graphql \
  -H 'Content-Type: application/json' -H "Authorization: Basic __datahub_system:${SECRET}" \
  -d "{\"query\":\"{dataset(urn:\\\"$URN\\\"){glossaryTerms{terms{term{urn}}} editableSchemaMetadata{editableSchemaFieldInfo{fieldPath globalTags{tags{tag{urn}}}}}}}\"}" | jq
```

The PII tag sits on the **column**, not the dataset, so a dataset-level tag
count of zero is normal.

<details>
<summary>Expected metadata</summary>

```
glossary term : urn:li:glossaryTerm:Buyer
column        : c_email_address
                tag  = urn:li:tag:PII
                desc = Customer contact email.
```

</details>

These scripts still need stronger GraphQL error and read-back checks. Confirm the
descriptions, glossary terms, and PII tag in DataHub before continuing.

## Stage 6. Author the model (derive, enrich, then certify)

**Run this stage: yes.** Do it in two passes, because the difference between
them is the clearest way to show what a metadata catalog is worth.

Output goes to `tmp/` in the repo, which is gitignored.

### Pass 1. Derive without DataHub

The physical skeleton, straight from the Polaris catalog. No Glue and no SDK.
The derive command reads Trino's `information_schema`, so it sees exactly what
the engine sees.

```bash
pkill -f 'port-forward.*8081:8080' 2>/dev/null
kubectl -n trino port-forward svc/trino 8081:8080 &
export SQL_DIALECT=trino ENGINE_HOST=127.0.0.1 ENGINE_PORT=8081
mkdir -p tmp
go run ./cmd/ossiectl derive -source engine -catalog polaris -database osi_demo \
  -model tpcds_retail_model -name tpcds-retail -out tmp/scaffold-plain.yaml
```

Every dataset and column is filled in and candidate joins are inferred from key
naming. What is missing is meaning. Open the file and look at the governance
block:

```yaml
#       denyFields: []                 # e.g. ["customer.c_email_address"]
```

A commented-out placeholder. Correct, and useless until somebody remembers
which columns hold personal data.

### Pass 2. Derive with DataHub

Same command, plus the enrichment flags.

This pass needs the DataHub port-forward from Stage 5 on 8091. The `export`
below does not, because it reads the Secret through the Kubernetes API rather
than over the forward, but the `derive` call does.

```bash
GMS=$(kubectl -n datahub get svc -o name | grep -m1 datahub-gms | cut -d/ -f2)
pkill -f 'port-forward.*8091:8080' 2>/dev/null
kubectl -n datahub port-forward "svc/$GMS" 8091:8080 &
sleep 3

export DATAHUB_TOKEN="Basic __datahub_system:$(kubectl -n datahub get secret \
  datahub-auth-secrets -o jsonpath='{.data.system_client_secret}' | base64 -d)"
go run ./cmd/ossiectl derive -source engine -catalog polaris -database osi_demo \
  -enrich datahub -datahub-url http://localhost:8091 \
  -datahub-platform trino -datahub-dataset-prefix polaris \
  -model tpcds_retail_model -name tpcds-retail -out tmp/scaffold-enriched.yaml
```

```
enriched 5/5 tables from DataHub (1 sensitive columns, 0 deprecated)
```

If DataHub is unreachable the command fails and writes nothing, rather than
quietly producing a model that looks enriched and is not.

### Compare the two

```bash
for f in tmp/scaffold-plain.yaml tmp/scaffold-enriched.yaml; do
  printf '%-28s fields=%s descriptions=%s synonyms=%s\n' "$(basename $f)" \
    "$(grep -c 'expression:' $f)" "$(grep -c 'description:' $f)" "$(grep -c 'synonyms:' $f)"
done
diff tmp/scaffold-plain.yaml tmp/scaffold-enriched.yaml | grep '^>' | head -20
```

<details>
<summary>Expected difference</summary>

```
scaffold-plain.yaml          fields=244 descriptions=7  synonyms=6
scaffold-enriched.yaml       fields=244 descriptions=12 synonyms=9
```

The physical half is identical, 244 fields either way. What arrives is meaning:

```
+ synonyms: ["Buyer"]
+ description: "Customer contact email."
+ description: "Merchandise category."
+ synonyms: ["Merchandise"]
+ description: "Headcount at the store. Do not sum across a sales join."
+ synonyms: ["Storefront"]
+ description: "Extended sales price for the line item, net of discounts."
+ synonyms: ["Revenue"]
+ description: "Net profit for the line item."
+ denyFields:
+   - "customer.c_email_address"
```

</details>

Three of those matter more than the rest.

**`synonyms: ["Revenue"]` on `ss_ext_sales_price`.** That table has seven
plausible money columns: `ss_ext_list_price`, `ss_ext_sales_price`,
`ss_list_price`, `ss_net_paid`, `ss_net_paid_inc_tax`, `ss_net_profit`,
`ss_sales_price`. An agent writing SQL picks one and cannot tell you why. The
glossary term says which one the business means by revenue.

**"Do not sum across a sales join" on `s_number_employees`.** That is the
fan-out trap written down. Summing headcount over a joined fact table gives a
number orders of magnitude too large. A steward wrote that sentence once and
now every consumer sees it.

**`denyFields` is real, not a comment.** The steward's PII tag became an access
rule. In Stage 8 a request for that column returns 403 before any SQL exists.

### Then a human certifies it

Neither scaffold contains metrics. **A person turns the scaffold into the
certified model**, and this repo already contains that version,
[`semanticmodel.yaml`](https://github.com/KubedAI/semantic-operator/blob/main/examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml).

```bash
# the machine filled the physical half
grep -c 'expression:' tmp/scaffold-enriched.yaml
# and left the business half explicitly open
grep -c 'TODO' tmp/scaffold-enriched.yaml
# none left in the certified model
grep -c 'TODO' examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml
# what the human wrote: certified metrics and access policy
grep -A12 'governance:' examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml | head -14
```

That is the demo's honest moment. Machines wrote the physical half, the catalog
supplied the vocabulary and the sensitivity, people wrote the metrics and the
access rules, and no LLM invented any of it.

**Verify:** all three validate offline, no cluster needed.

```bash
go run ./cmd/ossiectl validate -f tmp/scaffold-plain.yaml
go run ./cmd/ossiectl validate -f tmp/scaffold-enriched.yaml
go run ./cmd/ossiectl validate -f examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml
```

## Stage 7. Deploy the model

**Run this stage: yes, always.**

### First, the handoff

In Stage 6 a machine wrote you a model. It read the Polaris catalog and filled
in everything a database can tell you: five datasets, all 121 columns, the
join candidates it could infer from `*_sk` naming. DataHub added what a steward
had recorded, so descriptions and the PII classification arrived too.

Then it stopped, on purpose, at the point where judgement begins. Open your
scaffold and it says so plainly:

```yaml
metrics: []
# metrics: certified aggregate definitions, the core of the model. A model
# with no metrics validates but answers nothing; define at least one.
```

No machine and no LLM decides what your business means by revenue.

**Now it is your turn.** A person opens that file and does the half a generator
cannot. They drop the columns nobody should be querying, 121 down to 34. They
confirm which of the inferred joins are real and delete the rest. They set a
primary key on every dataset, which is what makes a ratio metric safe across a
join. They write the metric expressions. They decide who may see what.

That finished file is already in this repo, so the walkthrough does not stall
while you hand write seven metrics. It is the same model as your scaffold,
further along:

| | your scaffold | the finished model | who |
|---|---|---|---|
| datasets | 5 | 5 | machine |
| fields | **121** | **34** | you, by pruning |
| field descriptions | 5 | 34 | you, 5 came from DataHub |
| field synonyms | 3 | 20 | you, 3 came from DataHub |
| primary keys | **0** | 5 | you |
| relationships | **0** | 4 | you, from the machine's candidates |
| metrics | **0** | 7 | you |
| views | **0** | 5 | you |
| roles | 1 | 3 | you |
| denyFields | 1 | 2 | 1 from DataHub, 1 you added |
| rowFilters | **0** | 1 | you |
| `TODO` markers | 23 | 0 | |

Two things in that table are worth pausing on.

**The scaffold has no metrics at all.** Apply it and it will validate, publish,
and report healthy. Stage 8 would then return an empty metric list, because
nothing has defined `total_sales` yet.

**The finished model has fewer fields, not more.** A semantic model is meant to
be smaller than the schema. Deciding that 87 of 121 columns should not be
offered to an agent is authoring work just as much as writing a metric is.

So the file you apply below is not the file you generated, and that is the
whole point rather than a mistake. Check the difference yourself:

```bash
diff tmp/scaffold-enriched.yaml examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml | head -40
grep -c TODO tmp/scaffold-enriched.yaml
grep -c TODO examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml
```

:::tip[Want to deploy your own file instead?]
Finish your scaffold rather than using the prepared one. Set a `primary_key` on
each dataset, uncomment the correct `relationships`, and write at least one
metric. Change `metadata.name` and the model name so it does not collide, then
apply that. It is a slower demo and a much more convincing one.
:::

### Deploy

```bash
kubectl apply -f examples/stacks/eks/datahub-polaris-trino/semanticmodel.yaml
# wait for VALIDATED=True PUBLISHED=True DRIFT=False
kubectl -n semantic-system get semanticmodels -w
```

**Verify** what the operator did:

```bash
kubectl -n semantic-system describe semanticmodel tpcds-retail | grep -A8 Conditions
kubectl -n semantic-system get semanticmodel tpcds-retail \
  -o jsonpath='{range .status.bindings[*]}{.dataset}{" -> "}{.table}{"\n"}{end}'
kubectl -n semantic-system get cm sm-tpcds-retail-compiled -o jsonpath='{.metadata.labels}'
```

<details>
<summary>Expected output</summary>

```
NAME           VERSION        VALIDATED   PUBLISHED   DRIFT   AGE
tpcds-retail   4ee25537eec3   True        True        False   30s
```

Bindings show the model resolved through Polaris, and each was drift-checked
against the live engine:

```
store_sales -> polaris.osi_demo.store_sales
date_dim    -> polaris.osi_demo.date_dim
customer    -> polaris.osi_demo.customer
item        -> polaris.osi_demo.item
store       -> polaris.osi_demo.store
```

And the published artifact carries its version:

```json
{"app.kubernetes.io/managed-by":"semantic-operator",
 "semantic.ossie.io/model":"tpcds_retail_model",
 "semantic.ossie.io/version":"4ee25537eec3"}
```

`DRIFT=False` is the interesting one. The operator introspected every table
through Trino before publishing. Had a column been missing, it would have
refused to publish and left the previous version serving.

</details>

## Stage 8. Query it
**Run this stage: yes.** This is the payoff.


Discovery, then the certified number, then proof of determinism:

Everything below talks to the semantic server on 8090. Stage 4 started that
forward, but start it again if you are in a new terminal.

```bash
pkill -f 'port-forward.*8090:8090' 2>/dev/null
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &
sleep 3
```

Discovery is authenticated and governed, so pass a role here too.

```bash
curl -s localhost:8090/v1/models -H 'X-Semantic-Role: analyst' | jq
curl -s localhost:8090/v1/models/tpcds_retail_model/metrics \
  -H 'X-Semantic-Role: analyst' | jq -r '.metrics[].name'

curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' \
  | jq '{rows, requestHash, cachedResult}'
# run it twice: identical requestHash, and cachedResult true the second time
```

<details>
<summary>Expected Stage 8 output</summary>

Seven certified metric names:

```
total_sales
total_profit
total_quantity
transaction_count
customer_lifetime_value
store_productivity
sales_by_brand
```

Then the metric, twice:

```json
{"rows":[["TN","644567.241309"],["TX","1826350.375719"]],
 "requestHash":"66855481b292e9423ed73e2abccb151e","cachedResult":false}

{"rows":[["TN","644567.241309"],["TX","1826350.375719"]],
 "requestHash":"66855481b292e9423ed73e2abccb151e","cachedResult":true}
```

The numbers depend on the scale factor you loaded, so yours may differ. What
must hold is that the hash is identical across both runs and that
`cachedResult` flips to true on the second, dropping the response from a couple
of seconds to about a millisecond.

If `cachedResult` stays false, Valkey is not wired. Go back to Stage 4.

</details>

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
# analyst sees both states, tx_analyst sees one. Same request, same model.

# the exact SQL, without executing it
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/sql \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["item.i_category"]}' | jq -r '.plan.sql'
```

<details>
<summary>Expected governance output</summary>

The PII request is refused before any SQL exists:

```json
{"error":"unauthorized: role \"analyst\" may not read field \"customer.c_email_address\""}
```

`tx_analyst` sees one state, `analyst` sees both. Same request, same model:

```json
[["TX","1510391760.72"]]
[["TN","1516666718.80"],["TX","1510391760.72"]]
```

And the compiled SQL carries the row filter, so the restriction is in the
statement rather than applied to the result afterwards:

```sql
/* semantic-layer model=tpcds_retail_model version=4ee25537eec3 request=4cfc29f3... */
SELECT "item"."i_category" AS "item.i_category",
       SUM("store_sales"."ss_ext_sales_price") AS "total_sales"
FROM "polaris"."osi_demo"."store_sales" AS "store_sales"
INNER JOIN "polaris"."osi_demo"."item" AS "item"
        ON "store_sales"."ss_item_sk" = "item"."i_item_sk"
INNER JOIN "polaris"."osi_demo"."store" AS "store"
        ON "store_sales"."ss_store_sk" = "store"."s_store_sk"
WHERE ((("store"."s_state" = 'TX')))
GROUP BY 1
ORDER BY 1
LIMIT 1000
```

The `LIMIT` is the server's default row limit, applied to any request that does
not set one of its own.

</details>

**Verify from the engine's side:** the Trino web UI shows every governed query
as the engine saw it.

```bash
pkill -f 'port-forward.*8081:8080' 2>/dev/null
kubectl -n trino port-forward svc/trino 8081:8080 &
```

Open <http://localhost:8081/ui> and sign in with any username, no password. Every governed query
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

The Polaris, Trino, and data-loading scripts are intended to be idempotent. The DataHub
integration remains a preview and should be verified after every run. Port-forward
hygiene between runs:

```bash
pkill -f 'kubectl.*port-forward'
```

Known operational note: the `trino-catalog` ConfigMap may be managed by the
cluster's Terraform, so re-run `trino-catalog.sh` after any infrastructure
apply.

Numbers depend on the scale factor you loaded in Stage 3. At the default
`sf1` they are:

| Check | Value |
|---|---|
| `store_productivity` by state | TN 644567.241309, TX 1826350.375719 |
| `total_sales` as `tx_analyst` | TX 1510391760.72, one row |
| `total_sales` as `analyst` | TN 1516666718.80, TX 1510391760.72 |

The TN figure also matches the hand written SQL in Stage 3, which is the point
worth making: the governed path and a direct query agree.

