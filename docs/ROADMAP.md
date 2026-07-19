# Roadmap

This document sets out the main areas of future work for the project. It is a working roadmap rather than a fixed commitment. The aim is to build a semantic execution layer for AI agents and BI workloads that runs well on Kubernetes and is straightforward for the community to extend.

## Direction

The project should develop in four main directions:

- **Engine-neutral runtime.** StarRocks is the reference implementation today. In time, the runtime should support a broader set of engines through stable extension points.
- **Catalog-neutral metadata plane.** Glue is one metadata source today. OpenMetadata, DataHub, Hive Metastore, Iceberg REST, Polaris, and portable SQL metadata sources should fit naturally into the same model.
- **Contributor-friendly extension model.** Community contributors should be able to add engines, catalogs, auth adapters, and policy sources without reworking the core planner or controller.
- **CNCF-grade Kubernetes operation.** The operator and server should be efficient, observable, safe to roll out, and suitable for multi-tenant deployment patterns.

## P1: Kubernetes and runtime hardening

These items are the most valuable steps towards a production-grade operator and semantic server on Kubernetes. The emphasis is on correctness, modest control-plane load, and clear operational behaviour.

- [ ] **Admission webhook for `SemanticModel`.** Reject structurally invalid or unsupported specs before they hit reconcile, and add defaulting only where it is stable and documented.
- [ ] **Controller API efficiency review.** Audit reconcile paths for unnecessary `Get`, `Update`, status writes, relists, and watch-triggered churn. Keep reconcile idempotent and avoid API-server load when nothing materially changed.
- [ ] **End-to-end operator tests.** Add coverage for reconcile, drift detection, publish/update, finalizers, owned ConfigMap lifecycle, and governed view cleanup.
- [ ] **Helm production hardening.** Add or tighten resource requests and limits, security contexts, PodDisruptionBudgets, topology spread constraints, priority classes, and optional NetworkPolicies.
- [ ] **Image and supply-chain hardening.** Publish minimal production images, SBOMs, signatures, provenance, and vulnerability scan results as part of release automation.
- [ ] **Observability completion.** Add focused metrics and traces for reconcile outcomes, publish latency, informer sync state, model reload failures, artifact age, cache effectiveness, and engine execution latency.
- [ ] **Condition and event cleanup.** Standardize condition reasons and messages, and emit targeted Kubernetes events for drift, publish failure, catalog sync failure, and view-apply failure.
- [ ] **CRD schema tightening.** Improve OpenAPI validation, enums, examples, descriptions, and printer columns so the API is stricter and easier to use.
- [ ] **HA and rollout audit.** Validate leader election, multi-replica behavior, informer sync readiness, disruption handling, and rolling-upgrade safety for both deployables.
- [ ] **Operational SLOs and alerts.** Define SLOs and alerting guidance for reconcile failures, stale artifacts, model load failures, API latency, and availability.

## P1: Engine and catalog extensibility

This is one of the most important parts of the roadmap. The project should grow into a semantic runtime that can sit in front of the data engines commonly used by AI agents, analytics services, and business-facing BI tools.

- [ ] **Promote a full engine abstraction.** Move beyond `emitter.Dialect` to a broader engine contract that covers SQL emission, query execution, schema introspection, explain support, and engine capability flags.
- [ ] **Replace the direct DB client construction with an engine factory.** One engine selection path should choose the emitter, client, introspector, and capability profile together.
- [ ] **Add a portable `information_schema` metadata source.** This should become the lowest-common-denominator path for model derivation when no richer catalog exists.
- [ ] **Add more open metadata/catalog sources.** Prioritize OpenMetadata, DataHub, Hive Metastore, Iceberg REST, Polaris, and Glue as first-class implementations behind `catalog.Source`.
- [ ] **Add more query engines.** Prioritize Trino, ClickHouse, PostgreSQL, Redshift, and DuckDB. StarRocks remains the reference engine until others reach parity.
- [ ] **Define engine support tiers.** For example: Tier 1 fully supported, Tier 2 community maintained, Tier 3 experimental. This prevents vague support claims and helps contributors target realistic milestones.
- [ ] **Extract a planner interface.** Turn the current package-level planner entrypoint into a `planner.Planner` interface so alternative compilers can plug in cleanly.
- [ ] **Stabilize the compiled semantic artifact contract.** Treat the published ConfigMap payload as a versioned runtime artifact so alternative servers or adapters can consume it safely.

## P2: Open-source catalog and semantic reuse

Today the operator imports physical schema only. Over time, it should also reuse semantics and governance that already exist in open metadata systems so that users can build on work they already maintain elsewhere.

