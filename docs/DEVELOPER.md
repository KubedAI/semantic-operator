# Developer guide

How this codebase is organized, how the pieces depend on each other, where
engine-specific code lives, what the Helm chart actually deploys, and how to
extend it without fighting the architecture. Read this first if you're going to
change code.

**Related docs.** [OVERVIEW.md](OVERVIEW.md) — what and why, plus role-based
onboarding (non-code). [ARCHITECTURE.md](ARCHITECTURE.md) — the runtime request
lifecycle and compile-time governance in depth.
[EXTENDING-ENGINES.md](EXTENDING-ENGINES.md) — step-by-step for adding
Trino/ClickHouse/DuckDB. [ROADMAP.md](ROADMAP.md) — planned work. Installing and
operating the two binaries is [§ Deploy & operate](#deploy--operate) below;
running a specific use case end to end lives in that example's README (start with
[examples/starrocks/retail](../examples/starrocks/retail/README.md)). This guide
is the map that ties them together.

---

## Mental model in 60 seconds

Think of it as a **compiler with a control plane**, not a query service.

- A user authors a `SemanticModel` custom resource (Apache Ossie YAML: datasets,
  fields, metrics, relationships, governance).
- The **operator** (`manager` binary) is the control plane: it validates the
  model, drift-checks its physical bindings against the live warehouse,
  compiles it to a versioned artifact (a ConfigMap), and creates governed views.
- The **semantic server** (`server` binary) is the data plane: stateless, it
  watches those artifacts and answers semantic requests by **compiling each
  request to exactly one SQL statement** — same request, same SQL, every time.
  An LLM only picks certified metrics/dimensions; it never writes SQL.
- Everything engine-specific (how to *render* and *run* SQL) sits behind two
  interfaces. Everything else — the model, the planner, governance — is
  engine-agnostic.

Two binaries, one shared core library, talking through the Kubernetes API.

---

## The dependency layering

The package graph points in **one direction**: binaries depend on adapters,
adapters depend on the domain core, the core depends on `api/` types, and
nothing depends back upward. This is the discipline that keeps the planner pure
and the engines pluggable.

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
        │  internal/planner  ·  internal/governance  ·  internal/osi  ·  api/v1alpha1  │
        └─────────────────────────────────────────────────────────────────────────────┘
