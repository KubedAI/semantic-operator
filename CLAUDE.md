# CLAUDE.md

Guidance for Claude Code (and other AI agents) working in this repository.

## What this project is

`osi-semantic-operator` is a **Kubernetes operator + stateless semantic server** written in Go.
It runs an **Apache Ossie (incubating)** semantic layer — the standard formerly called
**Open Semantic Interchange (OSI)** — on Amazon EKS, on top of an existing **StarRocks**
cluster that queries **Apache Iceberg** tables through an AWS **Glue** external catalog.

Core idea: users author a `SemanticModel` custom resource (Apache Ossie YAML). The operator
validates it, drift-checks its physical bindings against the live StarRocks/Iceberg schema,
compiles it deterministically, and publishes a compiled artifact. A **deterministic planner**
turns semantic requests (metrics, dimensions, filters, time grain) into **exactly one**
StarRocks SQL statement, with row/column governance applied **at compile time**. An LLM only
selects certified metrics/dimensions — it never writes SQL.

> Naming note: the standard was renamed OSI → Apache Ossie (July 2026). Code identifiers keep
> the historical `osi` short name on purpose: the `semantic.osi.io` API group, the `spec.osi`
> block, and the `osictl` CLI. Do not rename these — existing deployments depend on them.

## Module & toolchain

- Go module: `github.com/vara-bonthu/osi-semantic-operator` (Go 1.26).
- Kubebuilder/controller-runtime operator (`sigs.k8s.io/controller-runtime`).
- Key deps: AWS SDK v2 (Glue, Bedrock), `go-sql-driver/mysql` (StarRocks MySQL protocol),
  `redis/go-redis` (Valkey), `modelcontextprotocol/go-sdk` (MCP), OpenTelemetry, Prometheus.

## Repository layout

```
api/v1alpha1/          CRD types (Apache Ossie model as Go structs); ai_context.go, helpers.go
controllers/           SemanticModel reconciler (semanticmodel_controller.go)
cmd/manager/           Operator binary (reconciles CRs)
cmd/server/            Stateless semantic server (planner + governance + adapters)
cmd/osictl/            CLI: offline validate, Glue stub derivation, CR round-trip
internal/osi/          Apache Ossie schema validation
internal/planner/      semantic request -> logical plan; planner/expr/ expression handling
internal/governance/   compile-time row/column policies
internal/emitter/      Dialect interface; emitter/starrocks/ implementation
internal/catalog/      Source interface; catalog/glue/ implementation
internal/cache/         Valkey plan + result caches
internal/serving/      Adapters: mcp/ (streamable HTTP), rest/, views/ (governed StarRocks views)
internal/starrocks/    MySQL-protocol client + schema introspection
internal/nlbench/       NL comparison / benchmark support
internal/observability/ tracing + metrics
charts/semantic-operator/  Helm chart (crds/, templates/, values.yaml)
examples/              use cases by engine; starrocks/retail/ (data, model, nl, bench)
                       and starrocks/flights/ (model-only). See examples/README.md
docs/                  OVERVIEW, ARCHITECTURE, DEVELOPER, EXTENDING-ENGINES, ROADMAP;
                       diagrams/ (mermaid sources), img/ (hand-authored SVGs)
hack/                  tools.go (build-time tool deps)
```

## Common commands (via Makefile)

```bash
make build        # compile bin/manager, bin/server, bin/osictl
make test         # go vet ./... && go test ./... -count=1
make cover        # coverage profile
make generate     # regenerate deepcopy + CRD manifests after editing api/ (REQUIRED after CRD type changes)
make helm-lint    # lint the chart

# Images (registry/tags are variables; nothing hardcoded to an account)
make docker-build docker-push REGISTRY=<acct>.dkr.ecr.us-west-2.amazonaws.com

# Demo / benchmark (need a live cluster; see examples/starrocks/retail/README.md for env)
make demo-data                                  # load TPC-DS subset as Iceberg tables
make demo-nl QUESTION="..."                     # answer both ways: raw text-to-SQL vs semantic layer
make bench                                       # write examples/starrocks/retail/bench/RESULTS.md
```

Plain Go also works: `go build ./...`, `go test ./...`, `go vet ./...`.

Tests live next to code (`*_test.go`): `controllers/`, `internal/cache/`, `internal/planner/`,
`internal/planner/expr/`, `internal/osi/`. Prefer running the whole suite; it's fast and offline.

## Architecture essentials

- **Two binaries.** `manager` reconciles `SemanticModel` CRs → publishes a compiled model
  ConfigMap (versioned artifact) + creates governed StarRocks views. `server` is stateless,
  watches those ConfigMaps, and hosts the planner + governance + MCP/REST adapters + Valkey caches.
- **Determinism is the point.** The planner is a compiler: same semantic request → same SQL,
  every time. Every emitted statement carries a leading comment with model name, model version,
  and request hash for audit traceability.
- **Governance is compile-time.** Row/column policies are applied inside the planner before SQL
  is emitted; an unauthorized request fails to compile. No post-hoc filtering.
- **Extension points are interfaces.** `emitter.Dialect` (only StarRocks today),
  `catalog.Source` (only Glue today). A MetricFlow integration point is documented, not built.
  See docs/ARCHITECTURE.md#extension-points for scope guardrails — keep new engines/catalogs
  behind these interfaces.
- **Nothing is hardcoded to an account/endpoint.** Registry, StarRocks host, Valkey addr, AWS
  region, catalog names are all Helm values / env vars.

## Conventions & guardrails

- After changing anything in `api/`, run `make generate` and commit the regenerated
  `zz_generated.deepcopy.go` and `charts/semantic-operator/crds/`.
- Keep the `osi` identifiers (API group `semantic.osi.io`, `spec.osi`, `osictl`) — they are
  intentional backward-compat, not stale naming.
- Match surrounding Go style; this is idiomatic controller-runtime + stdlib code.
- The planner emits **StarRocks SQL only**. Don't add other-engine SQL outside the
  `emitter.Dialect` interface.

## Current working-tree state (as of 2026-07-15)

Recent commits built out: operator core (CRD, validator, planner, governance, cache, controller,
MCP/REST adapters) → packaging (Makefile, Dockerfile, CI, Helm) → demo + NL comparison + benchmark.

Docs + layout were then consolidated for external review:
- `demo/` and `bench/` moved under `examples/starrocks/retail/` (+ `examples/starrocks/flights/`).
- Docs reduced to three guides + per-example: `OVERVIEW` (absorbed TEACHING), `ARCHITECTURE`,
  `DEVELOPER` (absorbed RUNBOOK's deploy/operate); `RUNBOOK.md`/`DEMO.md`/`TEACHING.md` deleted,
  their end-to-end content folded into `examples/starrocks/retail/README.md`. Added `docs/ROADMAP.md`.
- Mermaid diagrams removed from README/ARCHITECTURE; replaced with hand-authored `docs/img/*.svg`,
  with sources preserved in `docs/diagrams/*.mmd`.

If continuing: review with `git diff` / `git status`
before committing.
