---
title: What a semantic layer is
description: Why business meaning needs a home outside dashboards and prompts, and what changes when the semantic layer becomes a Kubernetes workload.
---

## The knowledge that has no home

Every company has a set of facts that are not written in the database. Revenue is the sum of
extended sales price, not list price. A sale joins to a customer through a surrogate key, not
the customer id. Sales per employee has to count each store's headcount once, not once per
transaction. Analysts may not read email addresses.

None of that is in the schema. It lives in the heads of a few people and gets rewritten,
slightly differently, into every dashboard, notebook, and report. That is why two teams
present different revenue numbers in the same meeting and spend an hour working out whose
query was wrong.

A semantic layer gives that knowledge a home. It is a model that records the entities, the
joins between them, the certified metrics, the time grains, and the access rules. Every
consumer reads the same model, so they compute the same number.

## Why agents make it urgent

Disagreeing dashboards are an old, slow problem. Someone eventually notices and sorts it out.

Pointing an AI agent at a warehouse changes the speed and the failure mode. The agent reads
the schema and writes SQL. It has no idea that headcount must be deduplicated before it
becomes a denominator, so it writes a join that multiplies each store's employee count by the
number of sales rows. The query runs, the number comes back, and it is wrong by orders of
magnitude while looking entirely reasonable.

We measured this. Across 30 business questions asked three ways each, raw text to SQL was
correct 69 percent of the time, and every miss returned a confidently wrong number with no
error. Ask the same question twice and you can get two different queries, and access rules
applied after a query runs are easy to skip.

## How Semantic Operator works

Semantic Operator turns the semantic layer into a Kubernetes workload. You declare the model
in Git, and the platform generates, validates, versions, governs, and serves it. Three ideas
do the work.

- **The model is generated, not typed.** [ossiectl](/architecture/ossiectl) reads your
  catalog and writes the model, leaving only the parts that need human judgement: the metric
  formulas and the access rules.
- **The lifecycle is a reconcile loop.** You `kubectl apply` the model, and
  [the operator](/architecture/operator) validates it, checks it against the live database,
  and publishes a versioned artifact. A dropped column blocks the new version while the last
  good one keeps serving.
- **Serving is a compiler.** [The semantic server](/architecture/server) turns each request
  into exactly one SQL statement, applying access rules while the statement is built. Agents
  reach it over MCP, applications over REST, and BI tools over governed SQL views, all sharing
  the same certified definitions.

It builds on [Apache Ossie](https://ossie.apache.org/), a vendor-neutral standard for
semantic models. For how this differs from dbt, Cube, and Looker, see
[How Semantic Operator compares](/start/how-this-compares).

## Determinism, governance, and portability

Determinism, so results are cacheable and auditable. Governance that runs early enough to
prevent a leak rather than filter one afterward. And portability, because the model is Apache
Ossie rather than a private runtime format.

If you work with knowledge graphs and wonder how this relates to an ontology, that is a real
distinction, covered in [Semantic layers and ontologies](/architecture/ontology).

Next, either [see it running](/start/quickstart) or [read how it works](/architecture).
