---
title: Everything on your laptop
description: The whole stack in a local kind cluster. An object store, an Iceberg REST catalog, a query engine, DataHub, and the operator, with no cloud account.
---

This runs the entire stack on one machine in a [kind](https://kind.sigs.k8s.io/) cluster, with
no cloud account. Garage provides S3-compatible object storage, Apache Polaris is the Iceberg
REST catalog, StarRocks is the query engine, the Semantic Operator governs the models, and
DataHub supplies discovery and business metadata. It is the right starting point if you want
to understand the system before committing infrastructure to it.

The stack is self-contained. An Iceberg lakehouse sits on Garage, Polaris catalogs it,
StarRocks queries it, the Semantic Operator governs it, and DataHub makes it discoverable. The
data is a small synthetic dataset, about 45,000 rows.


## Architecture

```
host ──► kind (1 node) ──┬─ Garage             S3-compatible object store (Iceberg data)
                         ├─ Postgres           shared: Polaris catalog + DataHub metadata
                         ├─ Polaris            Iceberg REST catalog on Garage (--no-sts)
                         ├─ StarRocks          shared-data: 1 FE + 1 CN; Iceberg external catalog → Polaris
                         ├─ Semantic Operator  reconciles SemanticModels → governed SQL/views over StarRocks
                         ├─ OPA                external first-gate authorizer the server calls before compiling
                         ├─ DataHub            GMS + frontend + OpenSearch + Kafka; Iceberg ingestion from Polaris
                         ├─ Keycloak           OIDC issuer for DataHub browser login
                         └─ Caddy gateway      local HTTPS (*.localtest.me) in front of DataHub and Keycloak
```


## How it works (through an agent's eyes)

One agent answers a question by composing both MCP servers, with a strict split:

> **DataHub** says what the data means, where it came from, who owns it, and
> whether it's trustworthy. The **Semantic Operator** decides what may be queried
> and computes the certified metric. The agent never writes SQL.

Asked to *"prepare a renewal briefing for Northstar Systems,"* the agent:

1. Discovers the datasets in DataHub. Domain, owners, glossary, certification, freshness.
2. Drops the deprecated `account_feature_monthly` on metadata alone, and flags the `support_ticket` freshness gap.
3. Reads the exact semantic mapping from DataHub structured properties. Never guessed from names.
4. Picks certified metrics by name. The operator compiles and runs the one governed SQL statement.
5. Sends its principal in `X-Semantic-User` and role in `X-Semantic-Role`. The operator denies disallowed fields before any SQL exists, and adds row filters (for example `region = 'NA'`).
6. Answers with certified values, DataHub context, and the Semantic Server's SQL, version, and hash. No invented "health score."

If DataHub is unavailable, discovery and trust questions fail rather than guess.

## Prerequisites

- Host prerequisites, on your PATH: Docker, `make`, `git`, a Go toolchain
  matching `go.mod`, `curl`, `openssl`, Bash, and the standard Unix utilities
  the scripts use (`awk`, `sed`, `grep`, `tr`, `tar`, `base64`, `cmp`, `cp`,
  `install`, `uname`). `go` builds the operator, server, and data generator.
  `openssl` mints the local gateway CA. No AWS CLI and no cloud account are
  needed, since Garage provides S3 locally.
- Cluster CLIs: `kind`, `kubectl`, `helm`, and [`uv`](https://docs.astral.sh/uv/).
  Run `make tools` to fetch the pinned versions into `./bin` for your OS and
  architecture (Linux and macOS, amd64 and arm64), or put your own on PATH.
  `uv` runs the DataHub ingest and enrich steps.
- **amd64 workloads.** The container images are amd64-only. On an Apple Silicon
  Mac they run under Docker's emulation, which works but is slower. The CLI
  binaries above are always host-native.
- Substantial resources. DataHub's OpenSearch + Kafka dominate.
  Budget **~10 GB RAM** and several CPUs for the Docker VM.
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

## Run from the example directory

Every `make` target lives in this example directory. Clone the repository and
change into it first, and run all commands below from there:

```bash
git clone https://github.com/KubedAI/semantic-operator
cd semantic-operator/examples/stacks/kind/datahub-polaris-starrocks
```

## Offline / one-time fetch

Everything is pinned in [`deploy/versions.lock`](https://github.com/KubedAI/semantic-operator/blob/main/examples/stacks/kind/datahub-polaris-starrocks/deploy/versions.lock).
You fetch the images and Helm charts once, with network. They stay cached on your
machine. A later cluster rebuild needs no new download.

Run these once, with network:

```bash
make tools             # fetch pinned kind/kubectl/helm/uv into ./bin
make charts-vendor     # vendor the pinned Helm charts into deploy/*/charts
make charts-images     # list the chart images into images.txt
make images-pull       # pull all pinned images into the Docker cache
```

`make images-load` loads the cached images into the kind node. It runs in step 1,
after the node exists.

`data/` holds the Garage objects and the Postgres state. It is git-ignored. The
vendored chart tarballs are committed, so the stack is self-contained.

## Run order (each step is stood up and verified before the next)

Do the one-time fetch above first. Then run the steps in order.

1. Create the kind cluster and load the cached images into the node:

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

6. Build and install the operator and server, deploy the OPA first-gate
   authorizer, and apply the semantic models:

```bash
make operator-build operator-up opa-up models-apply
```

7. Bring up the local HTTPS gateway and Keycloak, then install DataHub and
   ingest and enrich the datasets. DataHub logs browser users in through
   Keycloak, so the gateway and realm come first (takes several minutes):

```bash
make gateway-up keycloak-up datahub-up datahub-ingest datahub-enrich
```

8. Build and deploy the DataHub MCP server. The build installs
   `mcp-server-datahub` from PyPI, so the first build needs network:

```bash
make datahub-mcp-build datahub-mcp-up
```

This exposes DataHub over MCP at `http://localhost:8091/mcp`, alongside the
Semantic Operator MCP at `http://localhost:8090/mcp`. Point your own agent at
both, as shown in [Use it from your own agent](#use-it-from-your-own-agent).

Step order mirrors the pipeline map at the top of the [`Makefile`](https://github.com/KubedAI/semantic-operator/blob/main/examples/stacks/kind/datahub-polaris-starrocks/Makefile).

## Verify the semantic layer

You can check the operator on its own, right after step 6, before you install
DataHub. Wait for all three models to report Validated, Published, and no drift:

```bash
kubectl get semanticmodels -n semantic-system
# NAME           VERSION        VALIDATED   PUBLISHED   DRIFT
# saas-adoption  2ecdf285bcf9   True        True        False
# saas-revenue   4384df1f86e5   True        True        False
# saas-support   1ccd2cfe2a01   True        True        False
```

Then run one governed query. Header auth requires the `X-Semantic-User` header.
The `X-Semantic-Role` header is optional. It falls back to the model's
`defaultRole`, which is `platform_analyst` here. The query path uses the
published model name `saas_revenue`, not the resource name `saas-revenue`:

```bash
curl -s -X POST localhost:8090/v1/models/saas_revenue/query \
  -H 'X-Semantic-User: demo' -H 'X-Semantic-Role: platform_analyst' \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["monthly_recurring_revenue"],"dimensions":["account.segment"]}'
```

It returns one row per segment, the emitted SQL, and provenance:

```json
{"columns":["account.segment","monthly_recurring_revenue"],
 "rows":[["Enterprise","140944.71"],["Mid-Market","323994.05"],["SMB","594460.63"]],
 "rowCount":3, "model":"saas_revenue", "modelVersion":"4384df1f86e5", "sql":"/* ... */ ..."}
```

The one emitted SQL statement reads `FROM iceberg.saas_accounts_demo`, and the
reply carries the model version and request hash.

Governance runs before any SQL exists. In the revenue model the
`platform_analyst` role may not read the contact PII or the contract money
fields, so this query is refused with HTTP 403:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST \
  localhost:8090/v1/models/saas_revenue/query \
  -H 'X-Semantic-User: demo' -H 'X-Semantic-Role: platform_analyst' \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["monthly_recurring_revenue"],"dimensions":["contract.contract_value"]}'
# 403
```

The `na_customer_success` role adds an automatic `region = 'NA'` row filter, so
the same metric grouped by region returns only the NA row.

## External authorization (OPA), the first gate

Built-in governance is not the only gate. The demo also runs OPA as an external
authorizer, and the server calls it first, before it compiles a model to SQL. A
model opts in with `spec.governance.external.providerRef: account-opa`, and the
server reaches OPA at the address set in the Helm values. Both gates must
allow a request. If OPA is unreachable, the request fails closed with 503.

`make opa-up` loads the policy in
[`deploy/opa/policy.rego`](https://github.com/KubedAI/semantic-operator/blob/main/examples/stacks/kind/datahub-polaris-starrocks/deploy/opa/policy.rego).
It restricts the revenue-retention metrics to a finance identity, in one place,
for every model. A per-model `allowMetrics` list cannot do that without being
copied into each model.

Ask for a retention metric as `platform_analyst`. OPA denies it before any SQL
exists, so the Semantic Server returns 403 and names the provider:

```bash
curl -s -X POST localhost:8090/v1/models/saas_revenue/query \
  -H 'X-Semantic-User: demo' -H 'X-Semantic-Role: platform_analyst' \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["net_revenue_retention"],"dimensions":["account.segment"]}'
# {"error":"unauthorized: external provider \"account-opa\" denied action \"query\""}
```

Send the same request as `finance_analyst`. OPA allows it, then built-in
governance and the planner run as usual and the rows come back:

```bash
curl -s -X POST localhost:8090/v1/models/saas_revenue/query \
  -H 'X-Semantic-User: demo' -H 'X-Semantic-Role: finance_analyst' \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["net_revenue_retention"],"dimensions":["account.segment"]}'
```

The policy receives the certified request, never SQL. It sees the identity, the
model name and version, and the requested metrics, dimensions, and filters, and
it returns only allow or deny. To gate on Keycloak groups instead of a role,
switch the server to JWT auth and match `input.identity.groups` in the policy.

## Teardown

`make cluster-down` deletes the kind cluster but preserves the persisted host
state under `data/` (Garage objects + Postgres), so a rebuild reuses the loaded
data. To reset completely, remove it:

```bash
make cluster-down     # delete the kind cluster (data/ preserved)
rm -rf data/          # drop the persisted Garage + Postgres state for a clean slate
```

If you registered the MCP servers with Claude Code, remove them, since they
persist in `.mcp.json`:

```bash
claude mcp remove semantic-layer
claude mcp remove datahub
```

For Kiro CLI, delete the `.kiro/settings/mcp.json` you created in this directory.

## Datasets

The demo data models a **B2B SaaS vendor's account portfolio** across four
domains: Revenue Operations, Product Analytics, Customer Operations, and a shared Data
Platform. The three SemanticModels (`saas_revenue`, `saas_adoption`, `saas_support`) compute
certified metrics over these tables. It is a small (~45k-row), deterministic synthetic dataset
generated by [the data generator](#the-data-generator).

| Table | Domain | What it represents |
|---|---|---|
| `date_dim` | shared | Calendar, one row per day (`2024-07-01` → `2027-06-30`), with a surrogate key and year, quarter, and month. Every fact joins it for time grouping. |
| `account` | Customer Ops | The customer companies: `segment` (Enterprise/Mid-Market/SMB), `region`, `industry`, `csm_team`, `renewal_date`, and `lifecycle_status` (trial/active/paused/churned/renewal-due). Includes the fixed **Northstar Systems** anchor (`account_id=1001`). |
| `account_primary_contact` | Customer Ops | Exactly one primary contact per account (name, email, phone, title). These are **PII**, so value access is role-governed. One row per account, so contact joins never multiply fact values. |
| `plan` | Revenue Ops | Catalog of SaaS plans and SKUs: name, tier (Starter/Growth/Business/Enterprise), product family. |
| `contract` | Revenue Ops | One row per contract: start and end dates, negotiated discount %, annual rate, contract value. **Confidential** pricing fields. The contract end date drives the renewal horizon. |
| `product_feature` | Product Analytics | Catalog of licensed features and modules: name, product area, criticality. |
| `account_feature_entitlement` | Product Analytics | Current licensed capacity per `(account, feature)`: licensed, eligible, and adopted seats. The denominator for adoption and seat utilization. **Every `usage_daily` row matches exactly one entitlement.** |
| `subscription_monthly` | Revenue Ops | Monthly subscription snapshots (24 months per subscription): `status` + `mrr`, plus point-in-time `current_*` revenue fields (MRR/ARR/renewal-ARR and the GRR/NRR retention cohort) populated only on the current snapshot (`2026-06-01`). Source for MRR, ARR, GRR, NRR, and ARPA. |
| `usage_daily` | Product Analytics | Daily product-usage events at account-user-feature-day grain (the largest table): total/error event counts, plus current-window IDs and counters for June 2026. Source for active users/accounts, feature adoption, seat utilization, and error rate. Northstar is deliberately low-adoption. |
| `support_ticket` | Customer Ops | Support tickets: created date, requester email, subject, status, escalation and SLA flags, first-response and resolution hours. Current-period fields are evaluated at the **`2026-06-28` observation cutoff**. The feed intentionally stops ~48h before the as-of date, so support metrics carry a known freshness gap. **PII** fields are role-governed. |
| `account_feature_monthly` | Product Analytics | A monthly adoption aggregate per `(account, feature, month)` that looks plausible but is **deprecated and stale**, a state knowable only from DataHub metadata, never from its name, schema, or values. The demo agent is expected to reject it on metadata alone. |

Most fact tables carry both raw columns and point-in-time `current_*` columns.
The `current_*` columns are populated only inside the fixed windows (the revenue
current snapshot on `2026-06-01`, the adoption window in June 2026, and the support window
through `2026-06-28`) and are zero or NULL elsewhere. The SemanticModels aggregate the
`current_*` columns, so each metric reflects the fixed as-of point without a
runtime date filter.



## Use it from your own agent

The demo does not ship an agent. It exposes two MCP servers, and you point your
own MCP host at them. Any host that speaks Streamable HTTP works, hosted or
local. The examples below use Claude Code and Kiro CLI.

- The **Semantic Server MCP** at `http://localhost:8090/mcp`. It offers
  `list_models`, `list_metrics`, `list_dimensions`, and `query_metric`. The
  client selects certified metrics and dimensions by name. The Semantic Server compiles
  and runs the one governed SQL statement. The client never writes SQL.
- The **DataHub MCP** at `http://localhost:8091/mcp`. It offers search, entity
  metadata, schema fields, lineage, glossary, and structured properties. The
  client uses it to discover assets and judge whether they are trustworthy.

Header auth is on, so you send the identity yourself. Send `X-Semantic-User` and
`X-Semantic-Role` to the Semantic Server MCP. Send `Authorization: Bearer local-no-auth`
to the DataHub MCP, since auth is disabled locally. Scope the registration to
this directory so it does not touch the rest of your machine.

### Claude Code

Register both servers from this directory with `--scope project`. That writes a
`.mcp.json` here and applies to this directory only:

```bash
claude mcp add --transport http --scope project semantic-layer http://localhost:8090/mcp \
  -H "X-Semantic-User: demo-user" -H "X-Semantic-Role: platform_analyst"

claude mcp add --transport http --scope project datahub http://localhost:8091/mcp \
  -H "Authorization: Bearer local-no-auth"
```

Run `/mcp` in Claude Code to confirm both connections.

### Kiro CLI

Kiro CLI reads a workspace MCP file at `.kiro/settings/mcp.json`. Create it in
this directory:

```json
{
  "mcpServers": {
    "semantic-layer": {
      "url": "http://localhost:8090/mcp",
      "headers": {
        "X-Semantic-User": "demo-user",
        "X-Semantic-Role": "platform_analyst"
      }
    },
    "datahub": {
      "url": "http://localhost:8091/mcp",
      "headers": { "Authorization": "Bearer local-no-auth" }
    }
  }
}
```

Run `kiro-cli mcp list workspace` to confirm, then `/mcp` inside a chat.

Add `"autoApprove": ["*"]` to a server entry to skip the per-tool approval
prompt in this local demo. This is a client-side convenience only. The operator
still enforces governance, so a denied field is refused regardless.

### Ask questions

With both servers connected, ask questions that need discovery and a governed
metric together:

- "What can I analyze here? List the models, metrics, and dimensions."
- "What is our net revenue retention by segment?"
- "Prepare a renewal briefing for Northstar Systems."
- "Is `account_feature_monthly` safe to use?" DataHub marks it deprecated.

### Governance roles

The role you send in `X-Semantic-Role` decides what the Semantic Server allows. Policy
runs at compile time, so a denied field fails before any SQL exists, and a
role's row filter is compiled into the SQL:

- `platform_analyst` (the default). In the revenue model it cannot read the
  contact PII or the contract money fields. In the support model it cannot read
  the ticket requester email or subject. The adoption model adds no field
  denials, so it can read the per-user identifiers there.
- `finance_analyst`. In the revenue model it reads the contract money and is
  denied the contact PII. It is granted no metrics in the support or adoption
  models, so it cannot query those.
- `na_customer_success`. Adds an automatic `region = 'NA'` row filter in every
  model, and in the revenue model is also denied the contract money fields.

Field and row rules are defined per model, so a role's effect depends on the
model. Change the role header and ask the same question to see the difference.



## The data generator

This is a small, self-contained Go module that deterministically generates the SaaS
accounts demo dataset and loads it into the local StarRocks **Iceberg**
external catalog. It is intended for the **local** (kind) deployment only.

It is deliberately lightweight: its own `go.mod`, one dependency (the MySQL
driver StarRocks speaks), and **none** of the parent demo's operational
scaffolding. No AWS/S3, no Glue, no IAM preflight, no storage-location
management, no data profiles, no `_demo_manifest`/checksums, and no
force/reset. The REST catalog (Polaris) owns table locations, so the loader
just runs `CREATE TABLE IF NOT EXISTS` and batched `INSERT`s, then verifies each
table's row count.

## Dataset row counts

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
metrics stay meaningful. Invariants (for example, every `usage_daily`
`(account_id, feature_id)` has exactly one entitlement) are enforced during
generation. A violation aborts with an error rather than loading bad data.

## Usage

Normally you run it through the demo Makefile or script, which targets the FE NodePort:

```bash
make data-load          # from the example root
# or:
scripts/data-load.sh
```

Directly:

```bash
cd data-gen
CGO_ENABLED=0 go run . --host 127.0.0.1 --port 9030 --user root --catalog iceberg
```

Flags: `--host` `--port` (default 9030) `--user` (root) `--password`
`--catalog` (iceberg) `--database` (saas_accounts_demo)
`--batch-size` (1000).

Offline self-check. Generate everything and print row counts without
connecting to a database:

```bash
CGO_ENABLED=0 go run . --count-only
```

## Layout

```
main.go            flags, StarRocks connection, --count-only self-check
constants/         seed, window dates, and the fixed small-profile row counts
gen/               deterministic streaming generators (dims + facts + anchors)
loader/            catalog-owned CREATE TABLE + batched INSERT + row-count verify
```
