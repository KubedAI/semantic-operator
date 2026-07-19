# semantic-operator

Define a business metric once. Every AI agent, BI tool, and app then gets the same correct answer. The LLM never writes SQL.

This is a Kubernetes operator and server that turn a semantic model into safe, governed SQL. It implements the [Apache Ossie](https://ossie.apache.org/) standard.

## The problem

Point an LLM at your warehouse and it writes raw SQL. Two things go wrong.

First, it is not repeatable. Ask the same question twice and you can get two different queries, and two different numbers.

Second, it is often wrong on business logic. Ask for "customer lifetime value" or "sales per employee" and the model guesses a formula. A bad join double-counts rows, so the answer looks reasonable but is off by a lot.

Access control is a third problem. Rules applied after the query runs are easy to get wrong.

## How it works

You write your metrics, dimensions, and joins once, in a `SemanticModel` file (Apache Ossie YAML). You apply it like any Kubernetes resource.

The operator checks the model against your live database schema and compiles it. The server then answers requests three ways:

- an MCP server for AI agents
- a REST API for apps
- governed SQL views for BI tools

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

Why StarRocks: it runs star-schema joins fast over Iceberg tables on S3, it speaks the MySQL protocol so BI tools connect to it directly, and it supports views over external tables. Other engines plug in behind the `emitter.Dialect` interface. Catalog access sits behind the `catalog.Source` interface, with a Glue implementation today. See [ARCHITECTURE](docs/ARCHITECTURE.md#extension-points) for the scope guardrails.

## What a request looks like

You send a semantic request. You get one SQL statement back.

```json
{
  "model": "tpcds_retail_model",
  "metrics": ["total_sales"],
  "dimensions": ["item.i_category"],
  "filters": [{"field": "date_dim.d_year", "op": "=", "value": 2001}],
  "identity": {"role": "analyst"}
}
```

```sql
/* semantic-layer model=tpcds_retail_model version=3f2a9c1b7e4d request=7d0e1f8a... */
SELECT `item`.`i_category` AS `item.i_category`,
       SUM(`store_sales`.`ss_ext_sales_price`) AS `total_sales`
FROM `iceberg`.`osi_demo`.`store_sales` AS `store_sales`
INNER JOIN `iceberg`.`osi_demo`.`item` AS `item`
    ON `store_sales`.`ss_item_sk` = `item`.`i_item_sk`
INNER JOIN `iceberg`.`osi_demo`.`date_dim` AS `date_dim`
    ON `store_sales`.`ss_sold_date_sk` = `date_dim`.`d_date_sk`
WHERE `date_dim`.`d_year` = 2001
GROUP BY `item`.`i_category`
ORDER BY `item`.`i_category`
```

Same request, same SQL, every time. Each statement starts with a comment holding the model name, version, and request hash. That lets you trace any query in the StarRocks logs back to the request that made it.

## Quickstart

You need a Kubernetes cluster with:

- **StarRocks** (FE and BE, shared-data mode). This is the only required dependency. On EKS, deploy it with the [StarRocks on EKS stack](https://awslabs.github.io/data-on-eks/docs/datastacks/databases/starrocks-on-eks) from Data on EKS.
- **Valkey**. Optional, for caching. Enable the add-on in the same [Data on EKS](https://awslabs.github.io/data-on-eks/) stack, or leave it out.
- **A Glue/Iceberg external catalog in StarRocks**. Run `CREATE EXTERNAL CATALOG` once so StarRocks can read your Iceberg tables. That is the only manual data step. `make demo-data` creates the demo tables for you. [Steps here](examples/starrocks/retail/README.md#1-create-the-starrocks-external-catalog-for-glueiceberg-once).

Then, from a checkout:

```bash
# 1. Build and push images to ECR (region and account come from your environment)
make ecr-login docker-build docker-push \
  REGISTRY=<acct>.dkr.ecr.us-west-2.amazonaws.com

# 2. Install the operator and server.
#    valkey.addr is optional. Omit that --set line to run without caching.
helm install semantic-operator charts/semantic-operator \
  --namespace semantic-system --create-namespace \
  --set image.repository=<acct>.dkr.ecr.us-west-2.amazonaws.com/ossie-semantic-operator \
  --set image.tag=0.1.0 \
  --set starrocks.host=starrocks-fe.starrocks.svc.cluster.local \
  --set valkey.addr=valkey.valkey.svc.cluster.local:6379 \
  --set aws.region=us-west-2

# 3. Point your workstation at the cluster. The make demo-* and bench targets run
#    on your machine and reach StarRocks and the server over these port-forwards.
#    STARROCKS_HOST is required. MCP_ENDPOINT and the LLM vars are only for the
#    NL demo and benchmark.
kubectl -n <starrocks-ns> port-forward svc/<fe-service> 9030:9030 &
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

# 6. Compare raw text-to-SQL against the semantic layer (needs an LLM)
make demo-nl QUESTION="What was total sales by category in 2001?"

# 7. Run the accuracy benchmark (needs an LLM)
make bench
```

The governed views are already in StarRocks (`semantic_views.*`). Point any MySQL-protocol BI tool at them to see certified numbers.

Every endpoint, credential, and catalog name above is a Helm value or an environment variable. Nothing is hardcoded. See [values.yaml](charts/semantic-operator/values.yaml). Deploy and operate details are in [DEVELOPER](docs/DEVELOPER.md#deploy--operate). The full end-to-end for this demo, including the one-time catalog setup and troubleshooting, is in the [retail example](examples/starrocks/retail/README.md).

> **Which LLM?** The semantic layer never calls an LLM. It exposes an MCP server and a REST API, and any MCP-capable agent can drive it: the Anthropic or OpenAI APIs, Bedrock, or a self-hosted model on GPU behind vLLM, TGI, or Ollama. Only the demo and benchmark tools use Amazon Bedrock, for reproducible numbers. Point them at a different endpoint by implementing the small `Complete` and `RunToolLoop` functions in `internal/nlbench`.

## Demo: with and without the semantic layer

The retail example answers a business question two ways on the same StarRocks cluster: raw LLM text-to-SQL, and the semantic layer. It prints both queries and both results side by side. Ambiguous metrics like `customer_lifetime_value` and `store_productivity` are where raw text-to-SQL invents a formula that changes from run to run. The semantic path stays the same. The benchmark measures this across many questions and reports accuracy, consistency, and hallucination rate.

See the [retail example](examples/starrocks/retail/README.md) to run it, and [RESULTS.md](examples/starrocks/retail/bench/RESULTS.md) for results.

## Repository layout

```
api/v1alpha1/          CRD types (Apache Ossie model as Go structs)
controllers/           SemanticModel reconciler
cmd/                   manager, server, ossiectl binaries
internal/ossie/          Apache Ossie schema validation
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
