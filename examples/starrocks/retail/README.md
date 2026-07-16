# StarRocks retail example (TPC-DS subset)

A fully runnable, end-to-end example: a synthetic TPC-DS retail subset loaded as
Iceberg tables through StarRocks, a certified `SemanticModel`, and the
with/without-semantic-layer accuracy demo. This is the reference example — start
here.

- **Model:** [`model/semanticmodel.yaml`](model/semanticmodel.yaml) — datasets,
  relationships, metrics (`total_sales`, `total_profit`, `customer_lifetime_value`,
  `store_productivity`, `sales_by_brand`), governance, and BI views.
- **Data loader:** [`data/`](data/) — deterministic ~204k-row loader, no Spark.
- **NL comparison:** [`nl/`](nl/) — raw text-to-SQL vs. semantic layer, same question.
- **Benchmark:** [`bench/`](bench/) — accuracy / consistency / hallucination harness.
- **Superset:** [`superset/`](superset/README.md) — governed views in a BI tool.

> **Deploy the operator and server first.** This example assumes the
> `semantic-operator` and `semantic-server` are already installed on your
> cluster. The generic install, prerequisites, and troubleshooting live in
> [docs/DEVELOPER.md → Deploy & operate](../../../docs/DEVELOPER.md#deploy--operate).
> Everything below is specific to *this* example.

## Prerequisites for this example

| Dependency | Required? | Why |
|---|---|---|
| StarRocks (FE MySQL endpoint) + a Glue/Iceberg external catalog | **Yes** | stores and queries the demo tables |
| The `semantic-operator` + `semantic-server` running | **Yes** | reconcile the model, serve queries |
| Valkey | No | plan/result caching only; the server runs correctly without it |
| Amazon Bedrock (a Claude model enabled in your region) | Only for §6/§7 | the natural-language comparison and benchmark call an LLM |
| Apache Superset | Only for §7 (BI view demo) | everything else is `curl`/`mysql`-driven |

On your workstation: Go 1.26+, `kubectl` (kubeconfig pointing at the cluster),
`mysql` client, `jq`, AWS credentials for Glue/S3 (and Bedrock if running §6/§7).

```bash
export AWS_REGION=us-west-2
kubectl config current-context   # must be the target cluster
```

## 1. Create the StarRocks external catalog for Glue/Iceberg (once)

From any MySQL client that can reach the FE (port-forward `9030`, or a
`kubectl run` mysql pod):

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

The StarRocks BE/CN node role (or IRSA role) needs Glue read/write and S3 access
to the warehouse bucket. Writing demo tables also needs a Glue database location;
if `CREATE DATABASE` in the next step complains about a missing location, create
the Glue database once:

```bash
aws glue create-database --database-input \
  '{"Name":"osi_demo","LocationUri":"s3://<your-warehouse-bucket>/osi_demo"}'
```

## 2. Load the demo data (idempotent)

Run from your workstation against a port-forward:

```bash
kubectl -n <starrocks-ns> port-forward svc/<fe-service> 9030:9030 &
export STARROCKS_HOST=127.0.0.1
make demo-data          # creates iceberg.osi_demo.* and loads ~204k rows
```

Expected output ends with `demo data ready in iceberg.osi_demo`. Re-running skips
already-loaded tables; `go run ./examples/starrocks/retail/data -force` reloads.

## 3. Apply the SemanticModel and watch it reconcile

```bash
kubectl apply -f examples/starrocks/retail/model/semanticmodel.yaml
kubectl -n semantic-system get semanticmodels -w
```

Expected within ~30s: `VALIDATED=True PUBLISHED=True DRIFT=False` for
`tpcds-retail`. Then inspect the published artifact and bindings:

```bash
kubectl -n semantic-system get configmap sm-tpcds-retail-compiled -o jsonpath='{.metadata.labels}{"\n"}'
kubectl -n semantic-system describe semanticmodel tpcds-retail
```

**Drift demo:** drop a column or table in StarRocks and watch `DriftDetected` flip
to `True` while the previously published artifact keeps serving.

## 4. Smoke-test the serving layer (no LLM)

```bash
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &

curl -s localhost:8090/v1/models | jq
curl -s localhost:8090/v1/models/tpcds_retail_model/metrics | jq '.metrics[].name'

# A governed query → rows plus the exact compiled SQL:
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["item.i_category"],"filters":[{"field":"date_dim.d_year","op":"=","value":2001}]}' | jq
```

**Determinism / cache** — run the same request again; the request hash is
identical and the second call is a cache hit (if Valkey is configured):

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["item.i_category"],"filters":[{"field":"date_dim.d_year","op":"=","value":2001}]}' | jq '{requestHash, cachedResult, rowCount}'
```

**Governance at compile time** — an analyst asking for a PII column returns
HTTP 403; the request never compiles, so no SQL touches StarRocks:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["customer.c_email_address"]}'
```

