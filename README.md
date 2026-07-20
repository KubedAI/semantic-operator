# Semantic Operator

[![CI](https://github.com/KubedAI/semantic-operator/actions/workflows/ci.yaml/badge.svg)](https://github.com/KubedAI/semantic-operator/actions/workflows/ci.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/KubedAI/semantic-operator)](https://goreportcard.com/report/github.com/KubedAI/semantic-operator)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

A Kubernetes operator and server that run an [Apache Ossie](https://ossie.apache.org/) (incubating) semantic layer on your existing data platform. You define each business metric once, in a `SemanticModel` resource. The operator validates the model, checks its bindings against the live database schema, blocks publication on drift, and applies row and column access rules at compile time. A deterministic compiler turns every request into exactly one governed SQL statement, so AI agents (over MCP), apps (over REST), and BI tools (over SQL views) all compute the metric the same way. The LLM only selects certified metrics and dimensions. It never writes SQL. Query engines and catalogs are extension points, not assumptions.

## The problem

Point an LLM at your warehouse and it writes raw SQL. Three things go wrong.

First, it is not repeatable. Ask the same question twice and you can get two different queries, and two different numbers.

Second, it is often wrong on business logic. Ask for "customer lifetime value" or "sales per employee" and the model guesses a formula. A bad join double-counts rows, so the answer looks reasonable but is off by a lot.

Third, access control. Rules applied after the query runs are easy to get wrong, and easy to skip.

## How it works

You write your metrics, dimensions, and joins once, in a `SemanticModel` file (Apache Ossie YAML). You apply it like any Kubernetes resource.

The operator checks the model against your live database schema and compiles it. One compiler then serves it three ways:

- an MCP server for AI agents
- a REST API for apps
- governed SQL views for BI tools (created by the operator in the query engine)

An agent picks a certified metric and some dimensions. It does not write SQL. A compiler turns that choice into one SQL statement and runs it. The same request always gives the same SQL. Access rules are applied while the SQL is built, so a request a user is not allowed to make fails before it reaches the database.

> Naming: this implements Apache Ossie (incubating), the metric standard once called Open Semantic Interchange (OSI). Identifiers use the `ossie` name throughout: the `semantic.ossie.io` API group, the `spec.ossie` block, and the `ossiectl` CLI.

## Architecture

![Architecture overview](docs/img/architecture-overview.png)
<!-- Diagram source: docs/diagrams/architecture-overview.mmd -->

## Documentation

- [OVERVIEW](docs/OVERVIEW.md). What this is and who uses it.
- [AUTHORING](docs/AUTHORING.md). Write a model, starting from a generated template.
- [ARCHITECTURE](docs/ARCHITECTURE.md). How the operator, server, and compiler work.
- [DEVELOPER](docs/DEVELOPER.md). Code layout, and how to deploy and operate it.
- [EXTENDING-ENGINES](docs/EXTENDING-ENGINES.md). Add a query engine such as Trino, ClickHouse, or DuckDB.
- [ROADMAP](docs/ROADMAP.md). What is planned next.

Examples are in [examples/](examples/README.md). Start with [starrocks/retail](examples/starrocks/retail/README.md), a full runnable demo. [starrocks/flights](examples/starrocks/flights/README.md) is a second, model-only example adapted from the [Apache Ossie flights model](https://github.com/apache/ossie/blob/main/examples/flights.yaml).

## Components

| Component | Binary | What it does |
|---|---|---|
| Operator | `cmd/manager` | Reconciles `SemanticModel` resources. Validates the model, checks it against the live database, compiles it, publishes the result, and creates the governed views. |
| Server | `cmd/server` | Stateless. Answers queries. Runs the compiler, applies governance, and hosts the MCP and REST APIs. Caches in Valkey when one is configured. |
| CLI (`ossiectl`) | `cmd/ossiectl` | Validate a model offline, generate a model template from a Glue database, and convert between Ossie YAML and the resource format. |

The query engine is pluggable, in two halves selected together by one Helm value (`engine.type`): the SQL dialect (`emitter.Dialect`) and the connection client (`internal/dbclient`). Two engines ship today — **StarRocks** (MySQL protocol; fast star-schema joins over Iceberg on S3, views over external tables, BI tools connect natively) and **Trino** (HTTP protocol; views live in a catalog, e.g. `iceberg.semantic_views`). Engines such as ClickHouse, DuckDB, or Redshift and catalogs such as Unity or Polaris slot in without touching the compiler — [EXTENDING-ENGINES](docs/EXTENDING-ENGINES.md) walks through it, with the Trino implementation as the worked example. See [ARCHITECTURE](docs/ARCHITECTURE.md#extension-points) for the scope guardrails.

## What a request looks like

You send a semantic request. You get one SQL statement back. The caller's
identity travels in the `X-Semantic-Role` header (never in the body), so an
authenticating proxy in front of the server controls it.

```bash
curl -X POST $SERVER/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' -d '
{
  "metrics": ["total_sales"],
  "dimensions": ["item.i_category"],
  "filters": [{"field": "date_dim.d_year", "op": "=", "value": 2001}]
}'
```

```sql
/* semantic-layer model=tpcds_retail_model version=e190c3e42461 request=c1e2386104dddc43 */
SELECT `item`.`i_category` AS `item.i_category`,
       SUM(`store_sales`.`ss_ext_sales_price`) AS `total_sales`
FROM `iceberg`.`osi_demo`.`store_sales` AS `store_sales`
INNER JOIN `iceberg`.`osi_demo`.`date_dim` AS `date_dim` ON `store_sales`.`ss_sold_date_sk` = `date_dim`.`d_date_sk`
INNER JOIN `iceberg`.`osi_demo`.`item` AS `item` ON `store_sales`.`ss_item_sk` = `item`.`i_item_sk`
WHERE (`date_dim`.`d_year` = 2001)
GROUP BY 1
ORDER BY 1
```

This SQL is the planner's verbatim output for this request (joins follow the
model's relationship order; dimensions group and order by ordinal). Same
request, same SQL, every time — the response carries it alongside the rows.
Each statement starts with a comment holding the model name, version, and
request hash. That lets you trace any query in the engine's query log back to
the request that made it. `POST .../sql` returns the plan without executing it.

## Quickstart

The quickstart below uses StarRocks, the reference engine. To run on Trino
instead, follow [examples/trino/retail](examples/trino/retail/README.md) —
the install differs only in two `--set engine.*` flags.

You need a Kubernetes cluster with:

- **StarRocks** (FE and BE, shared-data mode). This is the only required dependency. On EKS, deploy it with the [StarRocks on EKS stack](https://awslabs.github.io/data-on-eks/docs/datastacks/databases/starrocks-on-eks) from Data on EKS.
- **Valkey**. Optional, for caching. Enable the add-on in the same [Data on EKS](https://awslabs.github.io/data-on-eks/) stack, or leave it out.
- **A Glue/Iceberg external catalog in StarRocks**. Run `CREATE EXTERNAL CATALOG` once so StarRocks can read your Iceberg tables. That is the only manual data step. `make demo-data` creates the demo tables for you. [Steps here](examples/starrocks/retail/README.md#1-create-the-starrocks-external-catalog-for-glueiceberg-once).

Then, from a checkout:

```bash
# 1. Build and push images to ECR. TAG must match step 2's image.tag
#    (without TAG=, the Makefile tags images with the git SHA instead).
make ecr-create ecr-login docker-build docker-push \
  REGISTRY=<acct>.dkr.ecr.us-west-2.amazonaws.com TAG=0.1.0

# 2. Install the operator and server.
#    valkey.addr is optional. Omit that --set line to run without caching.
#    Service names below match the Data on EKS StarRocks stack; adjust for yours.
helm install semantic-operator charts/semantic-operator \
  --namespace semantic-system --create-namespace \
  --set image.repository=<acct>.dkr.ecr.us-west-2.amazonaws.com/semantic-operator \
  --set image.tag=0.1.0 \
  --set starrocks.host=kube-starrocks-fe-service.starrocks.svc.cluster.local \
  --set valkey.addr=valkey.valkey.svc.cluster.local:6379 \
  --set aws.region=us-west-2

# 3. Point your workstation at the cluster. The make demo-* and bench targets run
#    on your machine and reach StarRocks and the server over these port-forwards.
#    STARROCKS_HOST is required. MCP_ENDPOINT and the LLM vars are only for the
#    NL demo and benchmark.
kubectl -n starrocks port-forward svc/kube-starrocks-fe-service 9030:9030 &
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &
export STARROCKS_HOST=127.0.0.1
export MCP_ENDPOINT=http://localhost:8090/mcp
export AWS_REGION=us-west-2
export BEDROCK_MODEL_ID=<your enabled Bedrock model id>   # NL demo and bench only

# 4. Load the demo data (creates Iceberg tables in Glue through StarRocks, idempotent)
make demo-data

# 5. Apply the demo semantic model (Apache Ossie TPC-DS subset)
kubectl apply -f examples/starrocks/retail/model/semanticmodel.yaml
kubectl get semanticmodels -n semantic-system   # wait for Validated, Published, Drift=False

# 6. Compare raw text-to-SQL against the semantic layer (needs an LLM).
#    This question hits a fan-out join: raw text-to-SQL inflates the employee
#    denominator by the sales row count and is wrong by ~4 orders of magnitude;
#    the semantic layer compiles the certified fan-out-safe ratio.
make demo-nl QUESTION="What is our sales per employee by state?"

# 7. Run the accuracy benchmark (needs an LLM)
make bench
```

The governed views are already in StarRocks (`semantic_views.*`). Point any MySQL-protocol BI tool at them to see certified numbers.

Every endpoint, credential, and catalog name above is a Helm value or an environment variable. Nothing is hardcoded. See [values.yaml](charts/semantic-operator/values.yaml). Deploy and operate details are in [DEVELOPER](docs/DEVELOPER.md#deploy--operate). The full end-to-end for this demo, including the one-time catalog setup and troubleshooting, is in the [retail example](examples/starrocks/retail/README.md).

> **Which LLM?** The semantic layer never calls an LLM. It exposes an MCP server and a REST API, and any MCP-capable agent can drive it: the Anthropic or OpenAI APIs, Bedrock, or a self-hosted model on GPU behind vLLM, TGI, or Ollama. Only the demo and benchmark tools use Amazon Bedrock, for reproducible numbers. Point them at a different endpoint by implementing the small `Complete` and `RunToolLoop` functions in `internal/nlbench`.

## Demo: with and without the semantic layer

The retail example answers a business question two ways on the same StarRocks cluster: raw LLM text-to-SQL, and the semantic layer. It prints both queries and both results side by side. Ambiguous metrics like `customer_lifetime_value` and `store_productivity` are where raw text-to-SQL invents a formula that changes from run to run. The semantic path stays the same. The benchmark measures this across many questions and reports accuracy, consistency, and hallucination rate.

Measured on a live EKS deployment (StarRocks 4.1, Iceberg on Glue, Claude Sonnet 4.5 on Bedrock, temperature 0):

| Question | Without semantic layer | With semantic layer |
|---|---|---|
| "Sales per employee by state?" (NY) | **$12.54** — the fan-out join repeats each store's headcount once per sales row | **$210,176.60** — matches hand-computed ground truth exactly |
| "Customer lifetime value?" | A 2000-row per-customer dump **including email addresses (PII)** | **$157,891.20** — the certified metric, one number |
| Paraphrase: "Average revenue per customer?" | **$154,705.51** — a different formula this time (silently dropped unattributed sales) | **$157,891.20** — identical SQL, identical request hash |

The raw SQL always *looks* plausible. That is the problem. The benchmark quantifies it — 30 questions × 3 phrasings × both paths, Claude Sonnet 4.5 on Bedrock, temperature 0:

| Path | Accuracy | Paraphrase consistency | Hallucinations | Wrong answers |
|---|---|---|---|---|
| Raw text-to-SQL | 62/90 (69%) | 19/30 (63%) | 0 | 28 |
| Semantic layer | **87/90 (97%)** | **27/30 (90%)** | 0 | 3 |

Every raw-path miss executed without error and returned a confidently wrong number — the raw path failed all 12 sales-per-employee runs and 10 of 12 customer-lifetime-value runs. The semantic layer's three misses were single-paraphrase metric-selection slips by the agent, never the planner. See the [retail example](examples/starrocks/retail/README.md) to run it, and [RESULTS.md](examples/starrocks/retail/bench/RESULTS.md) for per-question verdicts.

## Project status

Alpha. The API is `semantic.ossie.io/v1alpha1` and may change before v1beta1.
The planner's supported subset, the governance model, and the compiled-artifact
format are specified in [ARCHITECTURE](docs/ARCHITECTURE.md). StarRocks is the
reference engine; the extension interfaces are stable enough to build against.

## Contributing

Start with [DEVELOPER](docs/DEVELOPER.md) for the code layout and the offline
test loop (`make test` needs no cluster), and [ROADMAP](docs/ROADMAP.md) for
prioritized work. New engine dialects, catalog sources, and a local `kind`
quickstart are the best first contributions, and
[EXTENDING-ENGINES](docs/EXTENDING-ENGINES.md) walks through the first one.

## Repository layout

```
api/v1alpha1/          CRD types (Apache Ossie model as Go structs)
controllers/           SemanticModel reconciler
cmd/                   manager, server, ossiectl binaries
internal/ossie/        Apache Ossie schema validation
internal/planner/      semantic request -> logical plan -> SQL; expr/ grammar
internal/governance/   compile-time row/column/metric policies
internal/emitter/      Dialect interface; starrocks/ implementation
internal/catalog/      Source interface; glue/ implementation; derive + template
internal/cache/        Valkey plan + result caches (optional)
internal/serving/      shared query service; mcp/, rest/, views/ adapters
internal/starrocks/    MySQL-protocol client, schema introspection
internal/nlbench/      NL comparison and benchmark support
internal/observability/ tracing + metrics
charts/semantic-operator/  Helm chart (crds/, templates/, values.yaml)
examples/              use cases by engine: starrocks/retail, starrocks/flights
hack/                  build-time tool dependencies
docs/                  OVERVIEW, AUTHORING, ARCHITECTURE, DEVELOPER, EXTENDING-ENGINES, ROADMAP
```

## License

Apache-2.0