- [ ] **Import semantics from OpenMetadata.** Map glossary terms, owners, descriptions, tags, and classifications into the semantic model and governance scaffolding.
- [ ] **Import semantics from DataHub.** Reuse ownership, glossary, domain, schema, and policy metadata where it maps cleanly.
- [ ] **Support Hive Metastore and Iceberg-native metadata paths.** Model derivation should not require Glue if the user already has an open metastore or Iceberg catalog.
- [ ] **Sync semantics over time, not only at bootstrap.** Support controlled refresh of descriptions, tags, field lists, and selected governance metadata from the source catalog.
- [ ] **Add automatic dataset field refresh.** Reconcile physical field-list changes from the catalog on a controlled timer or explicit sync action, rather than requiring manual re-derive only.
- [ ] **Introduce a semantic import policy.** Users should be able to decide what is authoritative in the source catalog versus what remains human-owned in Git.

## P2: Local developer experience and contributor friendliness

If the project is to grow into a strong CNCF community runtime, contributors need to be able to develop and test it without depending on EKS, Glue, or a vendor account.

- [ ] **Add a local `kind` quickstart.** Make it possible to run the operator locally with a fully open stack.
- [ ] **Build a portable local data stack.** Use combinations such as MinIO, Iceberg REST, Hive Metastore, Trino, ClickHouse, PostgreSQL, or DuckDB for local development and CI coverage.
- [ ] **Publish prebuilt public images.** Quickstarts and evaluations should not require local image builds.
- [ ] **Write a contributor guide for engines and catalogs.** Show exactly how to add a new engine adapter or metadata source and how to test it.
- [ ] **Publish capability matrices.** Document what works per engine and per catalog: derivation, drift check, governed views, planner features, auth patterns, and benchmark coverage.
- [ ] **Provide example implementations.** Ship at least one additional engine and one additional catalog source as extension references, not just interfaces.

## P2: Conformance and compatibility

An extensible platform needs clear tests that define what support means in practice.

- [ ] **Engine conformance suite.** Verify deterministic SQL generation, query execution, join safety, filter semantics, time grain behavior, ratio handling, governance enforcement, and explainability across engines.
- [ ] **Catalog conformance suite.** Verify table discovery, field typing, relationship inference hooks, tag import, and drift-related metadata behavior across catalog implementations.
- [ ] **Compiled artifact compatibility tests.** Ensure the operator and semantic server remain compatible across version upgrades.
- [ ] **Benchmark expansion beyond NL.** Measure semantic correctness, determinism, governance correctness, cache behavior, and drift handling over time.
- [ ] **Reference example matrix.** Add end-to-end examples for Trino and ClickHouse once their adapters land, and expand beyond the current StarRocks retail flow.

## P2: Authentication, tenancy, and policy

A production semantic runtime needs to fit cleanly within real cluster and organisational boundaries.

- [ ] **Trusted auth front-door guidance.** Document and test production deployment patterns for authenticating proxies and trusted identity propagation into REST and MCP.
- [ ] **Multi-tenant deployment model.** Define clear watch-scope, namespace, and RBAC patterns for serving multiple teams safely from one cluster.
- [ ] **Policy provider abstraction.** Allow governance inputs to come from multiple sources over time, not only inline CR fields.
- [ ] **Audit and traceability model.** Standardize how model version, request hash, role context, and policy decisions are exposed in logs, metrics, and traces.

## P3: Automated semantic model creation

This is a major long-term area of work. The value lies not only in executing semantic models well, but also in reducing the effort needed to create and maintain them.

- [ ] **Automatic semantic model bootstrap.** Generate a draft semantic model from physical schema, metadata tags, docs, and naming conventions.
- [ ] **Suggested joins and entity inference.** Improve relationship inference beyond simple key-name matching while keeping the final model human-reviewed.
- [ ] **Suggested metrics and synonyms.** Propose candidate metrics, business names, and AI-friendly descriptions from metadata and usage patterns.
- [ ] **Semantic drift remediation workflows.** Go beyond detection and help users review, accept, or reject safe model updates.
- [ ] **Authoring UX improvements.** Add dry-run, diff, explain-plan, and lint workflows so model authors can iterate safely before applying changes.

## P3: Ecosystem growth

These items become more important once the runtime core, extension model, and local developer experience are in good shape.

- [ ] **More reference examples.** Add examples for engines and catalogs beyond the initial pairings.
- [ ] **Public benchmark corpus.** Grow the NL benchmark corpus and keep regression history visible over time.
- [ ] **Release compatibility policy.** Publish versioning and support expectations for CRDs, compiled artifacts, engine plugins, and catalog plugins.
- [ ] **Community extension registry.** Long term, make it easy to discover community-maintained adapters and examples.

## Bigger bets (design first)

- [ ] **Alternative compiler backends.** Keep the runtime contract stable enough that alternative planners or compilers can plug into the same serving and governance surfaces.
- [ ] **Richer semantic interoperability.** Evaluate compatibility paths with adjacent open semantic ecosystems without weakening deterministic execution or governance guarantees.

## Out of scope (for now)

- Full MetricFlow semantics: multi-hop metrics, cumulative metrics, saved queries.
- Bundling a BI tool into this project.
- Becoming a managed vendor platform inside the core repo.
- Multi-engine federation until single-engine correctness, extensibility, and operability are mature.
- Engines that cannot support the project's core guarantees around deterministic planning, joins, governance, and auditable execution.