```

The domain core imports **no** SDKs — no AWS, no MySQL driver, no
controller-runtime. That's why `internal/planner`, `internal/osi`,
`internal/governance` are fast, offline, table-driven unit tests. The
engine/cloud coupling lives entirely in the adapter layer.

---

## Repository map

Grouped by layer, top (binaries) to bottom (types).

### Binaries — `cmd/`

| Path | Binary | Role |
|---|---|---|
| `cmd/manager/` | `manager` | The operator. Reconciles `SemanticModel` CRs → publishes compiled-model ConfigMaps + creates governed views. Uses controller-runtime. |
| `cmd/server/` | `server` | The stateless semantic server. Watches ConfigMaps; hosts planner + governance + MCP/REST adapters + caches. Scales horizontally. |
| `cmd/osictl/` | `osictl` | CLI: offline model validation, Glue-stub derivation, CR round-trip. No cluster required for validate. |

Each `main.go` is thin: read env → construct clients → wire interfaces → run.
All configuration is env/flags; nothing is compiled in.

### Domain core — `api/` and `internal/` (engine-agnostic)

| Path | Contents |
|---|---|
| `api/v1alpha1/` | The CRD types (Apache Ossie model as Go structs). `semanticmodel_types.go` (spec/status), `helpers.go` (lookups, dialect preference), `ai_context.go` (LLM grounding fields), `groupversion_info.go`, `zz_generated.deepcopy.go` (generated — do not hand-edit). |
| `internal/osi/` | `validate.go` — Apache Ossie schema validation (structural correctness before compile). |
| `internal/planner/` | The compiler. `model.go` (CompiledModel), `plan.go` (semantic request → logical plan), `plan_sql.go` (logical plan → SQL via a Dialect), `expr/expr.go` (expression handling). This is the heart; it is pure. |
| `internal/governance/` | `governance.go` — compile-time row/column policies. Applied **inside** the planner before SQL is emitted; an unauthorized request fails to compile. |

### Adapter layer — the extension seams (`internal/`)

| Path | Interface / role | Ships |
|---|---|---|
| `internal/emitter/` | `emitter.Dialect` — SQL rendering atoms (quoting, literals, DATE_TRUNC, null-safe eq). Registry keyed by name. | — |
| `internal/emitter/starrocks/` | The StarRocks dialect implementation. | ✅ |
| `internal/catalog/` | `catalog.Source` — schema discovery for derivation/drift. `derive.go` turns physical tables into dataset stubs + infers relationships. | — |
| `internal/catalog/glue/` | AWS Glue implementation of `catalog.Source` (IRSA-authenticated). | ✅ |
| `internal/starrocks/` | `client.go` — MySQL-protocol client: `Query`, `Exec`, `DescribeTable`, `ShowCreateTable`. The runtime DB surface. | ✅ |
| `internal/cache/` | `cache.go` — Valkey (Redis-protocol) plan + result caches. **A nil cache is a valid no-op**, so caching is a pure optimization. | ✅ |
| `internal/serving/` | The shared query path used by both adapters. `service.go` (plan→execute→report, defines `QueryExecutor`), `store.go` (in-memory compiled-model store fed by the ConfigMap watcher). | ✅ |
| `internal/serving/mcp/` | `mcp.go` — MCP streamable-HTTP adapter (what LLM agents call). | ✅ |
| `internal/serving/rest/` | `rest.go` — REST adapter. | ✅ |
| `internal/serving/views/` | `views.go` — governed StarRocks view publisher (`Executor` = `Exec`). | ✅ |
| `internal/observability/` | `observability.go` — tracing + metrics wiring. | ✅ |

### Control plane — `controllers/`

`semanticmodel_controller.go` — the reconciler. Validates → binds/drift-checks
via the `StarRocksClient` interface (`DescribeTable`) → compiles → writes the
ConfigMap → publishes views. Defines the narrow interface it needs rather than
importing the concrete client.

### Everything else

| Path | What |
|---|---|
| `charts/semantic-operator/` | Helm chart: `crds/`, `templates/` (manager + server Deployments, RBAC, SAs), `values.yaml`. |
| `examples/` | Use cases grouped by engine. `starrocks/retail/` is the runnable reference example (`data/` loader, `model/` CR, `nl/` comparison, `bench/` harness); `starrocks/flights/` is a second, model-only Glue-bound example. See [examples/README.md](../examples/README.md). |
| `hack/` | `tools.go` — build-time tool dependencies. |
| `docs/` | These documents. |

---

## The two binaries and how they communicate

They **do not call each other**. They coordinate through the Kubernetes API,
which is what makes the server stateless and horizontally scalable.

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

- **manager** imports `controllers`, `internal/starrocks`, `internal/emitter`,
  `internal/catalog` (for derivation via `osictl`/reconcile). It is the only
  writer of compiled artifacts.
- **server** imports `internal/serving`, `internal/cache`, `internal/starrocks`,
  `internal/emitter`. It is read-only against models (consumes ConfigMaps) and
  read-only against the warehouse except for cached `SELECT`s.

---

## Engine-specific vs engine-agnostic code

This is the question that matters most for portability. **All StarRocks coupling
lives in exactly two packages**; everything else is engine-neutral.

| Concern | StarRocks today | Where a new engine (Trino, DuckDB, …) goes |
|---|---|---|
| SQL rendering (quoting, literals, DATE_TRUNC, null-safe eq) | `internal/emitter/starrocks/` | `internal/emitter/<engine>/` implementing `emitter.Dialect` |
| Runtime DB client (`Query`, `Exec`, `DescribeTable`) | `internal/starrocks/` | `internal/<engine>/` (a `database/sql` wrapper — same shape) |
| Catalog/schema discovery | `internal/catalog/glue/` (Glue) | `internal/catalog/<source>/` implementing `catalog.Source` (e.g. `information_schema`) |
| Which engine is active | `SQL_DIALECT=starrocks` (env) selects the dialect at runtime; client is currently constructed directly in both `main.go`s | Add a client factory keyed by the same `SQL_DIALECT` variable |

The planner, governance, validator, serving adapters, caching, and CRD types
are **untouched** when you add an engine. That is by design — see
[EXTENDING-ENGINES.md](EXTENDING-ENGINES.md) for the full Tier 1 walkthrough
(Trino/ClickHouse/DuckDB), including the per-method dialect deltas and the small
client-factory refactor.

> **Storage format (Iceberg) is not in this code at all.** The planner emits
> `catalog.database.table` references and hands the SQL to the engine; whether
> those tables are Iceberg/Delta/Hive is the engine's external-catalog concern.
> Grep confirms it: no snapshot/manifest/parquet logic anywhere. `iceberg` in
> the tree is only a configurable *catalog name string*.

---

## Deployment topology and the Valkey question

The Helm chart deploys **two Deployments** (manager, server) plus the CRD, RBAC,
and two ServiceAccounts. It does **not** deploy a database or a cache — it wires
the workloads to infrastructure you already run.

```
        Helm chart: semantic-operator
        ├── manager Deployment   (1 replica, leader-elected)     ← operator
        ├── server  Deployment   (N replicas, ClusterIP Service) ← semantic server
        ├── CRD  semanticmodels.semantic.osi.io
        ├── RBAC (Role/Binding) + 2 ServiceAccounts (IRSA-annotatable)
        │
        ├── depends on ── StarRocks   (EXISTING, required)   FE MySQL endpoint
        ├── depends on ── Valkey      (EXISTING, OPTIONAL)   Redis-protocol
        └── depends on ── AWS Glue    (via IRSA on manager SA)
