# AGENTS.md

Guidance for AI coding agents (Claude Code, KIRO, Copilot, Cursor, etc.) working in this repository.

## What this project is

`semantic-operator` is a **Kubernetes operator + stateless semantic server** written in Go.
It runs an **Apache Ossie (incubating)** semantic layer — the standard formerly called
**Open Semantic Interchange (OSI)** — on Kubernetes, on top of an existing **StarRocks**
or **Trino** query engine. The engine may reach Apache Iceberg through Glue, Polaris,
Hive Metastore, or another catalog it supports.

Core idea: users author a `SemanticModel` custom resource (Apache Ossie YAML). The operator
validates it, drift-checks its physical bindings against the live StarRocks/Iceberg schema,
compiles it deterministically, and publishes a compiled artifact. A **deterministic planner**
turns semantic requests (metrics, dimensions, filters, time grain) into **exactly one**
StarRocks SQL statement, with row/column governance applied **at compile time**. An LLM only
selects certified metrics/dimensions — it never writes SQL.

> Naming note: the standard was renamed OSI → Apache Ossie (July 2026), and this repo follows it.
> Identifiers use the `ossie` name throughout: the `semantic.ossie.io` API group, the `spec.ossie`
> block, `internal/ossie`, and the `ossiectl` CLI. The demo Glue database is still `osi_demo`
> (demo data, not a project identifier).

## Module & toolchain

- Go module: `github.com/KubedAI/semantic-operator` (Go 1.26).
- Kubebuilder/controller-runtime operator (`sigs.k8s.io/controller-runtime`).
- Key deps: AWS SDK v2 (Glue, Bedrock), `go-sql-driver/mysql` (StarRocks MySQL protocol),
  `redis/go-redis` (Valkey), `modelcontextprotocol/go-sdk` (MCP), OpenTelemetry, Prometheus.
- Two container images from one Dockerfile (`--target manager`, `--target server`),
  distroless nonroot, CGO disabled.

## Repository layout

```
api/v1alpha1/          CRD types (Apache Ossie model as Go structs); ai_context.go, helpers.go
controllers/           SemanticModel reconciler (semanticmodel_controller.go)
cmd/manager/           Operator binary (reconciles CRs)
cmd/server/            Stateless semantic server (planner + governance + adapters)
cmd/ossiectl/          CLI: offline validate, Glue-derived scaffolds, CR <-> Ossie round-trip
internal/ossie/        Apache Ossie schema validation (pure, no I/O)
internal/planner/      semantic request -> logical plan -> SQL; expr/ bounded grammars
internal/governance/   compile-time row/column/metric policies
internal/emitter/      Dialect interface; emitter/starrocks/, emitter/trino/ implementations
internal/dbclient/     engine connection interface + factory (SQL_DIALECT selects dialect+client)
internal/catalog/      Source (physical) + Enricher (business meaning) interfaces;
                       catalog/glue/, catalog/infoschema/ (any engine), catalog/datahub/ (enrich)
internal/cache/        Valkey plan + result caches (nil *Cache = valid no-op)
internal/serving/auth/ identity resolution: header mode vs JWKS-validated JWT
internal/serving/      Store (informer-fed model registry) + Service (one query path);
                       adapters: mcp/ (streamable HTTP), rest/, views/ (governed engine-native views)
internal/starrocks/    MySQL-protocol client + schema introspection (DESC / SHOW CREATE TABLE)
internal/trino/        Trino HTTP-protocol client + information_schema introspection
internal/nlbench/      NL comparison / benchmark support (Bedrock Converse, temperature 0)
internal/observability/ slog logger, Prometheus metrics, optional OTLP tracing
charts/semantic-operator/  Helm chart (crds/, templates/, values.yaml)
examples/              use cases by engine; starrocks/retail/ (data, model, nl, bench)
                       and starrocks/flights/ (model-only). See examples/README.md
website/               the documentation site (Astro + Starlight). ALL prose lives here,
                       in website/src/content/docs/. Diagrams are inline-SVG Astro
                       components in website/src/components/, themed via
                       website/src/styles/theme.css. `make docs` serves it locally.
docs/diagrams/         mermaid sources for reference (the site uses the components)
hack/                  tools.go (build-time tool deps)
```

