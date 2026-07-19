# Developer guide

How this codebase is organized, how the pieces depend on each other, where
engine-specific code lives, what the Helm chart deploys, and how to extend it.
Read this first if you are going to change code.

**Related docs.** [OVERVIEW.md](OVERVIEW.md) covers what and why, plus role-based
onboarding. [ARCHITECTURE.md](ARCHITECTURE.md) covers the runtime request
lifecycle and compile-time governance in depth.
[EXTENDING-ENGINES.md](EXTENDING-ENGINES.md) walks through adding Trino,
ClickHouse, or DuckDB. [ROADMAP.md](ROADMAP.md) lists planned work. Installing
and operating the two binaries is [Deploy & operate](#deploy--operate) below.
Running a specific use case end to end lives in that example's README (start with
[examples/starrocks/retail](../examples/starrocks/retail/README.md)).

---

## Mental model in 60 seconds

Think of it as a compiler with a control plane, not a query service.

- A user authors a `SemanticModel` resource (Apache Ossie YAML: datasets, fields,
  metrics, relationships, governance).
- The operator (`manager` binary) is the control plane. It validates the model,
  drift-checks its physical bindings against the live warehouse, compiles it to a
  versioned artifact (a ConfigMap), and creates governed views.
- The semantic server (`server` binary) is the data plane. It is stateless. It
  watches those artifacts and answers requests by compiling each request to
  exactly one SQL statement. Same request, same SQL, every time. An LLM only picks
  certified metrics and dimensions. It never writes SQL.
- Everything engine-specific (how to render and run SQL) sits behind two
  interfaces. Everything else, the model, the planner, and governance, is
  engine-agnostic.

Two binaries, one shared core library, talking through the Kubernetes API.

---

## The dependency layering

The package graph points one direction. Binaries depend on adapters, adapters
depend on the domain core, the core depends on `api/` types, and nothing depends
back upward. This discipline keeps the planner pure and the engines pluggable.

```
                 cmd/manager (operator)          cmd/server (semantic server)
                        │                                   │
        ┌───────────────┴───────────┐         ┌─────────────┴──────────────┐
        ▼                           ▼         ▼                            ▼
   controllers/               internal/starrocks/   internal/serving/     internal/cache/
   (reconciler)               (SQL client)          (mcp, rest, views)    (Valkey, optional)
        │                           │                     │
        └───────────────┬──────────┴──────────┬──────────┘
                        ▼                      ▼
                 ADAPTERS (interfaces): emitter.Dialect · catalog.Source
                        │                      │
                        ▼                      ▼
        ┌──────────────────── DOMAIN CORE (pure, offline-testable) ───────────────────┐
        │  internal/planner  ·  internal/governance  ·  internal/ossie  ·  api/v1alpha1  │
        └─────────────────────────────────────────────────────────────────────────────┘
```

The domain core imports no SDKs. No AWS, no MySQL driver, no controller-runtime.
That is why `internal/planner`, `internal/ossie`, and `internal/governance` have
fast, offline, table-driven unit tests. The engine and cloud coupling lives
entirely in the adapter layer.

---

## Repository map

Grouped by layer, binaries at the top, types at the bottom.

### Binaries (`cmd/`)

| Path | Binary | Role |
|---|---|---|
| `cmd/manager/` | `manager` | The operator. Reconciles `SemanticModel` resources, publishes compiled-model ConfigMaps, and creates governed views. Uses controller-runtime. |
| `cmd/server/` | `server` | The stateless semantic server. Watches ConfigMaps. Hosts the planner, governance, the MCP and REST adapters, and the caches. Scales horizontally. |
| `cmd/ossiectl/` | `ossiectl` | CLI. Offline model validation, model-template generation from Glue, and resource round-trip. No cluster required for validate. |

Each `main.go` is thin. Read env, construct clients, wire interfaces, run. All
configuration is env or flags. Nothing is compiled in.

### Domain core (`api/` and `internal/`, engine-agnostic)

| Path | Contents |
|---|---|
| `api/v1alpha1/` | The CRD types (Apache Ossie model as Go structs). `semanticmodel_types.go` (spec and status), `helpers.go` (lookups, dialect preference), `ai_context.go` (LLM grounding fields), `groupversion_info.go`, `zz_generated.deepcopy.go` (generated, do not hand-edit). |
| `internal/ossie/` | `validate.go`. Apache Ossie schema validation, the structural checks before compile. |
| `internal/planner/` | The compiler. `model.go` (CompiledModel), `plan.go` (semantic request to logical plan), `plan_sql.go` (logical plan to SQL via a Dialect), `expr/expr.go` (expression handling). This is the heart, and it is pure. |
| `internal/governance/` | `governance.go`. Compile-time row and column policies. Applied inside the planner before SQL is emitted, so an unauthorized request fails to compile. |