```

**Does the deployment rely on Valkey?** **No — Valkey is optional.** This is true
in both layers:

- **Code:** `cache.New` returns a `nil *Cache` when the address is empty, and a
  nil cache is a valid no-op — callers never branch on "caching enabled." The
  server logs `VALKEY_ADDR not set; running without plan/result caches` and runs
  normally. A cache miss or an unreachable Valkey degrades to
  recompute-and-query, never to an error.
- **Chart:** `values.yaml` ships `valkey.addr: ""` by default, and
  `server-deployment.yaml` only injects the `VALKEY_*` env vars
  `{{- if .Values.valkey.addr }}`. Leave it empty and the server has no cache;
  it still answers every request correctly, just recompiling and re-querying
  each time.

Set `valkey.addr` to an existing Valkey/ElastiCache endpoint to turn caching on.
There is **no Valkey subchart** and no bundled Redis — you point at your own
(BYO). Same philosophy for StarRocks: it's an **existing** cluster (required),
not something the chart stands up.

**What each dependency is for:**

| Dependency | Required? | Used by | Purpose |
|---|---|---|---|
| StarRocks (FE MySQL endpoint) | **Yes** | both binaries | introspection (manager) + query execution (server) |
| Valkey (Redis protocol) | No | server only | plan + result caches (latency optimization) |
| AWS Glue | Only if using catalog derivation/drift from Glue | manager | reads physical schema; IRSA on the manager SA supplies creds |

Everything is a Helm value / env var — no account, endpoint, or region is
compiled into the images. See `charts/semantic-operator/values.yaml` and
[§ Deploy & operate](#deploy--operate).

---

## Deploy & operate

This is the engine- and example-agnostic install: build the images, deploy the
two workloads, point them at your existing StarRocks (and optionally Valkey).
Running a specific use case on top — loading data, applying a model, the
accuracy demo — lives in that example's README (start with
[examples/starrocks/retail](../examples/starrocks/retail/README.md)).

**Prerequisites.** An EKS (or any Kubernetes) cluster with an **existing
StarRocks** cluster reachable over the FE MySQL endpoint, fronting Iceberg via a
Glue external catalog. Valkey is **optional** (see the topology table above);
any MySQL-protocol BI tool can read the governed views without extra components. On your workstation: Go 1.26+, Docker, `kubectl`, Helm 3, and AWS
credentials with ECR/Glue/S3 access (Bedrock only if you run the NL demo).

```bash
export AWS_REGION=us-west-2
export ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
export REGISTRY=$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com
kubectl config current-context   # must be the target cluster
```

**1. Compile and test locally** (offline, no cluster):

```bash
make test     # go vet + all unit and smoke tests
make build    # bin/manager, bin/server, bin/osictl
go run ./cmd/osictl validate -f examples/starrocks/retail/model/semanticmodel.yaml
```

**2. Build and push images:**

```bash
make ecr-create ecr-login AWS_REGION=$AWS_REGION REGISTRY=$REGISTRY
make docker-build docker-push REGISTRY=$REGISTRY TAG=0.1.0
```

x86 nodes use the default `PLATFORM=linux/amd64`; Graviton needs
`PLATFORM=linux/arm64`.

**3. Verify the cluster prerequisites and record endpoints:**

```bash
kubectl get svc -A | grep -iE 'starrocks|fe-service'   # required
kubectl get svc -A | grep -iE 'valkey|redis'           # optional (caching)
export SR_FE=starrocks-fe-service.starrocks.svc.cluster.local
export VALKEY=valkey-primary.valkey.svc.cluster.local:6379   # omit to run without a cache
```

**4. Install the operator + server:**

```bash
# $VALKEY may be empty — that --set line simply disables caching.
helm upgrade --install semantic-operator charts/semantic-operator \
  --namespace semantic-system --create-namespace \
  --set image.repository=$REGISTRY/osi-semantic-operator \
  --set image.tag=0.1.0 \
  --set starrocks.host=$SR_FE \
  --set valkey.addr=$VALKEY \
  --set aws.region=$AWS_REGION