## How the pieces fit (read this before editing)

**Write path (operator, `cmd/manager`):**
`Reconcile` in `controllers/semanticmodel_controller.go` drives
Validate → Compile → Bind/drift-check → Publish → Views.

1. `ossie.ValidateSpec` — structural checks, bounded metric grammar, join graph is a tree. Pure.
2. `planner.Compile` — freezes the spec into a `CompiledModel` JSON artifact.
   `planner.SpecVersion` = sha256(spec)[:12] is the content-addressed model version.
3. `bind` — `DESC` every dataset table through StarRocks; missing tables/columns are **drift**
   (blocks publication of the new version; the last published artifact keeps serving),
   connectivity failures are errors (requeue).
4. `publish` — writes the artifact to an owned ConfigMap `sm-<name>-compiled`, labeled with
   model name + version. Idempotent by content comparison.
5. `views.Publish` — each `spec.views` entry compiles through the same planner under its role
   and becomes `CREATE OR REPLACE VIEW` in StarRocks; owned view names are tracked in an
   annotation, stale ones are dropped. A finalizer drops views on CR deletion.

**Read path (server, `cmd/server`):**
`serving.WatchConfigMaps` (label-selected informer) keeps `serving.Store` current.
`serving.Service` is the single query path shared by MCP and REST:
plan (governed, cached) → execute (cached) → report. Every emitted statement carries a
leading `/* semantic-layer model=... version=... request=... */` comment for audit.

**Planner invariants (do not break these):**
- `planner.Build` is a pure function of (compiled model, dialect, request, identity):
  no I/O, no randomness, iteration only via order slices (`DatasetOrder`, `MetricOrder`,
  `FieldOrder`), never bare map range. Same request → byte-identical SQL.
- Governance (`governance.Authorize`) runs **before** any SQL exists; a violation is a
  compile error (`ErrUnauthorized` → HTTP 403), never a filtered result.
- Ratio metrics get fan-out-safe planning: sides that aggregate a non-root dataset are
  compiled as separate CTEs deduplicated on the dataset's `primary_key`.
- Metric expressions and row-filter predicates are parsed under **bounded grammars**
  (`internal/planner/expr`); anything outside them fails at validation, never at query time.

**Trust model:** the CR author is trusted (field expressions are raw SQL scalars by design).
Governance protects **query-time callers**, not against the model author. Identity is resolved
by `internal/serving/auth`: `AUTH_MODE=header` (default) trusts `X-Semantic-User` and
`X-Semantic-Role` and therefore
requires an authenticating proxy in front, while `AUTH_MODE=jwt` validates a bearer token against
the issuer's JWKS and **ignores both headers entirely**. 401 means unverified caller, 403 means
verified but disallowed. Row-filter predicates are grammar-bounded so a policy typo can't smuggle
arbitrary SQL through the control plane.

**Least privilege:** the manager and server resolve separate engine credentials
(`engine.manager.*` / `engine.server.*`). Manager = metadata read + DDL on the view schema only;
server = SELECT only. The operator imports no catalog package, so it needs no cloud credentials;
Glue/DataHub/Polaris are read only by `ossiectl` under a human's credentials.

## Common commands (via Makefile)

```bash
make build        # compile bin/manager, bin/server, bin/ossiectl
make test         # go vet ./... && go test ./... -count=1
make cover        # coverage profile
make generate     # regenerate deepcopy + CRD manifests after editing api/ (REQUIRED after CRD type changes)
make helm-lint    # lint the chart

# Images (registry/tags are variables; nothing hardcoded to an account)
make docker-build docker-push REGISTRY=<acct>.dkr.ecr.us-west-2.amazonaws.com

# Demo / benchmark (need a live cluster; see examples/retail/README.md for env)
make demo-data                                  # load TPC-DS subset as Iceberg tables
make demo-nl QUESTION="..."                     # answer both ways: raw text-to-SQL vs semantic layer
make bench                                       # write examples/retail/bench/RESULTS.md
```

