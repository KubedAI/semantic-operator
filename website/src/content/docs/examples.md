---
title: Choosing an example
description: One shared retail model, several deployment stacks. Pick the one that matches your platform and follow it end to end.
---

Every example uses the same retail model, a small TPC-DS subset with five tables and seven
certified metrics. What changes between them is the infrastructure underneath.

That is the point. The same model, unchanged apart from a catalog name, produces the same
numbers on StarRocks and on Trino. You can read one walkthrough and understand them all.

Start with [Prerequisites](/examples/prerequisites), which every walkthrough assumes.

## Pick a stack

| Walkthrough | Catalog | Engine | Good for |
|---|---|---|---|
| [Retail on Glue and StarRocks](/examples/glue-starrocks) | AWS Glue | StarRocks | The reference path. Start here if you have no strong preference. |
| [Retail on Glue and Trino](/examples/glue-trino) | AWS Glue | Trino | You already run Trino, or you want to see engine portability proved. |
| [DataHub, Polaris and Trino](/examples/datahub-polaris-trino) | Apache Polaris | Trino | An open lakehouse with a metadata platform supplying business meaning. |
| [Everything on your laptop](/examples/kind) | Apache Polaris | StarRocks | Trying the whole thing locally with no cloud account. |

## What each walkthrough proves

They all end at the same place, so you can judge whether this is worth adopting.

**A model reconciles.** It validates, binds to real tables, passes a drift check against the
live schema, and publishes a versioned artifact.

**A certified metric returns the right number.** Specifically `store_productivity`, a ratio
that spans a join and is wrong in the obvious hand written version. You will compare it
against ground truth.

**The same request is deterministic.** Ask twice and you get the same request hash, the same
SQL, and a cache hit on the second call.

**Governance is real.** An analyst asking for an email address gets a 403 before any SQL
reaches the database. A row filtered role sees only its own rows.

**BI works without the server.** Governed views live in the engine and any SQL client can
read them.

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
