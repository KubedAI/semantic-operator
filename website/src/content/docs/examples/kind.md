---
title: Everything on your laptop
description: The whole stack in a local kind cluster. An object store, an Iceberg REST catalog, a query engine, DataHub, and the operator, with no cloud account.
---

This runs the entire stack on one machine in a [kind](https://kind.sigs.k8s.io/) cluster.
Garage provides S3 compatible storage, Polaris is the Iceberg REST catalog, StarRocks is the
engine, and DataHub supplies discovery and business metadata.

Nothing here touches a cloud account. It is the right starting point if you want to
understand the system before committing infrastructure to it.

A single-node [kind](https://kind.sigs.k8s.io/) cluster that runs the whole
customer-health demo locally: Garage (S3-compatible object store), Apache
Polaris (Iceberg REST catalog), StarRocks, the
Semantic Operator, and DataHub. Over a small (~45k-row) synthetic dataset.
The stack is self-contained: an Iceberg lakehouse on Garage, catalogued by
Polaris, queried by StarRocks, governed by the Semantic Operator, and surfaced
for discovery in DataHub.


## Architecture

```
host ──► kind (1 node) ──┬─ Garage             S3-compatible object store (Iceberg data)
                         ├─ Postgres           shared: Polaris catalog + DataHub metadata
                         ├─ Polaris            Iceberg REST catalog on Garage (--no-sts)
                         ├─ StarRocks          shared-data: 1 FE + 1 CN; Iceberg external catalog → Polaris
                         ├─ Semantic Operator  reconciles SemanticModels → governed SQL/views over StarRocks
                         └─ DataHub            GMS + frontend + OpenSearch + Kafka; Iceberg ingestion from Polaris
```


## How it works (through the agent's eyes)

One agent answers a question by composing both MCP servers, with a strict split:

> **DataHub** says what the data means, where it came from, who owns it, and
> whether it's trustworthy. The **Semantic Operator** decides what may be queried
> and computes the certified metric. The agent never writes SQL.

Asked to *"prepare a renewal briefing for Northstar Systems,"* the agent:

1. Discovers the datasets in DataHub. Domain, owners, glossary, certification, freshness.
2. Drops the deprecated `account_feature_monthly` on metadata alone, and flags the `support_ticket` freshness gap.
3. Reads the exact semantic mapping from DataHub structured properties. Never guessed from names.
4. Picks certified metrics by name. The operator compiles and runs the one governed SQL statement.
5. Sends its principal in `X-Semantic-User` and role in `X-Semantic-Role`. The operator denies disallowed fields before any SQL exists, and adds row filters (e.g. `region = 'NA'`).
6. Answers with certified values, DataHub context, and the operator's SQL/version/hash. No invented "health score."

If DataHub is unavailable, discovery and trust questions fail rather than guess.

## Prerequisites

- Docker, `kubectl`, `helm`, `aws` CLI (S3 API against Garage), and `kind`.
  The scripts default to `./bin/kind` (git-ignored). Put a `kind` binary there
  or export `KIND=/path/to/kind`.
- **amd64** host.
- Substantial resources. DataHub's OpenSearch + Kafka dominate.
  budget **~10 GB RAM** and several CPUs for the Docker VM.
- **inotify limits.** A single node running this many pods can exhaust the
  host's default inotify limits. You may see pods crash-loop with
  `too many open files` or `User limit of inotify instances reached`. 
  If that happens, raise the limits on the host and restart the affected pod:

  ```bash
  sudo tee /etc/sysctl.d/99-kind-inotify.conf >/dev/null <<'EOF'
  fs.inotify.max_user_instances = 1024
  fs.inotify.max_user_watches   = 1048576
  EOF
  sudo sysctl --system
  ```

## Offline / one-time fetch

Everything is pinned in [`deploy/versions.lock`](https://github.com/KubedAI/semantic-operator/blob/main/examples/stacks/kind/datahub-polaris-starrocks/deploy/versions.lock). Images
and helm charts are fetched **once** with network, cached locally, and loaded
into the kind node. So recreating the cluster needs no re-pull.

```bash
make charts-vendor     # helm pull pinned charts → deploy/<c>/charts/*.tgz   (needs network)
make images-pull       # docker pull all pinned images into the Docker cache (needs network)
make images-load       # kind load the pinned images into the node           (no registry)
```

`data/` (Garage objects, Postgres) is git-ignored runtime state. Vendored chart
tarballs are committed so the stack is self-contained.

## Run order (each step is stood up and verified before the next)

One-time setup (needs network). Vendor charts and pull images into the Docker cache:

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

8. Build and deploy the DataHub MCP server, then ask questions interactively.
   `make agent` needs an OpenAI-compatible LLM endpoint. For Amazon Bedrock,
   set `OPENAI_BASE_URL` to the `bedrock-mantle` endpoint, `OPENAI_API_KEY` to a
   Bedrock API key, and `BEDROCK_MODEL_ID` to a model that supports the
   **Responses API and tool calling** (e.g. The `gpt-5.6` family):

```bash
make datahub-mcp-build datahub-mcp-up
export OPENAI_BASE_URL="https://bedrock-mantle.<region>.api.aws/openai/v1"
export OPENAI_API_KEY="<your Bedrock API key>"
export BEDROCK_MODEL_ID="<your enabled model id>"
make agent                       # or: make agent ROLE=finance_analyst
```

Then ask questions in the REPL. See [`agent/`](#the-agent) for example
questions, the governance roles (`ROLE=`), and what to check in the results.
Step order mirrors the pipeline map at the top of the [`Makefile`](https://github.com/KubedAI/semantic-operator/blob/main/examples/stacks/kind/datahub-polaris-starrocks/Makefile).

## Teardown

`make cluster-down` deletes the kind cluster but preserves the persisted host
state under `data/` (Garage objects + Postgres), so a rebuild reuses the loaded
data. To reset completely, remove it:

```bash
make cluster-down     # delete the kind cluster (data/ preserved)
rm -rf data/          # drop the persisted Garage + Postgres state for a clean slate
```

## Datasets

The demo data models **B2B SaaS customer health** across four domains. Revenue
Operations, Product Analytics, Customer Operations, and a shared Data Platform.
The three SemanticModels (`saas_revenue`, `saas_adoption`, `saas_support`)
compute certified metrics over these tables. It is a small (~45k-row),
deterministic synthetic dataset generated by [`data-gen/`](#the-data-generator).

| Table | Domain | What it represents |
|---|---|---|
| `date_dim` | shared | Calendar, one row per day (`2024-07-01` → `2027-06-30`); surrogate key + year/quarter/month. Every fact joins it for time grouping. |
| `account` | Customer Ops | The customer companies. `segment` (Enterprise/Mid-Market/SMB), `region`, `industry`, `csm_team`, `renewal_date`, `lifecycle_status` (trial/active/paused/churned/renewal-due). Includes the fixed **Northstar Systems** anchor (`account_id=1001`). |
| `account_primary_contact` | Customer Ops | Exactly one primary contact per account (name, email, phone, title). **PII**. value access is role-governed. One row per account so contact joins never multiply fact values. |
| `plan` | Revenue Ops | Catalog of SaaS plans/SKUs. name, tier (Starter/Growth/Business/Enterprise), product family. |
| `contract` | Revenue Ops | One row per contract. start/end, negotiated discount %, annual rate, contract value. **Confidential** pricing fields. The contract end date drives the renewal horizon. |
| `product_feature` | Product Analytics | Catalog of licensed features/modules. name, product area, criticality. |
| `account_feature_entitlement` | Product Analytics | Current licensed capacity per `(account, feature)`: licensed / eligible / adopted seats. The denominator for adoption and seat-utilization; **every `usage_daily` row matches exactly one entitlement**. |
| `subscription_monthly` | Revenue Ops | Monthly subscription snapshots (24 months per subscription): `status` + `mrr`, plus point-in-time `current_*` revenue fields (MRR/ARR/renewal-ARR and the GRR/NRR retention cohort) populated only on the current snapshot (`2026-06-01`). Source for MRR, ARR, GRR, NRR, and ARPA. |
| `usage_daily` | Product Analytics | Daily product-usage events at account-user-feature-day grain (the largest table): total/error event counts, plus current-window IDs and counters for June 2026. Source for active users/accounts, feature adoption, seat utilization, and error rate. Northstar is deliberately low-adoption. |
| `support_ticket` | Customer Ops | Support tickets. created date, requester email, subject, status, escalation/SLA flags, first-response/resolution hours. Current-period fields are evaluated at the **`2026-06-28` observation cutoff**; the feed intentionally stops ~48h before the as-of date, so support metrics carry a known freshness gap. **PII** fields role-governed. |
| `account_feature_monthly` | Product Analytics | A monthly adoption aggregate per `(account, feature, month)` that looks plausible but is **deprecated/stale**. a state knowable only from DataHub metadata, never from its name, schema, or values. The demo agent is expected to reject it on metadata alone. |

Most fact tables carry both raw columns and point-in-time `current_*` columns.
The `current_*` columns are populated only inside the fixed windows (revenue
current snapshot `2026-06-01`. Adoption window June 2026. Support window through
`2026-06-28`) and are zero/NULL elsewhere. The SemanticModels aggregate the
`current_*` columns, so each metric reflects the fixed as-of point without a
runtime date filter.


The dataset generator/loader is a small, standalone Go module in
[`data-gen/`](#the-data-generator). Its own `go.mod`, one dependency, and none
of the parent demo's AWS/Glue/profile/manifest scaffolding.



## The agent

A small, standalone Go program that answers customer-health questions by
composing two governed MCP servers:

- the **Semantic Operator MCP** (`localhost:8090/mcp`). `list_models`,
  `list_metrics`, `list_dimensions`, `query_metric`. The agent selects certified
  metrics and dimensions. The operator compiles and runs the one governed SQL
  statement. **The agent never writes SQL.**
- the **DataHub MCP** (`localhost:8091/mcp`, optional). Search, entity metadata,
  schema fields, lineage, glossary, structured properties. The agent uses it to
  discover assets and judge whether they're trustworthy.

It calls an **OpenAI-compatible Responses API endpoint** via the official
`openai-go` SDK. With Amazon Bedrock that is the `bedrock-mantle` endpoint, so
only the base URL and key change. The model you pick must support the
**Responses API and function/tool calling** (the agent is driven entirely by
tool calls).

## Run

From the example root (`examples/stacks/kind/datahub-polaris-starrocks`), after the stack is up:

```bash
make datahub-mcp-build datahub-mcp-up      # deploy the in-cluster DataHub MCP
export OPENAI_BASE_URL="https://bedrock-mantle.<region>.api.aws/openai/v1"
export OPENAI_API_KEY="<your Bedrock API key>"
export BEDROCK_MODEL_ID="<your enabled model id>"
make agent                                 # interactive REPL
make agent ROLE=finance_analyst            # different governance role
make agent QUESTION="What is our NRR?"     # one-shot, then exit
```

> The Responses API on bedrock-mantle lives under **`/openai/v1`** (not `/v1`).
> the agent appends `/responses`. Use a model that supports the Responses API
> (e.g. The `gpt-5.6` family). The first call to a cold model can take ~60 to 90s.
> subsequent calls are ~1s.

Or run the binary directly from this directory:

```bash
go run . -role platform_analyst
```

## Configuration

| Env / flag | Default | Purpose |
|---|---|---|
| `OPENAI_BASE_URL` |. (required) | Responses API base URL (Bedrock `bedrock-mantle`, the `…/v1` root) |
| `OPENAI_API_KEY` |. (required) | Bearer key for the endpoint |
| `BEDROCK_MODEL_ID` / `-model` |. (required) | Model id |
| `SEMANTIC_MCP_URL` / `-operator-mcp` | `http://localhost:8090/mcp` | Semantic Operator MCP |
| `DATAHUB_MCP_URL` / `-datahub-mcp` | empty | DataHub MCP (empty ⇒ operator-only) |
| `DATAHUB_GMS_TOKEN` | `local-no-auth` | Bearer sent to the DataHub MCP (auth disabled locally) |
| `SEMANTIC_ROLE` / `-role` | `platform_analyst` | Sent as `X-Semantic-Role`; drives governance |
| `-question` | empty | Ask one question and exit |
| `-max-iters` | `12` | Max tool calls per question |

## Governance roles

The agent sends the fixed `demo-agent` principal in `X-Semantic-User` and the selected role in
`X-Semantic-Role`. The operator enforces policy
at compile time. Denied fields fail before any SQL exists. Row filters are
injected automatically:

- `platform_analyst` (default). PII **and** confidential contract $ denied.
- `finance_analyst`. Sees contract $; PII denied.
- `na_customer_success`. Contract $ denied + automatic `region = 'NA'` filter.

Ask the same question under two roles to see the difference.

## Layout

```
main.go     flags, env, REPL + one-shot
agent.go    multi-turn tool loop (bounded), conversation memory
llm.go      OpenAI-compatible Chat Completions client (net/http)
mcp.go      connect + merge operator/DataHub MCP tools, route calls, provenance
prompt.go   the authority-split system prompt
```

Standalone Go module (`chd.local/agent`). Dependencies: the MCP Go SDK and the
official `openai-go` v2 SDK. Build/test:

```bash
go mod tidy && go build ./... && go test ./...
```



## The data generator

A small, self-contained Go module that deterministically generates the
customer-health demo dataset and loads it into the local StarRocks **Iceberg**
external catalog. It is intended for the **local** (kind) deployment only.

It is deliberately lightweight: its own `go.mod`, one dependency (the MySQL
driver StarRocks speaks), and **none** of the parent demo's operational
scaffolding. No AWS/S3, no Glue, no IAM preflight, no storage-location
management, no data profiles, no `_demo_manifest`/checksums, and no
force/reset. The REST catalog (Polaris) owns table locations, so the loader
just runs `CREATE TABLE IF NOT EXISTS` and batched `INSERT`s, then verifies each
table's row count.

## Dataset

Eleven business tables, ~44,835 rows total (fact volume scales with a
60-account base):

| Table | Rows | | Table | Rows |
|---|--:|---|---|--:|
| `date_dim` | 1,095 | | `account_feature_entitlement` | 360 |
| `account` | 60 | | `subscription_monthly` | 1,680 |
| `account_primary_contact` | 60 | | `usage_daily` | 36,000 |
| `plan` | 12 | | `support_ticket` | 4,000 |
| `contract` | 80 | | `account_feature_monthly` | 1,440 |
| `product_feature` | 48 | | | |

Generation is deterministic (fixed seed `20260720`). Each table has an
independent random stream, and the point-in-time `current_*` projection columns
the SemanticModels depend on are computed exactly as in the reference dataset.
The fixed **Northstar Systems** anchor (account `1001`, NA/Enterprise,
renewal `2026-09-15`, low adoption, elevated support) is preserved so the demo's
metrics stay meaningful. Invariants (e.g. Every `usage_daily`
`(account_id, feature_id)` has exactly one entitlement) are enforced during
generation. A violation aborts with an error rather than loading bad data.

## Usage

Normally run via the demo Makefile / script, which targets the FE NodePort:

```bash
make data-load          # from local/
# or:
scripts/data-load.sh
```

Directly:

```bash
cd local/data-gen
go run . --host 127.0.0.1 --port 9030 --user root --catalog iceberg
```

Flags: `--host` `--port` (default 9030) `--user` (root) `--password`
`--catalog` (iceberg) `--database` (saas_customer_health_demo)
`--batch-size` (1000).

Offline self-check. Generate everything and print row counts without
connecting to a database:

```bash
go run . --count-only
```

## Layout

```
main.go            flags, StarRocks connection, --count-only self-check
constants/         seed, window dates, and the fixed small-profile row counts
gen/               deterministic streaming generators (dims + facts + anchors)
loader/            catalog-owned CREATE TABLE + batched INSERT + row-count verify
```

