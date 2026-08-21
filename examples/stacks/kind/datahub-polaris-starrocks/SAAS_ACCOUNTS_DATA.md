# SaaS account example data and DataHub enrichment

## Overview

This example creates a deterministic, entirely synthetic SaaS accounts dataset for an unnamed business-to-business SaaS vendor. It represents the vendor's portfolio of customer companies rather than the vendor's employees or internal operations. The data is designed to support realistic revenue, product-adoption, renewal-risk, and customer-support analysis while also demonstrating semantic modeling and metadata governance.

The generator writes 11 Iceberg tables to the `saas_accounts_demo` namespace through the StarRocks `iceberg` external catalog. The fixed seed (`20260720`) makes repeated runs produce the same rows. A complete generation contains **44,835 rows**.

All people, companies, contact details, and commercial values are fictional. Some synthetic columns are nevertheless classified as personally identifiable or confidential so the example can demonstrate realistic governance behavior.

## Generated tables

| Table | Rows | Grain and purpose |
|---|---:|---|
| `date_dim` | 1,095 | One calendar day from 2024-07-01 through 2027-06-30, with year, quarter, and month attributes. |
| `account` | 60 | One customer company, including segment, region, industry, assigned customer-success team, renewal date, and lifecycle status. |
| `account_primary_contact` | 60 | One current synthetic primary contact per account, including name, email, phone, and job title. |
| `plan` | 12 | One SaaS plan/SKU: the Engage, Insights, and Platform product families crossed with Starter, Growth, Business, and Enterprise tiers. |
| `contract` | 80 | One contract version, including account, term dates, negotiated discount, annual rate, and total contract value. |
| `product_feature` | 48 | One product feature: eight product areas crossed with six capabilities, plus a criticality classification. |
| `account_feature_entitlement` | 360 | One current account-feature entitlement. Every account receives six features with licensed, eligible, and adopted seat counts. |
| `subscription_monthly` | 1,680 | One subscription-month snapshot: 70 subscriptions across 24 months, with status, MRR, ARR, renewal, GRR, and NRR projection fields. |
| `usage_daily` | 36,000 | One account-user-feature-day observation, containing activity and error counts plus fixed-current-window projection fields. |
| `support_ticket` | 4,000 | One support ticket with optional feature attribution, requester and subject, status, escalation/SLA flags, and response/resolution times. |
| `account_feature_monthly` | 1,440 | One account-feature-month for four months. This is intentionally retained as a stale, deprecated aggregate for governance demonstrations. |

### Main relationships

`account` is the central customer dimension:

- contracts and monthly subscriptions belong to accounts;
- every account has one primary contact;
- entitlements connect accounts to product features;
- daily usage identifies the account, user, and entitled feature;
- support tickets belong to an account and can optionally identify a feature; and
- date keys connect subscription snapshots, usage observations, and support tickets to `date_dim`.

The generator checks important linkage invariants while producing the rows. For example, each usage record must match exactly one account-feature entitlement, subscription contracts must belong to the same account as the subscription, and monthly adoption cannot exceed eligible capacity.

## Customer and product distributions

Most customer records are generated from controlled distributions:

- Segments: Enterprise, Mid-Market, and SMB.
- Regions: NA, EMEA, APAC, and LATAM.
- Industries: Technology, Financial Services, Healthcare, Retail, Manufacturing, Media, Education, and Professional Services.
- Lifecycle states: trial, active, paused, churned, and renewal-due.
- Customer-success assignments: enterprise and growth teams split between North America and international regions, plus Digital Success.
- Contract terms: predominantly 12 months, with some 24- and 36-month terms; discounts range from 0% to 30%.
- Subscription histories: active, trial, paused, churned, newly started, contracted, and expanded trajectories are all represented.
- Product catalog: Analytics, Automation, Collaboration, Data Platform, Governance, Integrations, Security, and Workflow areas; each has Core, Dashboards, Exports, Monitoring, Orchestration, and Studio capabilities.

## Fixed analytical dates

The example deliberately uses fixed dates so demonstrations and expected results remain stable:

| Purpose | Fixed period |
|---|---|
| Overall as-of date | 2026-06-30 |
| Subscription snapshots | 2024-07-01 through 2026-06-01, monthly |
| Current subscription snapshot | 2026-06-01 |
| Retention baseline | 2025-06-01 |
| Renewal horizon | `[2026-07-01, 2026-10-01)` |
| Physical daily-usage coverage | 2026-03-04 through 2026-07-01, inclusive |
| Current adoption window | `[2026-06-01, 2026-07-01)` |
| Nominal current support window | `[2026-04-02, 2026-07-01)` |
| Support data observed through | 2026-06-28 |

The half-open notation means the start date is included and the end date is excluded. The support source intentionally has a two-day freshness gap before the overall as-of date; support results must not be described as complete through June 30.

## Northstar Systems fixture

Account `1001`, **Northstar Systems**, is a fixed, recognizable fixture used across otherwise independently generated tables. It provides a coherent customer-risk story:

- Enterprise technology customer in North America.
- Assigned to the Enterprise North America customer-success team.
- Lifecycle status `renewal-due`, with renewal on 2026-09-15.
- Synthetic primary contact Avery Chen, VP of Customer Operations.
- Six Analytics feature entitlements with deliberately low adoption.
- An existing subscription that contracts from $50,000 baseline MRR to $45,000 current MRR.
- A new $12,000 MRR subscription that is correctly excluded from the retention cohort.
- Total current MRR of $57,000 and ARR of $684,000.
- Existing-cohort GRR and NRR of 90%; the new subscription contributes to current revenue but not retention.
- 600 support tickets in the current observed period, including 120 open tickets, 150 escalations, and 400 tickets that met SLA.

Northstar is therefore suitable for questions about approaching renewals, weak product adoption, contraction risk, support pressure, and the difference between current revenue and cohort-based retention.

## Semantic analytical models

The physical tables support three semantic models under `deploy/models/`:

### Revenue (`saas_revenue`)

The revenue model joins subscriptions to accounts, contacts, plans, contracts, and dates. It exposes current MRR, current ARR, renewal ARR, GRR, NRR, and average revenue per account. Its generated current fields make fixed-snapshot metrics fan-out safe and prevent historical rows from contributing to current totals.

### Adoption (`saas_adoption`)

The adoption model combines daily usage with current account-feature entitlements, accounts, product features, and dates. It exposes active users, active accounts, feature adoption, licensed-seat utilization, product error rate, and active days. Entitlement capacity is current and unsnapshotted, so feature-adoption and licensed-seat-utilization metrics support account and feature dimensions but not date dimensions.

### Support (`saas_support`)

The support model joins tickets to accounts, optional product features, and dates. It exposes ticket count, open-ticket count, escalation rate, SLA attainment, average first-response time, and average resolution time. Its descriptions preserve the June 28 observation cutoff and the resulting two-day freshness warning.

The semantic model role policies also demonstrate different views of the same data: platform analysts cannot select PII or confidential fields, North America customer-success users receive an NA account filter, finance users can access revenue but not contact PII, and privacy administrators receive broad access. These compile-time semantic policies are separate from the descriptive DataHub classifications and operational tags added by enrichment.

## DataHub ingestion and enrichment flow

DataHub processing has two distinct stages:

1. **Ingestion discovers physical metadata.** `make datahub-ingest` runs the Iceberg recipe in `deploy/datahub/iceberg-recipe.yaml`. It reads the tables through the Polaris REST catalog and emits canonical DataHub datasets on platform `iceberg`, platform instance `account-demo-local`, environment `DEV`. The recipe ingests schema metadata only: profiling is disabled and no sample row values are collected.
2. **Enrichment overlays business metadata.** `make datahub-enrich` runs `scripts/datahub_enrich.py` with `deploy/datahub/enrichment-metadata.yaml`. It enriches the ingestion-created datasets rather than constructing competing dataset URNs.

The expected order is:

```bash
make data-load
make datahub-ingest
make datahub-enrich
```

A non-writing enrichment preview is available with:

```bash
make datahub-enrich ARGS=--dry-run
```

### How the enrichment job works

The job performs the following steps:

1. **Validate configuration.** It checks the metadata document's schema version, allowed fields, non-empty field descriptions, unique identifiers, expected asset names, cross-references, structured-property cardinalities, certification values, deprecation records, and transformation-job inputs and outputs. Invalid or dangling references fail before metadata is written.
2. **Discover canonical assets.** It searches DataHub using the configured platform, platform instance, environment, and database. Every one of the 11 expected tables must resolve to exactly one ingestion-created dataset URN. Missing or ambiguous matches stop the job.
3. **Write a discovery manifest.** The resolved URNs are sorted and written to `data/datahub-urn-manifest.json`; the same compact JSON is printed to standard output. This makes the physical-to-catalog binding explicit and inspectable.
4. **Build a deterministic plan.** Definitions and asset patches are sorted into a stable desired-state plan with duplicate operation keys forbidden. With the current metadata file, the plan contains 92 top-level convergent operations.
5. **Apply metadata change proposals.** Unless `--dry-run` is selected, the job emits DataHub metadata change proposals through GMS. It is designed to be safely rerun and converge on the same declared metadata rather than creating new physical datasets.

The script requires a DataHub GMS URL, token, and platform instance. Development GMS runs without authentication, so the wrapper supplies a non-empty ignored placeholder token by default. Log filtering redacts both the configured token and token-shaped text.

## Metadata added by enrichment

### Organizational structure

Four domains and matching owner groups organize the assets:

| Domain/group | Assets and responsibility |
|---|---|
| Revenue Operations | Plans, contracts, subscriptions, renewals, and recurring revenue. |
| Product Analytics | Features, entitlements, usage, adoption, and the deprecated monthly aggregate. |
| Customer Operations | Accounts, primary contacts, and support tickets. |
| Data Platform | Shared date dimension and technical ownership of transformation jobs. |

Each dataset receives a domain, technical group owners, and detailed documentation covering its grain, refresh cadence, as-of semantics, approved uses, and known limitations.

### Glossary

The job creates five top-level business-context nodes and 49 terms:

