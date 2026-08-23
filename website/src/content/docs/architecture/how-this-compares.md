---
title: How Semantic Operator compares
description: How dedicated semantic-layer products approach the same problem and what a standards-based Kubernetes operator changes.
---

Defining metrics once and sharing them across consumers is not a new idea. Products such as
dbt Semantic Layer, Cube, and Looker already solve parts of this problem. Semantic Operator
focuses on a different deployment and portability model.

<p class="so-key-point">The distinguishing choice is a portable Apache Ossie model with a Kubernetes-native runtime.</p>

**Start from the live schema.** `ossiectl` can generate datasets, fields, and likely
relationships from Glue or a query engine. A person still defines and certifies the business
meaning, metrics, and access rules.

**Keep the model portable.** The model uses the Apache Ossie standard rather than a format
owned by Semantic Operator. Portability still depends on which parts of the standard each
consumer implements.

**Run it with Kubernetes.** Semantic Operator is self-hosted software. Teams operate it with
the same reconciliation, rollout, status, and observability patterns they use for other
Kubernetes workloads.

[Apache Ossie](https://ossie.apache.org/) provides a vendor-neutral format for semantic
models. It does not define how to generate, validate, deploy, govern, or serve those models.
Those operational concerns still need a runtime.

Semantic Operator supplies that runtime. It generates a structural scaffold, reconciles the
model against the live engine, publishes a versioned artifact, and serves governed requests
over MCP and REST. It can also publish governed SQL views for BI tools. [What a semantic
layer is](/start/introduction) covers the problem, and [How it works](/architecture) covers
the mechanics.