## 5. With vs. without the semantic layer (the money shot)

The most visceral demo needs no LLM: run the naive query an analyst (or an LLM)
would hand-write next to the governed metric, in any MySQL client:

```sql
-- WITHOUT the semantic layer: naive fan-out join (confidently wrong)
SELECT s.s_state,
       SUM(ss.ss_ext_sales_price) / SUM(s.s_number_employees) AS naive_sales_per_employee
FROM iceberg.osi_demo.store_sales ss
JOIN iceberg.osi_demo.store s ON ss.ss_store_sk = s.s_store_sk
GROUP BY s.s_state;

-- WITH the semantic layer: the governed, fan-out-safe metric (correct)
SELECT * FROM semantic_views.store_productivity_by_state;
```

The naive number is wrong by orders of magnitude: joining `store` to the fact
table repeats each store's `s_number_employees` once per sales row, inflating the
denominator by the row count. The governed view (compiled by the planner) splits
the ratio into two aggregations and deduplicates headcount on the store primary
key. Put the two result grids side by side — that is the demo.

## 6. Natural-language comparison (Bedrock)

Enable a Claude model in the Bedrock console for your region, then:

```bash
export MCP_ENDPOINT=http://localhost:8090/mcp
export BEDROCK_MODEL_ID=us.anthropic.claude-sonnet-4-5-20250929-v1:0   # or your enabled model
make demo-nl QUESTION="What is our sales per employee by state?"
```

- **Raw path** — the LLM writes `store → store_sales → employees`; the join
  fans out and multiplies the denominator. A confidently wrong number with
  plausible SQL.
- **Semantic path** — the agent calls `list_metrics` → `query_metric` on the
  certified `store_productivity` metric and returns the correct number with the
  planner's exact SQL.

Run a **paraphrase** of each to show the raw path gives different answers to the
same reworded question while the semantic path is identical every time:

```bash
make demo-nl QUESTION="What is our customer lifetime value?"
make demo-nl QUESTION="Average revenue per customer over their history?"   # paraphrase
```

## 7. Superset (optional BI view demo)

Follow [`superset/README.md`](superset/README.md): connect Superset to StarRocks
over MySQL protocol, register the `semantic_views.*` datasets, and put the naive
fan-out query next to the governed view in SQL Lab. Analysts see certified
numbers and cannot re-derive a ratio metric incorrectly, because the view already
computed it through the planner.

## 8. Benchmark

```bash
make bench          # ~30 questions × 3 phrasings × 2 paths → bench/RESULTS.md
# cost/time control:
go run ./examples/starrocks/retail/bench/runner -limit 5 -out /tmp/results.md
```

[`bench/RESULTS.md`](bench/RESULTS.md) reports accuracy, cross-paraphrase
consistency, and hallucination rate per path. The seed data is fixed and
temperature is 0, so runs with the same model id are directly comparable.

## Troubleshooting

- **`external catalog "iceberg" not usable`** from `make demo-data`: create the
  catalog (§1) and check BE/CN IAM permissions for Glue/S3.
- **SemanticModel stuck without conditions:** `kubectl -n semantic-system logs
  deploy/semantic-operator-manager`.
- **`no policy for role`:** pass `X-Semantic-Role` with a role defined in
  `spec.governance` (`analyst`, `tx_analyst`, `admin` in this model).
- **MCP 4xx from `make demo-nl`:** confirm `MCP_ENDPOINT` points at `/mcp` and the
  server port-forward is alive.
- **Bedrock `AccessDeniedException`:** the model id is not enabled in the region,
  or your credentials lack `bedrock:InvokeModel`.

For operator/server install issues (pods not Ready, `/readyz` failing), see
[docs/DEVELOPER.md → Deploy & operate](../../../docs/DEVELOPER.md#deploy--operate).
