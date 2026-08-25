---
title: DataHub, Polaris, and StarRocks on Kind 
description: The whole stack in a local kind cluster. An object store, an Iceberg REST catalog, a query engine, DataHub, and the operator
---

This runs the entire stack on one machine in a [kind](https://kind.sigs.k8s.io/) cluster, with
no cloud account. Garage provides S3-compatible object storage, Apache Polaris is the Iceberg
REST catalog, StarRocks is the query engine, the Semantic Operator governs the models, and
DataHub supplies discovery and business metadata.

The stack is self-contained. An Iceberg lakehouse sits on Garage, Polaris catalogs it,
StarRocks queries it, the Semantic Operator governs it, and DataHub makes it discoverable.


## Architecture

```
host ──► kind (1 node) ──┬─ Garage             S3-compatible object store (Iceberg data)
                         ├─ Postgres           shared: Polaris catalog + DataHub metadata
                         ├─ Polaris            Iceberg REST catalog on Garage (--no-sts)
                         ├─ StarRocks          shared-data: 1 FE + 1 CN; Iceberg external catalog → Polaris
                         ├─ Semantic Operator  reconciles SemanticModels → governed SQL/views over StarRocks
                         ├─ OPA                external first-gate authorizer the server calls before compiling
                         ├─ DataHub            GMS + frontend + OpenSearch + Kafka; Iceberg ingestion from Polaris
                         └─ Keycloak           OIDC issuer for DataHub browser login
```


## How it works

One agent answers a question by composing both MCP servers, with a strict split:

> **DataHub** says what the data means, where it came from, who owns it, and
> whether it's trustworthy. The **Semantic Operator** decides what may be queried
> and computes the certified metric. The agent never writes SQL.

Asked to *"prepare a renewal briefing for Northstar Systems,"* the agent:

1. Discovers the datasets in DataHub. Domain, owners, glossary, certification, freshness.
2. Reads the exact semantic mapping from DataHub structured properties.
3. Picks certified metrics by name. The operator compiles and runs the one governed SQL statement.
4. The operator denies disallowed fields before any SQL exists, and adds row filters (for example `region = 'NA'`).
5. Answers with certified values, DataHub context, and the operator's SQL/version/hash.

## Prerequisites

- Host prerequisites, on your PATH: Docker, `make`, `git`, a Go toolchain
  matching `go.mod`, `curl`, `openssl`, Bash, and the standard Unix utilities.
- Cluster CLIs: `kind`, `kubectl`, `helm`, and [`uv`](https://docs.astral.sh/uv/).
  Run `make tools` to fetch the pinned versions into `./bin` for your OS.
- Budget **~10 GB RAM** and several CPUs for the Docker VM.

## Run from the example directory

Every `make` target lives in this example directory. Clone the repository and
change into it first, and run all commands below from there:

```bash
git clone https://github.com/KubedAI/semantic-operator
cd semantic-operator/examples/stacks/kind/datahub-polaris-starrocks
```

## Offline / one-time fetch

Everything is pinned in [`deploy/versions.lock`](https://github.com/KubedAI/semantic-operator/blob/main/examples/stacks/kind/datahub-polaris-starrocks/deploy/versions.lock).
You fetch the images and Helm charts once. They stay cached on your
machine. A later cluster rebuild needs no new download.

Run these once:

```bash
make tools             # fetch pinned kind/kubectl/helm/uv into ./bin
make charts-vendor     # vendor the pinned Helm charts into deploy/*/charts
make charts-images     # list the chart images into images.txt
make images-pull       # pull all pinned images into the Docker cache
```

`data/` holds the Garage objects and the Postgres state. It is git-ignored. The
vendored chart tarballs are committed, so the stack is self-contained.

## Run order

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
   `mcp-server-datahub` from PyPI:

```bash
make datahub-mcp-build datahub-mcp-up
```

This exposes DataHub over MCP at `http://localhost:8091/mcp`, alongside the
Semantic Operator MCP at `http://localhost:8090/mcp`. Point your own agent at
both, as shown in [Use it from your own agent](#use-it-from-your-own-agent).

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
The `X-Semantic-Role` header is optional. The query path uses the
published model name `saas_revenue`.

```bash
curl -s -X POST localhost:8090/v1/models/saas_revenue/query \
  -H 'X-Semantic-User: demo' -H 'X-Semantic-Role: platform_analyst' \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["monthly_recurring_revenue"],"dimensions":["account.segment"]}'
```

It returns one row per segment, the emitted SQL, and provenance:

```json
{
  "columns": [
    "account.segment",
    "monthly_recurring_revenue"
  ],
  "rows": [
    [
      "Enterprise",
      "250741.13"
    ],
    [
      "Mid-Market",
      "442868.77"
    ],
    [
      "SMB",
      "336261.70"
    ]
  ],
  "sql": "/* ...",
  "model": "saas_revenue"
}

```

The one emitted SQL statement reads `FROM iceberg.saas_accounts_demo`, and the
reply has the model version and request hash.

Governance runs before any SQL exists. In the revenue model the
`platform_analyst` role may not read the contact PII or the contract money
fields, so this query is refused with HTTP 403:

```bash
curl -s -X POST \
  localhost:8090/v1/models/saas_revenue/query \
  -H 'X-Semantic-User: demo' -H 'X-Semantic-Role: platform_analyst' \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["monthly_recurring_revenue"],"filters":[{"field":"contract.contract_value","op":">","value":0}]}'

# {"error":"unauthorized: role \"platform_analyst\" may not read field \"contract.contract_value\""}
```

## External authorization (OPA)

Built-in governance is not the only gate. The demo also runs OPA as an external
authorizer, and the server calls it first, before it compiles a model to SQL. A
model opts in with `spec.governance.external.providerRef: account-opa`, and the
server reaches OPA at the address set in the operator values.

An example:

```
# allowed only for finance_analyst only
allow if {
	input.action == "query"
	input.model.name in account_models
	count(finance_only_requested) > 0
	"finance_analyst" in input.identity.roles
	is_finance
}
```

It restricts the revenue-retention metrics to a finance identity.
Ask for a retention metric as `platform_analyst`. OPA denies it before any SQL
exists, so the operator returns 403 and names the provider:

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

The policy receives the certified request. It sees the identity, the
model name and version, and the requested metrics, dimensions, and filters, and
it returns only allow or deny. To gate on external groups instead of a built-in role,
switch the server to JWT auth and match `input.identity.groups` in the policy.

### Governance roles

The role you send in `X-Semantic-Role` decides what the operator allows. Policy
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

## Use it from your own agent

The demo does not ship an agent. It exposes two MCP servers, and you point your
own MCP host at them. The examples below use Claude Code and Kiro CLI.

- The **Semantic Operator MCP** at `http://localhost:8090/mcp`. It offers
  `list_models`, `list_metrics`, `list_dimensions`, and `query_metric`. The
  client selects certified metrics and dimensions by name. The operator compiles
  and runs the one governed SQL statement. The client never writes SQL.
- The **DataHub MCP** at `http://localhost:8091/mcp`. It offers search, entity
  metadata, schema fields, lineage, glossary, and structured properties. The
  client uses it to discover assets and judge whether they are trustworthy.

Header auth is on, so you send the identity yourself. Send `X-Semantic-User` and
`X-Semantic-Role` to the operator MCP. Send `Authorization: Bearer local-no-auth`
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


### Ask questions

With both servers connected, ask questions that need discovery and a governed
metric together:

- "What can I analyze here? List the models, metrics, and dimensions."
- "What is our net revenue retention by segment?"
- "Prepare a renewal briefing for Northstar Systems."
- "Is `account_feature_monthly` safe to use?" DataHub marks it deprecated.

## Datasets

The demo data models a **B2B SaaS vendor's account portfolio** across four
domains: Revenue Operations, Product Analytics, Customer Operations, and a shared Data
Platform. The three SemanticModels (`saas_revenue`, `saas_adoption`, `saas_support`) compute
certified metrics over these tables. It is a small (~45k-row), deterministic synthetic dataset
generated by [the data generator](#the-data-generator).
<details>
  <summary>Tables</summary>

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

Most fact tables have both raw columns and point-in-time `current_*` columns.
The `current_*` columns are populated only inside the fixed windows (the revenue
current snapshot on `2026-06-01`, the adoption window in June 2026, and the support window
through `2026-06-28`) and are zero or NULL elsewhere. The SemanticModels aggregate the
`current_*` columns, so each metric reflects the fixed as-of point without a
runtime date filter.

</details>


## Cleanup

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