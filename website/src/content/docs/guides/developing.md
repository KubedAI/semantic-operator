---
title: Developing and testing
description: Clone the repository, build the binaries, run the offline test suite, and try your changes on a real cluster.
---

The whole test suite runs offline in about ten seconds. You do not need a cluster, a
database, or cloud credentials to work on the compiler, the planner, or governance. That is
deliberate, and it is the fastest way to make progress.

## Get set up

You need Go 1.26 or later. For the cluster steps you also need Docker, `kind`, `kubectl`,
and `helm`.

```bash
git clone https://github.com/KubedAI/semantic-operator.git
cd semantic-operator
make build
```

That produces three binaries in `bin/`.

| Binary | What it is |
|---|---|
| `bin/manager` | The operator. Reconciles models, publishes artifacts, creates views. |
| `bin/server` | The semantic server. Compiles and answers queries. |
| `bin/ossiectl` | The authoring tool. Generates and validates models. |

## Run the tests

```bash
make test
```

That runs static analysis and the full suite. Everything is mocked, so there is no
external dependency. While iterating on one package, run just that package.

```bash
go test ./internal/planner/ -run TestFanOut -v
```

The fastest end to end check of your own change needs no cluster at all.

```bash
go run ./cmd/ossiectl validate -f examples/retail/model/semanticmodel.yaml
```

## How the code is laid out

The package graph points one direction. Binaries depend on adapters, adapters depend on the
core, and the core depends on nothing but the API types.

```
cmd/manager        the operator binary
cmd/server         the semantic server binary
cmd/ossiectl       the authoring CLI

api/v1alpha1       CRD types, the Ossie model as Go structs
controllers        the reconcile loop
internal/ossie     structural validation, pure
internal/planner   the compiler, pure
internal/governance compile time policy, pure
internal/emitter   SQL dialects, one package per engine
internal/dbclient  engine connection interface and factory
internal/catalog   model generation sources and enrichers
internal/serving   the shared query path, plus mcp, rest, views, auth adapters
```

The core packages import no SDKs. No AWS, no database driver, no controller runtime. That
is why they have fast table driven tests, and it is the property to preserve when adding
code.

If you are wondering where something belongs, the rule is that anything which knows about a
specific engine, cloud, or catalog goes behind an interface in the adapter layer, and
everything else goes in the core.

## Regenerating after an API change

If you touch anything under `api/`, regenerate the deepcopy functions and the CRD manifest.

```bash
make generate
```

Commit the regenerated files. Continuous integration fails if they are stale.

## Trying changes on a dedicated kind cluster

Use the repository helper when you do not have a disposable cluster. It creates or reuses a
cluster named `semantic-operator-dev` and exports its context to the repository-local
`.kube/config`. It does not build application images or deploy workloads.

```bash
make kind-deploy
```

Use a different cluster name when needed. Workload helpers use the matching
`kind-<cluster-name>` context from this file.

```bash
make kind-deploy KIND_CLUSTER_NAME=semantic-operator-test
```

Deploy the local Trino engine, then build and install the semantic operator and server.
`operator-deploy` waits for an existing Trino deployment before Helm waits for server
readiness.

```bash
make trino-deploy
make operator-deploy
```

Trino uses an in-memory catalog, so restarting or redeploying its pod removes loaded retail
data. Rerun `make models-deploy` after `make trino-deploy`. To use an existing StarRocks
service, skip `trino-deploy` and pass the engine settings to `operator-deploy`.

```bash
make operator-deploy \
  KIND_ENGINE_TYPE=starrocks \
  KIND_ENGINE_HOST=starrocks.default.svc.cluster.local \
  KIND_ENGINE_PORT=9030
```

Inspect the installation or reach the `ClusterIP` server with the repository kubeconfig and
named context.

```bash
kubectl --kubeconfig .kube/config --context kind-semantic-operator-dev \
  -n semantic-system get pods
kubectl --kubeconfig .kube/config --context kind-semantic-operator-dev \
  -n semantic-system port-forward svc/semantic-operator-server 8090:8090
```

### Deploy the retail OPA policy

Deploy OPA after the semantic operator is running. Kubernetes pulls
`docker.io/openpolicyagent/opa:1.19.1`. The target installs the retail policy behind a
`ClusterIP` Service and upgrades the existing semantic server release with the `retail-opa`
provider. It uses the same `.kube/config` and named kind context as the other helpers.

