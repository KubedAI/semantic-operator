# Roadmap

Where this project can go next, and what is open to work on. Not a commitment. Everything here is optional. The current StarRocks deployment needs none of it.

## Priority: reuse existing catalog semantics

Today the operator imports only physical schema, from Glue, behind the `catalog.Source` interface. It does not know your business meaning, so someone has to write metric descriptions, synonyms, and access rules by hand.

- **Import semantics from OpenMetadata or DataHub.** Add a semantics source that reads what these catalogs already hold and maps it into the model. Descriptions and glossary terms become `description` and `ai_context`. PII and sensitivity tags become `governance.denyFields`. Ownership and ACLs become `governance` roles. This makes the project complementary to existing catalogs instead of a parallel island, and it is the biggest lever for adoption.

## Priority: production hardening for Kubernetes

These are the highest-value items for making the operator and semantic server production-grade on Kubernetes, with a bias toward correctness, low control-plane load, and strong operability.

### P1: do next

- [ ] **Admission webhook for `SemanticModel`.** Reject structurally invalid or unsupported specs before they hit reconcile, and add defaulting only where it is stable and documented.
- [ ] **Controller API efficiency review.** Audit reconcile paths for unnecessary `Get`, `Update`, and status writes. Keep reconcile idempotent and avoid API-server churn when nothing materially changed.
- [ ] **End-to-end operator tests.** Add coverage for reconcile, drift detection, publish/update, finalizers, owned ConfigMap lifecycle, and governed view cleanup.
- [ ] **Helm production hardening.** Add or tighten resource requests and limits, security contexts, PodDisruptionBudgets, topology spread constraints, and optional NetworkPolicies.
- [ ] **Image and supply-chain hardening.** Publish minimal production images, SBOMs, signatures, and vulnerability scan results as part of release automation.
- [ ] **Observability completion.** Add focused metrics and traces for reconcile outcomes, publish latency, informer sync state, model reload failures, and artifact age.
- [ ] **Condition and event cleanup.** Standardize condition reasons and messages, and emit targeted Kubernetes events for drift, publish failure, and view-apply failure.
- [ ] **CRD schema tightening.** Improve OpenAPI validation, enums, examples, descriptions, and printer columns so the API is stricter and easier to use.
- [ ] **HA and rollout audit.** Validate leader election, multi-replica behavior, informer sync readiness, and rolling-upgrade safety for both deployables.

### P2: after the core hardening

- [ ] **DB client factory behind `SQL_DIALECT`.** Select both the SQL emitter and the query client from one engine abstraction, instead of binding only the emitter today.
- [ ] **Additional `catalog.Source` implementations.** Add Unity, Polaris, Hive, or `information_schema` sources so the project is not tied to Glue for model derivation.
- [ ] **Additional SQL dialects.** Add Trino, ClickHouse, and DuckDB behind `emitter.Dialect`.
- [ ] **Planner interface extraction.** Turn the current package-level planner entrypoint into a `planner.Planner` interface so alternative compilers can plug in cleanly.
- [ ] **Automatic dataset field refresh.** Reconcile physical field-list changes from the catalog on a controlled timer or explicit sync action, rather than requiring manual re-derive only.
- [ ] **Trusted auth front-door guidance.** Document and test production deployment patterns for authenticating proxies and trusted identity propagation into REST and MCP.
- [ ] **Multi-tenant deployment model.** Define clear watch-scope, namespace, and RBAC patterns for serving multiple teams safely from one cluster.
- [ ] **SLOs, alerts, and runbooks.** Add production operating guidance for cache health, reconcile failures, drift detection, latency, and availability.

### P3: ecosystem and adoption

- [ ] **Import semantics from OpenMetadata or DataHub.** Add a semantics source that reads what these catalogs already hold and maps it into the model. Descriptions and glossary terms become `description` and `ai_context`. PII and sensitivity tags become `governance.denyFields`. Ownership and ACLs become `governance` roles. This makes the project complementary to existing catalogs instead of a parallel island, and it is the biggest lever for adoption.
- [ ] **A local `kind` quickstart.** Let users try the operator without EKS or an AWS account, using a local-compatible catalog and storage path.
- [ ] **Prebuilt public images.** Publish public images so quickstarts and evaluations do not require local image builds.
- [ ] **More examples.** Add ClickHouse and Trino examples once those dialects land, and complete the flights example with an end-to-end data loader.
- [ ] **Benchmark expansion.** Grow the NL benchmark corpus and make semantic correctness regressions visible over time.

## More engines (behind the two interfaces)

All engine-specific code lives behind `emitter.Dialect` and a small query client. See [EXTENDING-ENGINES.md](EXTENDING-ENGINES.md).

- **Trino, ClickHouse, and DuckDB dialects** (`emitter.Dialect`). About 40 lines each. The per-engine deltas are already written up.
- **A `SQL_DIALECT`-driven client factory** (`internal/dbclient`). Today `SQL_DIALECT` selects the emitter but the DB client is still constructed directly. One factory would let a single env var pick both.
- **More `catalog.Source` implementations.** Unity, Polaris, Hive, or a portable `information_schema` source. The last one also lets `ossiectl derive` work without Glue.
- **Automatic dataset field-list refresh** from the catalog on a timer, so new physical columns become available as dimensions without hand-editing. Re-running `ossiectl derive` is the manual path today. A `spec.catalogSync` field was removed because it was never wired to the reconciler, so build the timer before re-adding the API.

## More examples

- **ClickHouse and Trino examples**, once their dialects land.
- **A data loader for the flights example** so it runs end to end, not model-only. Good first task.

## Easier onboarding

- **A local `kind` quickstart** so people can try the operator without EKS or an AWS account. The open question is the lake. Either point StarRocks at a local Iceberg REST catalog plus MinIO, or use StarRocks-native tables for the demo data.
- **Prebuilt public images** so the quickstart can skip `docker build`. Good first task.

## Bigger bets (design first)

- **A `planner.Planner` interface.** The compiler is currently `planner.Build`, a package-level function. Extracting it behind an interface would let an alternative compiler, for example a MetricFlow-backed one, slot in behind the same MCP, REST, and views adapters without touching them.

## Out of scope (for now)

- Full MetricFlow semantics: multi-hop metrics, cumulative metrics, saved queries.
- Multi-engine federation, and a bundled BI tool.
- Real-time OLAP engines without joins or views (Tier 2). See the scope guardrail in [EXTENDING-ENGINES.md](EXTENDING-ENGINES.md).
