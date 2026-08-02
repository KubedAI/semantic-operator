---
title: Developing and testing
description: Clone the repository, build the binaries, run the offline test suite, and try your changes on a real cluster.
---

The whole test suite runs offline in about ten seconds. You do not need a cluster, a
database, or cloud credentials to work on the compiler, the planner, or governance. That is
deliberate, and it is the fastest way to make progress.

## Get set up

You need Go 1.26 or later. For the cluster steps you also need Docker, `kubectl`, and
`helm`.

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

## Trying a change on a cluster

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