```bash
make opa-deploy
```

The policy allows `demo-user` with the `analyst`, `tx_analyst`, or `admin` role to query the
retail model through REST. It verifies the provider-neutral action, model identity, request,
adapter, and server access time. The model's built-in field restrictions and row filters still
apply independently.

### Load the retail data and E2E model

Load the deterministic retail dataset into `memory.osi_demo`, verify all five table counts,
and deploy the retail model.

```bash
make models-deploy
```

The target runs the existing loader in `examples/retail/data` through a local Trino port
forward with the compact `e2e` profile. It loads 1,096 dates, 6 stores, 25 items, 100
customers, and 2,000 sales. The default `full` loader profile remains unchanged for demos
and benchmarks. The target merges an E2E-only patch into a temporary copy of the shared
retail model. The patch selects `memory.osi_demo`, writes views under
`memory.semantic_views`, and sets `governance.external.providerRef: retail-opa`. The shared
model file remains unchanged. The target waits for the model and its views to report ready.

### Prepare the complete local environment

Use the aggregate target to create or reuse the named cluster, deploy Trino and the semantic
operator, deploy OPA, load the retail data, and publish the patched model.

```bash
make e2e
```

This target only prepares the environment. It does not send semantic queries or run E2E test
assertions. Trino's memory data is lost whenever its pod restarts. The aggregate target reloads
it, or you can rerun `make models-deploy` after a standalone Trino refresh.

To test manually, forward the `ClusterIP` semantic server.

```bash
kubectl --kubeconfig .kube/config --context kind-semantic-operator-dev \
  -n semantic-system port-forward svc/semantic-operator-server 8090:8090
```

An authorized query uses the OPA-approved principal and role.

```bash
curl -sS -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: demo-user' \
  -H 'X-Semantic-Role: analyst' \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["store.s_state"]}' | jq
```

The response should contain all six store states and a nonempty
`authorizationFingerprint`. Change only the principal to inspect OPA's denial.

```bash
curl -i -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: denied-user' \
  -H 'X-Semantic-Role: analyst' \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["store.s_state"]}'
```

The denied request should return HTTP 403 and name `retail-opa`.

## Trying a change on an existing cluster

Build and push images to a registry your cluster can pull from, then install the chart.

```bash
make docker-build docker-push REGISTRY=<your-registry> TAG=dev

helm upgrade --install semantic-operator charts/semantic-operator \
  --set server.auth.allowInsecureHeaderAuth=true \
  --namespace semantic-system --create-namespace \
  --set image.manager.repository=<your-registry>/manager \
  --set image.server.repository=<your-registry>/server \
  --set image.tag=dev \
  --set engine.type=starrocks \
  --set engine.host=<engine-host>
```

Watch it come up.

```bash
kubectl -n semantic-system get pods
kubectl -n semantic-system logs -f deploy/semantic-operator-manager
```

Reach the server over a port forward. The Service is `ClusterIP` on purpose and the chart
refuses to publish it externally without an explicit override.

```bash
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090
curl -s localhost:8090/readyz
```

To iterate quickly, rebuild with a new tag and delete the pods. Deleting them forces an
immediate pull rather than waiting out an image pull backoff.

```bash
make docker-build docker-push REGISTRY=<your-registry> TAG=dev2
helm upgrade semantic-operator charts/semantic-operator -n semantic-system \
  --reuse-values --set image.tag=dev2
kubectl -n semantic-system delete pods -l app.kubernetes.io/name=semantic-operator
```

## Running the operator outside the cluster

For controller work it is often quicker to run the manager on your machine against a
cluster, so you skip the image build entirely.

```bash
export ENGINE_HOST=127.0.0.1 SQL_DIALECT=starrocks
go run ./cmd/manager
```

It uses your kubeconfig. Port forward the engine first so the host resolves.

## Before you open a pull request

```bash
make lint
make test
make helm-lint
make generate && git diff --exit-code
```

`make lint` includes a check that no chart template creates a `LoadBalancer` or `NodePort`
Service, because a publicly reachable semantic server is an unauthenticated query endpoint.

## Extending it

Adding support for new infrastructure does not mean touching the compiler.

- [Adding a query engine](/guides/adding-an-engine) covers dialects and connection clients.
- [Adding a catalog source](/guides/adding-a-catalog) covers model generation and metadata
  import.