Plain Go also works: `go build ./...`, `go test ./...`, `go vet ./...`.

Tests live next to code (`*_test.go`): `cmd/server/`, `controllers/`, `internal/cache/`,
`internal/catalog/`, `internal/planner/`, `internal/planner/expr/`, `internal/ossie/`,
`internal/serving/`. The whole suite is fast and offline (StarRocks and Kubernetes are
mocked/faked); prefer running all of it.

## Conventions & guardrails

- **Never publish a Service outside the cluster.** `ClusterIP` only, reached with
  `kubectl port-forward`; no `LoadBalancer`, no `NodePort`, in the chart, in any
  example, or in any deploy instruction. The server authorizes callers from the
  `X-Semantic-User` and `X-Semantic-Role` headers and expects an authenticating
  proxy in front, so an
  exposed Service is an unauthenticated query endpoint (on EKS, a public IP —
  this has already caused one security incident). The chart hard-fails on any
  other Service type, `hack/check-no-public-services.sh` enforces it repo-wide
  from `make lint` and CI, and third-party charts must be installed with their
  service type overridden to `ClusterIP` (many default to `LoadBalancer`) and
  audited afterwards with `kubectl get svc -A`.
- After changing anything in `api/`, run `make generate` and commit the regenerated
  `zz_generated.deepcopy.go` and `charts/semantic-operator/crds/`. CI fails otherwise
  (`git diff --exit-code` after regeneration).
- Keep identifiers consistent under the `ossie` name: API group `semantic.ossie.io`, `spec.ossie`,
  package `internal/ossie`, and the `ossiectl` CLI. User-facing error messages reference spec
  paths as `ossie.<field>`.
- Match surrounding Go style; this is idiomatic controller-runtime + stdlib code. Comments
  state constraints and reasons, not narration.
- The planner emits SQL only through `emitter.Dialect`; StarRocks and Trino ship today.
  Don't add engine-specific SQL outside that interface. New engines and catalog sources
  go behind `emitter.Dialect`, `dbclient.Client`, or `catalog.Source`; see the docs site's
  adding-an-engine and adding-a-catalog guides.
- Nothing is hardcoded to an account/endpoint: registry, StarRocks host, Valkey addr,
  AWS region, and catalog names are all Helm values / env vars.
- The docs site is the build spec. `website/src/content/docs/architecture.mdx` and its
  child pages describe intended behaviour; if code and those pages disagree, fix one of
  them in the same change. README.md and AGENTS.md remain at the repository root, and
  each example directory keeps a short README pointing at its docs-site page.
- Docs prose style: short declarative sentences, no em dashes, no semicolons joining
  clauses, no colon splices. Link with the page's name, never a file path.
- Commit under the user's own git identity only; do not add AI attribution trailers.

## Gotchas

- `spec.joins` (join-type overrides), `governance`, and `views` are operator extensions kept
  **outside** `spec.ossie` so `ossiectl unwrap`/`wrap` preserves the supported typed Ossie
  semantics. YAML formatting, comments, quoting, and field ordering are not preserved.
  Don't move operator concepts into the `ossie` block.
- The join graph is directed many→one (`from` = fact side). `builder.joinTree` roots at a
  required dataset first, then falls back to spec order — a query touching only dimension
  tables must not detour through the fact table.
- ConfigMap publication rehomes owner references from the legacy `semantic.osi.io` API group
  (pre-rename artifacts); see `publish()` in the controller before touching that logic.
- `nil *cache.Cache` is a working no-op cache — don't add "is caching enabled" branches.
- The plan cache key includes model version + request hash (which includes the effective
  role); the result cache is keyed on the emitted SQL. Changing hashing changes cache
  correctness — keep roles inside the key.

## Current state (as of 2026-07-19)

Working tree is on `main`, all tests green. Recent work: OSI → Ossie rename across module,
API group, CLI, and docs; store metrics + roadmap checklist; validation/runtime hardening.
Docs and examples were consolidated earlier for external review (three core guides + per-example
READMEs, hand-authored SVG diagrams with mermaid sources preserved).

If continuing: review with `git diff` / `git status` before committing.
