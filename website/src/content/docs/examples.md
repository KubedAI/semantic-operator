---
title: Choosing an example
description: Independent walkthroughs for retail and SaaS models across local and EKS deployment stacks.
---

The examples are independent. The EKS walkthroughs use the same five-table retail model so
you can compare engines and catalogs directly. The laptop walkthrough uses three SaaS models
to show how an agent composes semantic queries with richer DataHub context.

<p class="so-key-point">Choose the walkthrough that matches the infrastructure and behavior you want to evaluate.</p>

Each walkthrough contains its own run order. Read [Prerequisites](/examples/prerequisites)
once for the shared workstation, cluster, image, and credential requirements.

## Pick a stack

| Walkthrough | Model | Catalog | Engine | Good for |
|---|---|---|---|---|
| [Retail on Glue and StarRocks](/examples/glue-starrocks) | Retail | AWS Glue | StarRocks | The reference EKS path |
| [Retail on Glue and Trino](/examples/glue-trino) | Retail | AWS Glue | Trino | Comparing the same model on another engine |
| [DataHub, Polaris and Trino](/examples/datahub-polaris-trino) | Retail | Apache Polaris, enriched by DataHub | Trino | Deriving physical structure and importing business meaning |
| [Everything on your laptop](/examples/datahub-polaris-starrocks) | SaaS revenue, adoption, and support | Apache Polaris, enriched by DataHub | StarRocks | Multiple models, two MCP servers, OPA, and no cloud account |

## What each walkthrough proves

The end-to-end walkthroughs share four checks even though their datasets and infrastructure
differ.

**A model reconciles.** It validates, binds to real tables, passes a drift check against the
live schema, and publishes a versioned artifact.

**A certified metric returns a verified number.** The retail walkthroughs use
`store_productivity`, a ratio that needs fan-out-safe planning. The laptop walkthrough uses
certified SaaS metrics across three business domains.

**The same request is deterministic.** Repeating the same authorized request against the
same model version and identity produces the same request hash and SQL. A configured cache
can reuse the plan or result.

**Governance is enforced before SQL.** The examples exercise denied fields, row filters, or
an external OPA decision depending on the stack.

**Consumers share definitions.** Agents use MCP, applications use REST, and BI tools can
read governed views in the engine.

## Beyond the walkthroughs

[The flights model](/examples/flights) is a second model with no deployment attached, useful
for seeing how a different domain is structured.

[See identity propagation by hand](/examples/identity-walkthrough) is a curl walkthrough on
Trino. You mint a real Keycloak token and watch the same query return different results for
different users, with the engine enforcing policy under each caller's identity.

[Govern a model with OPA](/examples/opa-governance) is a curl walkthrough on Trino. You deploy
an Open Policy Agent decision engine and watch two authorization layers apply to one query,
the model's built-in governance and an external OPA policy.

[Benchmark results](/examples/benchmark-results) has the measured comparison between raw
text to SQL and the semantic layer, with the method and the per question verdicts.