- Customer and Account Management: account, contact, segment, region, customer-success, lifecycle, renewal, and active/enterprise account concepts.
- Revenue and Finance: plans, contracts, subscriptions, commercial terms, fixed snapshots/cohorts, and the certified MRR, ARR, GRR, NRR, and renewal concepts.
- Product and Adoption: features, product areas, entitlements, seat concepts, usage/errors, the current adoption window, and certified adoption metrics.
- Customer Support: tickets, escalations, service levels, row-level response/resolution concepts, the observation cutoff, and certified support metrics.
- Data Classification: Public Information, Internal Information, Confidential Information, and Personally Identifiable Information.

The hierarchy organizes vocabulary by durable business context rather than by a particular analytical use case or team. Relevant terms are associated with datasets and fields so users can move from physical metadata to canonical business definitions.

### Column metadata

All 91 physical columns have non-empty descriptions covering physical meaning, units where applicable, null behavior, and fixed-window or as-of semantics. Foundational and metric glossary terms are associated at field level only where they provide governed business meaning.

Every column has exactly one base sensitivity classification:

| Classification term | Columns | Coverage |
|---|---:|---|
| Public Information | 5 | All calendar fields in `date_dim`. |
| Internal Information | 21 | Lower-sensitivity account attributes, primary-contact linkage, plan catalog, contract identifiers, and product catalog. |
| Confidential Information | 65 | Customer identity/renewal details, commercial facts, entitlements, monthly subscriptions, customer-specific usage, support, and the deprecated monthly adoption aggregate. |
| Personally Identifiable Information | 9 additional associations | Four primary-contact fields, three usage user identifiers, and two support-content fields; PII overlays the base sensitivity term. |

Customer-specific usage and every support field default to Confidential. PII is additionally applied to resolvable user/contact identifiers and potentially identifying support content.

### Classification, certification, and warnings

Five tags communicate technical or operational governance state:

| Tag | Applied to |
|---|---|
| `Derived` | 26 calculated, projected, or aggregated columns, including calendar parts, contract value, fixed-window `current_*` fields, and monthly active-user/event aggregates. |
| `Certified` | The ten supported current datasets. |
| `Deprecated` and `Stale` | `account_feature_monthly`, which is retained only for lineage and deprecation demonstrations. |
| `FreshnessWarning` | `support_ticket`, documenting that observations stop on June 28. |

`account_feature_monthly` is explicitly unverified and deprecated. Its deprecation note directs users to `account_feature_entitlement` with `usage_daily` for governed current adoption analysis. The other ten assets are certified, though `support_ticket` remains subject to its freshness warning.

Glossary terms provide controlled business vocabulary and sensitivity classification; tags communicate technical or operational state and aid discovery. Enforcement of row filters and denied fields occurs in the semantic models, not merely because a DataHub term or tag exists.

### Semantic linkage properties

The enrichment job defines five dataset structured properties:

- `semantic.models`: semantic models containing the dataset;
- `semantic.dataset`: the dataset name inside those models;
- `semantic.physical_table`: the fully qualified Iceberg table;
- `semantic.execution`: `semantic-operator`; and
- `semantic.mcp_tool`: the `query_metric` tool used for governed metric queries.

Supported datasets receive these values so DataHub users can trace physical assets to semantic definitions and execution. The deprecated `account_feature_monthly` table intentionally has no semantic model, execution, or MCP linkage.

### Source and transformation lineage

The job creates five metadata-only upstream source datasets:

| Source ID | Source dataset | Curated outputs |
|---|---|---|
| CRM system | `crm.customer_master` | `account`, `account_primary_contact` |
| Billing system | `billing.subscription_ledger` | `plan`, `subscription_monthly` |
| Contract system | `contracts.agreements` | `contract`, `subscription_monthly` |
| Product telemetry | `product.telemetry` | `product_feature`, `account_feature_entitlement`, `usage_daily`, `account_feature_monthly` |
| Support platform | `support.tickets` | `support_ticket` |

These placeholders use the `external-data` platform, `saas-accounts-sources` platform instance, and `DEV` environment. They represent source systems for lineage only; the example does not load physical source-system tables.

Four jobs in the `saas-accounts-curation` DataHub flow describe CRM, revenue, product, and support curation. Each job has source datasets as inlets, curated Iceberg datasets as outlets, and Data Platform ownership. Asset-level transformed lineage is also added directly, allowing users to inspect lineage from either the dataset or transformation-job perspective.

## Important limitations

- The data is synthetic and deterministic; it should not be treated as a production benchmark or statistically representative customer portfolio.
- Current metrics use fixed generated projection fields and dates. They are not arbitrary point-in-time calculations.
- The support dataset is observed only through 2026-06-28, despite an overall 2026-06-30 as-of date.
- Entitlement capacity is current rather than historically snapshotted, so capacity-based adoption metrics must not be grouped by date.
- `account_feature_monthly` is deliberately stale and deprecated and must not be selected for current analysis.
- DataHub ingestion does not profile the tables or ingest sample values.
- Enrichment depends on successful ingestion: it refuses to proceed unless every expected physical table resolves to exactly one canonical DataHub dataset.
