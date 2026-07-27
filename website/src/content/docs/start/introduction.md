---
title: What a semantic layer is
description: Why business meaning needs a home outside dashboards and prompts, how teams build that today, and what changes when the semantic layer becomes a Kubernetes workload.
---

## The knowledge that has no home

Every company has a set of facts that are not written in the database. Revenue is the sum
of extended sales price, not list price. A sale joins to a customer through a surrogate
key, not the customer id. Sales per employee has to count each store's headcount once, not
once per transaction. Analysts may not read email addresses.

None of that is in the schema. It lives in the heads of a few people and gets rewritten,
slightly differently, into every dashboard, notebook, and report. That is why two teams
present different revenue numbers in the same meeting and spend an hour working out whose
query was wrong.

A semantic layer gives that knowledge a home. It is a model that records the entities, the
joins between them, the certified metrics, the time grains, and the access rules. Every
consumer reads the same model, so they compute the same number.

## Why this became urgent

Disagreeing dashboards are an old problem and a slow one. Someone eventually notices and
sorts it out.

Pointing an AI agent at a warehouse changes the speed and the failure mode. The agent
reads the schema and writes SQL. It has no idea that headcount must be deduplicated before
it becomes a denominator, so it writes a join that multiplies each store's employee count
by the number of sales rows. The query runs. The number comes back. It is wrong by four
orders of magnitude and looks entirely reasonable.

We measured this. Across 30 business questions asked three ways each, raw text to SQL was
correct 69 percent of the time. Every single miss executed without an error and returned a
confidently wrong number. Nothing in the output suggested a problem.

Two more things go wrong at the same time. Ask the same question twice and you can get two
different queries, so the number moves between runs. And access rules applied after the
query has already run are easy to get wrong and easy to skip entirely.

## How teams build this today

The established answer is a semantic layer product. dbt Semantic Layer, Cube, and Looker
all let you define metrics once and make consumers go through them. They work.

The friction is in three places.

**The authoring is manual.** Someone writes out every dataset, every column, every join,
and every metric by hand. Then they keep it in step with a physical schema that keeps
changing underneath them.

**The definitions are locked in.** Each product uses its own model format, so the semantics
cannot follow the data to another engine or another tool.

**Somebody has to run it.** The server that answers queries is a product to install,
monitor, and upgrade.

[Apache Ossie](https://ossie.apache.org/) solves the second problem. It is a vendor neutral
standard for semantic models, backed by a broad group of data companies. One model document
can be read by agents, BI tools, and applications from any vendor.

The standard fixes the format. It does not fix the work. Models still have to be created,
validated against a live schema, deployed, versioned, governed, and served, and all of that
is still manual.

## What changes here

Semantic Operator turns the semantic layer into a Kubernetes workload. You declare the
model in Git and the platform does the rest.

### The model is generated, not typed

[ossiectl](/architecture/ossiectl) reads your catalog and writes the model. Every dataset
and field arrives populated. Candidate joins are inferred from key naming conventions.
Descriptions, business vocabulary, and PII classifications can be imported from DataHub, so
the work a data steward already did is reused rather than retyped.

What is left for a person is the part that genuinely needs judgement, which is certifying
the metric formulas and the access rules. The generated file marks those clearly and
refuses to invent them.

### The lifecycle is a reconcile loop

You apply the model like any other Kubernetes resource. [The operator](/architecture/operator)
validates it, binds it to physical tables, and checks those bindings against the live
database. If a column has disappeared, the operator refuses to publish the new version and
the last good one keeps serving. It compiles the model into a versioned artifact and
creates governed SQL views for BI tools.

Nothing about this is a special deployment process. It is `kubectl apply`, GitOps, and
status conditions, which is what a platform team already knows how to operate.

### Serving is a compiler, not a generator

[The semantic server](/architecture/server) turns each request into exactly one SQL
statement. The same request, from the same role, against the same model version, produces
byte identical SQL every time. Access rules are applied while the statement is being built,
so a request the caller is not allowed to make fails before it reaches the database.

An agent selects a certified metric by name over MCP. It never writes SQL. Applications use
REST. BI tools read the governed views directly. All three go through the same compiler, so
they cannot disagree.

## What you get

Determinism, which makes results cacheable, auditable, and worth tuning. Governance that
runs early enough to actually prevent a leak rather than filter one afterwards. And
portability, because the model is a plain Apache Ossie document that round trips byte for
byte, so the standard owns your semantics rather than this implementation.

Next, either [see it running](/start/quickstart) or [read how it works](/architecture).
