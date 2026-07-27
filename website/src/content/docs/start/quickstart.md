---
title: Quickstart
description: Install the operator and server, apply a model, and run your first governed query.
---

The shortest path from a checkout to a governed answer. It uses StarRocks, the reference
engine. Running on Trino changes two flags and nothing else, which is covered in
[Retail on Glue and Trino](/examples/glue-trino).

Read [Prerequisites](/examples/prerequisites) first if you have not set up a cluster and a
query engine yet. If you want the fuller version of this with a verification after every
step, go straight to [Retail on Glue and StarRocks](/examples/glue-starrocks).

## 1. Build and push the images

Images are not published yet, so build them into a registry your cluster can pull from. The
tag has to match the one you install with in the next step.

```bash
make ecr-create ecr-login docker-build docker-push \
  REGISTRY=<acct>.dkr.ecr.us-west-2.amazonaws.com TAG=0.1.0
```

## 2. Install the operator and server

Service names below match the Data on EKS StarRocks stack. Adjust them for yours. Valkey is
optional and only adds caching, so drop that line to run without it.

```bash
helm install semantic-operator charts/semantic-operator \
  --namespace semantic-system --create-namespace \
  --set image.repository=<acct>.dkr.ecr.us-west-2.amazonaws.com/semantic-operator \
  --set image.tag=0.1.0 \
  --set engine.type=starrocks \
  --set engine.host=kube-starrocks-fe-service.starrocks.svc.cluster.local \
  --set valkey.addr=valkey.valkey.svc.cluster.local:6379
```

Check that both workloads are up and the server can reach the engine.

```bash
kubectl -n semantic-system get pods
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &
curl -s -o /dev/null -w '%{http_code}\n' localhost:8090/readyz
```

A `200` means the model store has synced and the engine answered a ping.

## 3. Load the demo data

The loader creates Iceberg tables through StarRocks itself, so there is no Spark job. It is
idempotent and skips tables that already have rows.

```bash
kubectl -n starrocks port-forward svc/kube-starrocks-fe-service 9030:9030 &
export STARROCKS_HOST=127.0.0.1
make demo-data
```

This step needs an Iceberg external catalog to already exist in StarRocks. Creating it is a
one time piece of SQL, covered in
[step 1 of the retail walkthrough](/examples/glue-starrocks).

## 4. Apply a model

```bash
kubectl apply -f examples/retail/model/semanticmodel.yaml
kubectl -n semantic-system get semanticmodels -w
```

Wait for `VALIDATED=True PUBLISHED=True DRIFT=False`. That means the model was validated,
bound to real tables, checked against the live schema, and published as a versioned
artifact.

## 5. Ask a question

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' | jq
```

New York should come back as `210176.60448413`. That is a ratio spanning a join, and the
obvious hand written version of it returns about `12.54` because it counts each store's
headcount once per sale. The compiler splits the ratio and deduplicates the denominator, so
it does not make that mistake.

Run the same request again and the response carries the same `requestHash` with
`cachedResult` set to true. Identical requests compile to identical SQL.

## What you have now

Three ways to reach the same certified definitions.

Agents connect over MCP at `/mcp` and select metrics by name. Applications use REST, with
`POST /v1/models/{model}/sql` available as a dry run that returns the SQL without executing
it. BI tools read the governed views that the operator created in the engine, under
`semantic_views`, with no server involved.

Every endpoint, credential, and catalog name above is a Helm value or an environment
variable. Nothing is compiled in. See
[values.yaml](https://github.com/KubedAI/semantic-operator/blob/main/charts/semantic-operator/values.yaml)
for the full set, and [Developing and testing](/guides/developing) for deploying your own
build.

## Optional. Compare against raw text to SQL

With an LLM endpoint available, ask the same business question both ways and watch them
disagree.

```bash
export MCP_ENDPOINT=http://localhost:8090/mcp
export BEDROCK_MODEL_ID=<your enabled model id>
make demo-nl QUESTION="What is our sales per employee by state?"
make bench
```

The semantic layer never calls an LLM itself. It exposes MCP and REST, and any MCP capable
agent can drive it, whether that is the Anthropic or OpenAI APIs, Bedrock, or a self hosted
model behind vLLM, TGI, or Ollama. Only the demo and benchmark tools use Bedrock, so the
published numbers are reproducible.

## Next

[Retail on Glue and StarRocks](/examples/glue-starrocks) is this same path with a
verification after every step, plus governance and BI checks. [How it works](/architecture)
explains what the compiler is actually doing.
