---
title: How Semantic Operator compares
description: How dedicated semantic-layer products approach the same problem, and what a standards-based Kubernetes operator changes.
---

Defining metrics once and making every consumer go through them is not a new idea. dbt
Semantic Layer, Cube, and Looker all do it, and they work. The difference here shows up in
three places where those products leave friction.

**Authoring is manual.** Someone writes out every dataset, column, join, and metric by hand,
then keeps it in step with a physical schema that keeps changing underneath them.

**Definitions are locked in.** Each product uses its own model format, so the semantics
cannot follow the data to another engine or another tool.

**Somebody has to run it.** The server that answers queries is a product to install,
monitor, and upgrade.

[Apache Ossie](https://ossie.apache.org/) fixes the second problem. It is a vendor-neutral
standard for semantic models, backed by a broad group of data companies, so the model is not
tied to one product's format. What a given agent, BI tool, or application can consume still
depends on the fields and interfaces each one supports. The standard fixes the format, not
the work. Models still have to be created, validated against a live schema, deployed,
versioned, governed, and served.

Semantic Operator takes a different shape. A structural scaffold is generated from your
catalog, so you fill in and certify the metrics and access rules rather than typing the whole
model by hand. It is self-hosted and Kubernetes-native. You still run the operator and server,
but their lifecycle is declarative, with drift checks, status conditions, and standard
rollout and upgrade practices. [What a semantic layer is](/start/introduction) covers the
problem, and [How it works](/architecture) covers the mechanics.
