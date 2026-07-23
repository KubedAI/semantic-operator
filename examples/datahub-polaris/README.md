# Customer-health demo — local (kind)

A single-node [kind](https://kind.sigs.k8s.io/) cluster that runs the whole
customer-health demo locally: Garage (S3-compatible object store), Apache
Polaris (Iceberg REST catalog), StarRocks, the
Semantic Operator, and DataHub — over a small (~45k-row) synthetic dataset.
The stack is self-contained: an Iceberg lakehouse on Garage, catalogued by
Polaris, queried by StarRocks, governed by the Semantic Operator, and surfaced
for discovery in DataHub.


## Architecture (all in-cluster, single node, no Argo)

```
host ──► kind (1 node) ──┬─ Garage             S3-compatible object store (Iceberg data)
                         ├─ Postgres           shared: Polaris catalog + DataHub metadata
                         ├─ Polaris            Iceberg REST catalog on Garage (--no-sts)
                         ├─ StarRocks          shared-data: 1 FE + 1 CN; Iceberg external catalog → Polaris
                         ├─ Semantic Operator  reconciles SemanticModels → governed SQL/views over StarRocks
                         └─ DataHub            GMS + frontend + OpenSearch + Kafka; Iceberg ingestion from Polaris
```

The interactive LLM agent runs **on the host** (not in-cluster) and is the
final piece, added only after everything above is solid.

## How it works (through the agent's eyes)

> **Planned.** The agent is the last piece and isn't built in this
> example yet — this is the intended behavior.

One agent answers a question by composing both MCP servers, with a strict split:

> **DataHub** says what the data means, where it came from, who owns it, and
> whether it's trustworthy. The **Semantic Operator** decides what may be queried
> and computes the certified metric — the agent never writes SQL.

Asked to *"prepare a renewal briefing for Northstar Systems,"* the agent:

1. Discovers the datasets in DataHub — domain, owners, glossary, certification, freshness.
2. Drops the deprecated `account_feature_monthly` on metadata alone, and flags the `support_ticket` freshness gap.
3. Reads the exact semantic mapping from DataHub structured properties — never guessed from names.
4. Picks certified metrics by name; the operator compiles and runs the one governed SQL statement.
5. Sends its role in `X-Semantic-Role`; the operator denies disallowed fields before any SQL exists, and adds row filters (e.g. `region = 'NA'`).
6. Answers with certified values, DataHub context, and the operator's SQL/version/hash — no invented "health score."

If DataHub is unavailable, discovery and trust questions fail rather than guess.

## Prerequisites

- Docker, `kubectl`, `helm`, `aws` CLI (S3 API against Garage), and `kind`.
  The scripts default to `./bin/kind` (git-ignored); put a `kind` binary there
  or export `KIND=/path/to/kind`.
- **amd64** host.
- Substantial resources. DataHub's OpenSearch + Kafka dominate;
  budget **~10 GB RAM** and several CPUs for the Docker VM.

## Offline / one-time fetch

Everything is pinned in [`deploy/versions.lock`](deploy/versions.lock). Images
and helm charts are fetched **once** with network, cached locally, and loaded
into the kind node — so recreating the cluster needs no re-pull.

```bash
make charts-vendor     # helm pull pinned charts → deploy/<c>/charts/*.tgz   (needs network)
make images-pull       # docker pull all pinned images into the Docker cache (needs network)
make images-load       # kind load the pinned images into the node           (no registry)
```

`data/` (Garage objects, Postgres) is git-ignored runtime state; vendored chart
tarballs are committed so the stack is self-contained.

## Run order (each step is stood up and verified before the next)

One-time setup (needs network) — vendor charts and pull images into the Docker cache:

```bash
make charts-vendor charts-images images-pull
```

1. Create the kind cluster and load the images into the node:

```bash
make cluster-up images-load
```

2. Bring up Garage (S3 object store) and shared Postgres:

```bash
make garage-up postgres-up
```

3. Bring up Polaris and create the Iceberg REST catalog on Garage:

```bash
make polaris-up polaris-catalog
```

4. Bring up StarRocks and register the Iceberg external catalog:

```bash
make starrocks-up starrocks-catalog
```

5. Generate and load the ~45k-row dataset (takes a few minutes):

```bash
make data-load
```

6. Build/install the operator + server and apply the semantic models:

```bash
make operator-build operator-up models-apply
```

7. Install DataHub, then ingest and enrich the datasets (takes several minutes):

```bash
make datahub-up datahub-ingest datahub-enrich
```

The interactive local agent runs on the host and is added last. The steps and
their order mirror the pipeline map at the top of the [`Makefile`](Makefile).

## Datasets

The demo data models **B2B SaaS customer health** across four domains — Revenue
Operations, Product Analytics, Customer Operations, and a shared Data Platform.
The three SemanticModels (`saas_revenue`, `saas_adoption`, `saas_support`)
compute certified metrics over these tables. It is a small (~45k-row),
deterministic synthetic dataset generated by [`data-gen/`](data-gen/README.md).

| Table | Domain | What it represents |
|---|---|---|
| `date_dim` | shared | Calendar, one row per day (`2024-07-01` → `2027-06-30`); surrogate key + year/quarter/month. Every fact joins it for time grouping. |
| `account` | Customer Ops | The customer companies — `segment` (Enterprise/Mid-Market/SMB), `region`, `industry`, `csm_team`, `renewal_date`, `lifecycle_status` (trial/active/paused/churned/renewal-due). Includes the fixed **Northstar Systems** anchor (`account_id=1001`). |
| `account_primary_contact` | Customer Ops | Exactly one primary contact per account (name, email, phone, title). **PII** — value access is role-governed; one row per account so contact joins never multiply fact values. |
| `plan` | Revenue Ops | Catalog of SaaS plans/SKUs — name, tier (Starter/Growth/Business/Enterprise), product family. |
| `contract` | Revenue Ops | One row per contract — start/end, negotiated discount %, annual rate, contract value. **Confidential** pricing fields; the contract end date drives the renewal horizon. |
| `product_feature` | Product Analytics | Catalog of licensed features/modules — name, product area, criticality. |
| `account_feature_entitlement` | Product Analytics | Current licensed capacity per `(account, feature)`: licensed / eligible / adopted seats. The denominator for adoption and seat-utilization; **every `usage_daily` row matches exactly one entitlement**. |
| `subscription_monthly` | Revenue Ops | Monthly subscription snapshots (24 months per subscription): `status` + `mrr`, plus point-in-time `current_*` revenue fields (MRR/ARR/renewal-ARR and the GRR/NRR retention cohort) populated only on the current snapshot (`2026-06-01`). Source for MRR, ARR, GRR, NRR, and ARPA. |
| `usage_daily` | Product Analytics | Daily product-usage events at account-user-feature-day grain (the largest table): total/error event counts, plus current-window IDs and counters for June 2026. Source for active users/accounts, feature adoption, seat utilization, and error rate. Northstar is deliberately low-adoption. |
| `support_ticket` | Customer Ops | Support tickets — created date, requester email, subject, status, escalation/SLA flags, first-response/resolution hours. Current-period fields are evaluated at the **`2026-06-28` observation cutoff**; the feed intentionally stops ~48h before the as-of date, so support metrics carry a known freshness gap. **PII** fields role-governed. |
| `account_feature_monthly` | Product Analytics | A monthly adoption aggregate per `(account, feature, month)` that looks plausible but is **deprecated/stale** — a state knowable only from DataHub metadata, never from its name, schema, or values. The demo agent is expected to reject it on metadata alone. |

Most fact tables carry both raw columns and point-in-time `current_*` columns.
The `current_*` columns are populated only inside the fixed windows (revenue
current snapshot `2026-06-01`; adoption window June 2026; support window through
`2026-06-28`) and are zero/NULL elsewhere. The SemanticModels aggregate the
`current_*` columns, so each metric reflects the fixed as-of point without a
runtime date filter.


The dataset generator/loader is a small, standalone Go module in
[`data-gen/`](data-gen/README.md) — its own `go.mod`, one dependency, and none
of the parent demo's AWS/Glue/profile/manifest scaffolding.