### Adapter layer, the extension seams (`internal/`)

| Path | Interface and role | Ships |
|---|---|---|
| `internal/emitter/` | `emitter.Dialect`. SQL rendering atoms (quoting, literals, DATE_TRUNC, null-safe equality). Registry keyed by name. | — |
| `internal/emitter/starrocks/` | The StarRocks dialect implementation. | ✅ |
| `internal/catalog/` | `catalog.Source`. Schema discovery for derivation and drift. `derive.go` and `template.go` turn physical tables into dataset stubs and a full model scaffold, and infer candidate relationships. | — |
| `internal/catalog/glue/` | AWS Glue implementation of `catalog.Source` (IRSA-authenticated). | ✅ |
| `internal/starrocks/` | `client.go`. MySQL-protocol client with `Query`, `Exec`, `DescribeTable`, `ShowCreateTable`. The runtime DB surface. | ✅ |
| `internal/cache/` | `cache.go`. Valkey (Redis-protocol) plan and result caches. A nil cache is a valid no-op, so caching is a pure optimization. | ✅ |
| `internal/serving/` | The shared query path used by both adapters. `service.go` (plan, execute, report; defines `QueryExecutor`), `store.go` (in-memory compiled-model store fed by the ConfigMap watcher). | ✅ |
| `internal/serving/mcp/` | `mcp.go`. MCP streamable-HTTP adapter, what LLM agents call. | ✅ |
| `internal/serving/rest/` | `rest.go`. REST adapter. | ✅ |
| `internal/serving/views/` | `views.go`. Governed StarRocks view publisher (`Executor` is `Exec`). | ✅ |
| `internal/observability/` | `observability.go`. Tracing and metrics wiring. | ✅ |

### Control plane (`controllers/`)

`semanticmodel_controller.go` is the reconciler. It validates, then binds and
drift-checks through the `StarRocksClient` interface (`DescribeTable`), then
compiles, writes the ConfigMap, and publishes views. It defines the narrow
interface it needs instead of importing the concrete client.

### Everything else

| Path | What |
|---|---|
| `charts/semantic-operator/` | Helm chart. `crds/`, `templates/` (manager and server Deployments, RBAC, ServiceAccounts), `values.yaml`. |
| `examples/` | Use cases grouped by engine. `starrocks/retail/` is the runnable reference example (`data/` loader, `model/` CR, `nl/` comparison, `bench/` harness). `starrocks/flights/` is a second, model-only Glue-bound example. See [examples/README.md](../examples/README.md). |
| `hack/` | `tools.go`, the build-time tool dependencies. |
| `docs/` | These documents. |

---

## The two binaries and how they communicate

They do not call each other. They coordinate through the Kubernetes API. That is
what makes the server stateless and horizontally scalable.

```
   SemanticModel CR (user authors)
            │  kubectl apply
            ▼
   ┌─────────────────┐   validate → drift-check (DescribeTable) → compile
   │  manager        │   ───────────────────────────────────────────────►  StarRocks (introspect)
   │  (operator)     │
   └────────┬────────┘   writes                              creates governed views
            │  compiled-model ConfigMap (versioned artifact) │
            ▼                                                 ▼
   ┌─────────────────┐   watches ConfigMaps                StarRocks (CREATE VIEW)
   │  server × N     │   plan (governed, cached) → execute (cached) → report
   │  (stateless)    │   ─────────────────────────────────────────────►  StarRocks (run one SQL)
   └────────┬────────┘
            │  MCP / REST
            ▼
     LLM agent / BI tool  (picks certified metrics; never writes SQL)
```

- `manager` imports `controllers`, `internal/starrocks`, `internal/emitter`, and
  `internal/catalog` (for derivation via `ossiectl` and reconcile). It is the only
  writer of compiled artifacts.
- `server` imports `internal/serving`, `internal/cache`, `internal/starrocks`, and
  `internal/emitter`. It is read-only against models (it consumes ConfigMaps) and
  read-only against the warehouse except for cached `SELECT`s.

---

## Engine-specific vs engine-agnostic code

This is the question that matters most for portability. All StarRocks coupling
lives in exactly two packages. Everything else is engine-neutral.

| Concern | StarRocks today | Where a new engine (Trino, DuckDB, …) goes |
|---|---|---|
| SQL rendering (quoting, literals, DATE_TRUNC, null-safe equality) | `internal/emitter/starrocks/` | `internal/emitter/<engine>/` implementing `emitter.Dialect` |
| Runtime DB client (`Query`, `Exec`, `DescribeTable`) | `internal/starrocks/` | `internal/<engine>/`, a `database/sql` wrapper of the same shape |
| Catalog and schema discovery | `internal/catalog/glue/` (Glue) | `internal/catalog/<source>/` implementing `catalog.Source`, for example over `information_schema` |
| Which engine is active | `SQL_DIALECT=starrocks` (env) selects the dialect at runtime. The client is currently constructed directly in both `main.go` files. | Add a client factory keyed by the same `SQL_DIALECT` variable |

