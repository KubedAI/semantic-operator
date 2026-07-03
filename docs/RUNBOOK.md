# Runbook: compile, deploy, and test end to end

Target: an existing EKS cluster (example name `spark-on-eks`, us-west-2) with
StarRocks, Valkey, and Superset already running. Every step is copy-paste;
values in angle brackets are yours.

## 0. Prerequisites

On your workstation: Go 1.24+, Docker, kubectl (kubeconfig pointing at the
cluster), Helm 3, AWS CLI with credentials for ECR, Glue, S3, and Bedrock in
your account.

```bash
export AWS_REGION=us-west-2
export ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
export REGISTRY=$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com
kubectl config current-context   # must be the target cluster
```

## 1. Compile and test locally

```bash
make test     # go vet + all unit and smoke tests
make build    # bin/manager, bin/server, bin/osictl
go run ./cmd/osictl validate -f demo/model/semanticmodel.yaml
```

Expected: tests pass; osictl prints `OK ... (model tpcds_retail_model, version <hash>)`.

## 2. Build and push images

```bash
make ecr-create ecr-login AWS_REGION=$AWS_REGION REGISTRY=$REGISTRY
make docker-build docker-push REGISTRY=$REGISTRY TAG=0.1.0
```

EKS nodes on x86 need `PLATFORM=linux/amd64` (the default); Graviton nodes
need `PLATFORM=linux/arm64`.

## 3. Verify the cluster prerequisites

```bash
# StarRocks FE service (adjust namespace to your install)
kubectl get svc -A | grep -iE 'starrocks|fe-service'
# Valkey
kubectl get svc -A | grep -iE 'valkey|redis'
# Superset
kubectl get svc -A | grep -i superset
```

Record the DNS names, e.g.:

```bash
export SR_FE=starrocks-fe-service.starrocks.svc.cluster.local
export VALKEY=valkey-primary.valkey.svc.cluster.local:6379
```

If any of the three is missing, install it first (StarRocks via the
starrocks-kubernetes-operator in shared-data mode, Valkey via a standard
chart, Superset via its official chart); this project consumes them and does
not install them.

## 4. Create the StarRocks external catalog for Glue/Iceberg (once)

From any MySQL client that can reach the FE (e.g. `kubectl run` a mysql pod,
or port-forward 9030):

```sql
CREATE EXTERNAL CATALOG iceberg
PROPERTIES (
  "type" = "iceberg",
  "iceberg.catalog.type" = "glue",
  "aws.glue.use_instance_profile" = "true",
  "aws.glue.region" = "us-west-2",
  "aws.s3.use_instance_profile" = "true",
  "aws.s3.region" = "us-west-2"
);
SHOW DATABASES FROM iceberg;   -- sanity check
```

The StarRocks BE/CN node role (or IRSA role) needs Glue read/write and S3
access to the warehouse bucket. Writing demo tables also requires a Glue
database location or an explicit S3 path; if `CREATE DATABASE` in step 6
complains about a missing location, create the Glue database once with:

```bash
aws glue create-database --database-input \
  '{"Name":"osi_demo","LocationUri":"s3://<your-warehouse-bucket>/osi_demo"}'
```

## 5. Install the semantic operator

```bash
helm upgrade --install semantic-operator charts/semantic-operator \
  --namespace semantic-system --create-namespace \
  --set image.repository=$REGISTRY/osi-semantic-operator \
  --set image.tag=0.1.0 \
  --set starrocks.host=$SR_FE \
  --set valkey.addr=$VALKEY \
  --set aws.region=$AWS_REGION
# If StarRocks has a root password, first:
#   kubectl -n semantic-system create secret generic starrocks-auth --from-literal=password=<pw>
# and add: --set starrocks.passwordSecret.name=starrocks-auth

kubectl -n semantic-system get pods
```

Expected: `semantic-operator-manager` and two `semantic-operator-server`
pods Ready. If the server is not Ready, its `/readyz` pings StarRocks;
`kubectl logs` will say why.

## 6. Load the demo data (idempotent)

Run from your workstation against a port-forward, or as an in-cluster job.
Port-forward is simplest:

```bash
kubectl -n <starrocks-ns> port-forward svc/<fe-service> 9030:9030 &
export STARROCKS_HOST=127.0.0.1
make demo-data          # creates iceberg.osi_demo.* and loads ~204k rows
```

Expected output ends with `demo data ready in iceberg.osi_demo`. Re-running
skips loaded tables. `-force` reloads.

## 7. Apply the SemanticModel and watch it reconcile

```bash
kubectl apply -f demo/model/semanticmodel.yaml
kubectl -n semantic-system get semanticmodels -w
```

Expected within ~30s: `VALIDATED=True PUBLISHED=True DRIFT=False`. Then:

```bash
kubectl -n semantic-system get configmap sm-tpcds-retail-compiled -o jsonpath='{.metadata.labels}'
kubectl -n semantic-system describe semanticmodel tpcds-retail   # conditions and bindings
```

Drift demo: drop a column or table in StarRocks and watch `DriftDetected`
flip to True while the old artifact keeps serving.

## 8. Smoke-test the serving layer

```bash
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &

curl -s localhost:8090/v1/models | jq
curl -s localhost:8090/v1/models/tpcds_retail_model/metrics | jq '.metrics[].name'
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["item.i_category"],"filters":[{"field":"date_dim.d_year","op":"=","value":2001}]}' | jq
```

Expected: rows plus the emitted SQL. Governance check (must return 403):

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["customer.c_email_address"]}' -w '%{http_code}\n'
```

## 9. Run the natural-language comparison (Bedrock)

Enable a Claude model in the Bedrock console for your region, then:

```bash
export MCP_ENDPOINT=http://localhost:8090/mcp
export BEDROCK_MODEL_ID=us.anthropic.claude-sonnet-4-5-20250929-v1:0   # or your enabled model
make demo-nl QUESTION="What is our sales per employee by state?"
```

Expected: the raw path writes a join that inflates the employee denominator
(confidently wrong numbers); the semantic path calls `list_metrics`,
`list_dimensions`, `query_metric` and returns the certified figures with the
planner's SQL. Also try "What is our CLV?" and paraphrases of both.

## 10. Superset dashboard

Follow [demo/superset/README.md](../demo/superset/README.md): add the
StarRocks connection, register the `semantic_views.*` datasets, build the
comparison dashboard, and put the naive fan-out query side by side with the
governed view in SQL Lab.

## 11. Run the benchmark

```bash
make bench          # ~90 LLM phrasings x 2 paths; writes bench/RESULTS.md
# cost/time control: go run ./bench/runner -limit 5 -out /tmp/results.md
```

Expected: a markdown report with accuracy, paraphrase consistency, and
hallucination rate per path. The demo data seed is fixed and temperature is
0, so reruns with the same model id are comparable.

## Troubleshooting

- Server pod not Ready: `/readyz` failing means StarRocks is unreachable
  from the pod; check `starrocks.host` and network policies.
- `external catalog "iceberg" not usable` from demo-data: create the catalog
  (step 4) and check BE/CN IAM permissions for Glue/S3.
- SemanticModel stuck without conditions: `kubectl -n semantic-system logs
  deploy/semantic-operator-manager`.
- `no policy for role`: pass `X-Semantic-Role` with a role defined in
  `spec.governance` (analyst, tx_analyst, admin in the demo).
- MCP 4xx from demo/nl: confirm `MCP_ENDPOINT` points at `/mcp` and the
  port-forward is alive.
- Bedrock `AccessDeniedException`: the model id is not enabled in the region
  or your credentials lack `bedrock:InvokeModel`.