# If StarRocks has a root password:
#   kubectl -n semantic-system create secret generic starrocks-auth --from-literal=password=<pw>
#   ...and add: --set starrocks.passwordSecret.name=starrocks-auth

kubectl -n semantic-system get pods
```

Expected: `semantic-operator-manager` and the `semantic-operator-server` pods
Ready. The server's `/readyz` pings StarRocks; if it is not Ready, `kubectl logs`
says why.

**Operational notes.**

- **Server not Ready:** `/readyz` failing means StarRocks is unreachable from the
  pod — check `starrocks.host` and NetworkPolicies.
- **SemanticModel stuck with no conditions:** `kubectl -n semantic-system logs
  deploy/semantic-operator-manager`.
- **Model drift:** `DriftDetected=True` is expected when physical schema and
  bindings diverge; the last-good compiled artifact keeps serving until the drift
  is resolved.
- **Scaling:** the server is stateless (`server.replicas`); the manager is a
  single leader-elected writer.

## "Where do I put X?" — contributor cheat-sheet

| I want to… | Touch | Then |
|---|---|---|
| Add a metric/dimension/relationship *field* to the model schema | `api/v1alpha1/semanticmodel_types.go` | `make generate` (regenerates deepcopy + CRD), commit both |
| Change how a request compiles to SQL | `internal/planner/` (`plan.go`, `plan_sql.go`) | add a golden-SQL test in `plan_test.go` |
| Add/adjust a row/column policy | `internal/governance/governance.go` | it's compile-time — assert the plan fails/filters |
| Support a new query engine | `internal/emitter/<engine>/` + `internal/<engine>/` | see [EXTENDING-ENGINES.md](EXTENDING-ENGINES.md) |
| Support a new catalog (Unity, Polaris, Hive) | `internal/catalog/<source>/` implementing `catalog.Source` | wire into `osictl`/reconciler |
| Change reconcile/drift behavior | `controllers/semanticmodel_controller.go` | table-test with a fake `StarRocksClient` |
| Add an API surface (beyond MCP/REST) | `internal/serving/<adapter>/` calling `serving.Service` | reuse the shared plan→execute→report path |
| Change a deployment default | `charts/semantic-operator/values.yaml` + templates | `make helm-lint` |

**Do not** add other-engine SQL outside `emitter.Dialect`, and **do not** rename
the `osi` identifiers (API group `semantic.osi.io`, `spec.osi`, `osictl`) — they
are intentional backward-compat despite the OSI→Apache Ossie rename. See
[CLAUDE.md](../CLAUDE.md) for the full guardrails.

---

## Local development workflow

The domain core is offline and fast — you rarely need a cluster to make progress.

```bash
make build        # bin/manager, bin/server, bin/osictl
make test         # go vet ./... && go test ./... -count=1   (offline, fast)
make cover        # coverage profile
make generate     # REQUIRED after editing api/ — regenerates deepcopy + CRD
make helm-lint    # lint the chart

# plain Go works too
go build ./...  ·  go test ./...  ·  go vet ./...
```

**Testing philosophy.** Tests live next to code (`*_test.go`). The planner,
validator, governance, cache, and controller have unit tests that run with no
cluster and no database — the reconciler uses a fake `StarRocksClient`, the
planner asserts golden SQL. Prefer running the whole suite; it's fast. Anything
needing a live cluster (data loader, NL comparison, benchmark) is isolated under
`examples/` and documented in each example's README.

**The one rule that keeps this working:** if you catch yourself importing an SDK
(AWS, MySQL driver, controller-runtime) into `internal/planner`,
`internal/osi`, or `internal/governance`, stop — that coupling belongs in the
adapter layer behind an interface. Keeping the core pure is what makes both the
tests fast and the engines swappable.