The planner, governance, validator, serving adapters, caching, and CRD types are
untouched when you add an engine. That is by design. See
[EXTENDING-ENGINES.md](EXTENDING-ENGINES.md) for the full Tier 1 walkthrough
(Trino, ClickHouse, DuckDB), including the per-method dialect deltas and the small
client-factory refactor.

> Storage format (Iceberg) is not in this code at all. The planner emits
> `catalog.database.table` references and hands the SQL to the engine. Whether
> those tables are Iceberg, Delta, or Hive is the engine's external-catalog
> concern. Grep confirms it. There is no snapshot, manifest, or parquet logic
> anywhere. `iceberg` in the tree is only a configurable catalog-name string.

---

## Deployment topology and the Valkey question

The Helm chart deploys two Deployments (manager, server) plus the CRD, RBAC, and
two ServiceAccounts. It does not deploy a database or a cache. It wires the
workloads to infrastructure you already run.

```
        Helm chart: semantic-operator
        ├── manager Deployment   (1 replica, leader-elected)     ← operator
        ├── server  Deployment   (N replicas, ClusterIP Service) ← semantic server
        ├── CRD  semanticmodels.semantic.ossie.io
        ├── RBAC (Role/Binding) + 2 ServiceAccounts (IRSA-annotatable)
        │
        ├── depends on ── StarRocks   (EXISTING, required)   FE MySQL endpoint
        ├── depends on ── Valkey      (EXISTING, OPTIONAL)   Redis-protocol
        └── depends on ── AWS Glue    (via IRSA on manager SA)
```

**Does the deployment rely on Valkey? No. Valkey is optional.** This is true in
both layers.

- Code: `cache.New` returns a nil `*Cache` when the address is empty, and a nil
  cache is a valid no-op. Callers never branch on "caching enabled." The server
  logs `VALKEY_ADDR not set; running without plan/result caches` and runs
  normally. A cache miss or an unreachable Valkey degrades to recompute-and-query,
  never to an error.
- Chart: `values.yaml` ships `valkey.addr: ""` by default, and
  `server-deployment.yaml` only injects the `VALKEY_*` env vars when
  `.Values.valkey.addr` is set. Leave it empty and the server has no cache. It
  still answers every request correctly. It just recompiles and re-queries each
  time.

Set `valkey.addr` to an existing Valkey or ElastiCache endpoint to turn caching
on. There is no Valkey subchart and no bundled Redis. You point at your own. The
same is true for StarRocks. It is an existing cluster you already run, not
something the chart stands up.

**What each dependency is for:**

| Dependency | Required? | Used by | Purpose |
|---|---|---|---|
| StarRocks (FE MySQL endpoint) | Yes | both binaries | introspection (manager) and query execution (server) |
| Valkey (Redis protocol) | No | server only | plan and result caches, a latency optimization |
| AWS Glue | Only for catalog derivation or drift from Glue | manager | reads physical schema. IRSA on the manager SA supplies creds |

Everything is a Helm value or env var. No account, endpoint, or region is
compiled into the images. See `charts/semantic-operator/values.yaml` and
[Deploy & operate](#deploy--operate).

---

## Deploy & operate

This is the engine- and example-agnostic install. Build the images, deploy the
two workloads, and point them at your existing StarRocks (and optionally Valkey).
Running a specific use case on top, such as loading data, applying a model, or the
accuracy demo, lives in that example's README (start with
[examples/starrocks/retail](../examples/starrocks/retail/README.md)).

**Prerequisites.** An EKS or any Kubernetes cluster with an existing StarRocks
cluster reachable over the FE MySQL endpoint, fronting Iceberg through a Glue
external catalog. Valkey is optional (see the topology table above). Any
MySQL-protocol BI tool can read the governed views with no extra components. On
your workstation you need Go 1.26+, Docker, kubectl, Helm 3, and AWS credentials
with ECR, Glue, and S3 access (Bedrock only if you run the NL demo).

```bash
export AWS_REGION=us-west-2
export ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
export REGISTRY=$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com
kubectl config current-context   # must be the target cluster
```

**1. Compile and test locally** (offline, no cluster):

```bash
make test     # go vet + all unit and smoke tests
make build    # bin/manager, bin/server, bin/ossiectl
go run ./cmd/ossiectl validate -f examples/starrocks/retail/model/semanticmodel.yaml
```

**2. Build and push images:**

```bash
make ecr-create ecr-login AWS_REGION=$AWS_REGION REGISTRY=$REGISTRY
make docker-build docker-push REGISTRY=$REGISTRY TAG=0.1.0
```

x86 nodes use the default `PLATFORM=linux/amd64`. Graviton needs
`PLATFORM=linux/arm64`.

**3. Verify the cluster prerequisites and record endpoints:**

```bash
kubectl get svc -A | grep -iE 'starrocks|fe-service'   # required
kubectl get svc -A | grep -iE 'valkey|redis'           # optional (caching)
export SR_FE=starrocks-fe-service.starrocks.svc.cluster.local
export VALKEY=valkey-primary.valkey.svc.cluster.local:6379   # omit to run without a cache
```

**4. Install the operator and server:**

```bash
# $VALKEY may be empty. That --set line simply disables caching.
helm upgrade --install semantic-operator charts/semantic-operator \
  --namespace semantic-system --create-namespace \
  --set image.repository=$REGISTRY/semantic-operator \
  --set image.tag=0.1.0 \
  --set starrocks.host=$SR_FE \
  --set valkey.addr=$VALKEY \
  --set aws.region=$AWS_REGION
# If StarRocks has a root password:
#   kubectl -n semantic-system create secret generic starrocks-auth --from-literal=password=<pw>
#   ...and add: --set starrocks.passwordSecret.name=starrocks-auth

kubectl -n semantic-system get pods
```

Expected result: the `semantic-operator-manager` and `semantic-operator-server`
pods are Ready. The server's `/readyz` pings StarRocks. If a pod is not Ready,
`kubectl logs` says why.

**Operational notes.**

- Server not Ready. `/readyz` failing means StarRocks is unreachable from the pod.
  Check `starrocks.host` and NetworkPolicies.
- SemanticModel stuck with no conditions. Run `kubectl -n semantic-system logs
  deploy/semantic-operator-manager`.
- Model drift. `DriftDetected=True` is expected when the physical schema and the
  bindings diverge. The last good compiled artifact keeps serving until the drift
  is resolved.
- Scaling. The server is stateless (`server.replicas`). The manager is a single
  leader-elected writer.

## "Where do I put X?" contributor cheat-sheet

| I want to… | Touch | Then |
|---|---|---|
| Add a metric, dimension, or relationship field to the model schema | `api/v1alpha1/semanticmodel_types.go` | `make generate` (regenerates deepcopy and CRD), commit both |
| Change how a request compiles to SQL | `internal/planner/` (`plan.go`, `plan_sql.go`) | add a golden-SQL test in `plan_test.go` |
| Add or adjust a row or column policy | `internal/governance/governance.go` | it is compile-time. Assert the plan fails or filters |
| Support a new query engine | `internal/emitter/<engine>/` and `internal/<engine>/` | see [EXTENDING-ENGINES.md](EXTENDING-ENGINES.md) |
| Support a new catalog (Unity, Polaris, Hive) | `internal/catalog/<source>/` implementing `catalog.Source` | wire into `ossiectl` and the reconciler |
| Change reconcile or drift behavior | `controllers/semanticmodel_controller.go` | table-test with a fake `StarRocksClient` |
| Add an API surface beyond MCP and REST | `internal/serving/<adapter>/` calling `serving.Service` | reuse the shared serving path (plan, execute, report) |
| Change a deployment default | `charts/semantic-operator/values.yaml` and templates | `make helm-lint` |

**Do not** add other-engine SQL outside `emitter.Dialect`, and **do not** rename
the `osi` identifiers (the `semantic.ossie.io` API group, `spec.ossie`, `ossiectl`).
They are intentional backward-compat despite the OSI to Apache Ossie rename. See
[CLAUDE.md](../CLAUDE.md) for the full guardrails.

---

## Local development workflow

The domain core is offline and fast. You rarely need a cluster to make progress.

```bash
make build        # bin/manager, bin/server, bin/ossiectl
make test         # go vet ./... && go test ./... -count=1   (offline, fast)
make cover        # coverage profile
make generate     # REQUIRED after editing api/. Regenerates deepcopy + CRD
make helm-lint    # lint the chart

# plain Go works too
go build ./...  ·  go test ./...  ·  go vet ./...
```

**Testing.** Tests live next to code (`*_test.go`). The planner, validator,
governance, cache, and controller have unit tests that run with no cluster and no
database. The reconciler uses a fake `StarRocksClient`, and the planner asserts
golden SQL. Prefer running the whole suite. It is fast. Anything needing a live
cluster (data loader, NL comparison, benchmark) is isolated under `examples/` and
documented in each example's README.

**The one rule that keeps this working.** If you catch yourself importing an SDK
(AWS, the MySQL driver, controller-runtime) into `internal/planner`,
`internal/ossie`, or `internal/governance`, stop. That coupling belongs in the
adapter layer behind an interface. Keeping the core pure is what makes the tests
fast and the engines swappable.
