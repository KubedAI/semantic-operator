---
title: Ontology
description: How an ontology complements a semantic model, how Apache Ossie ontology support is evolving, and how Semantic Operator may support it in the future.
---

A semantic model defines how data can be queried. It records datasets, fields,
joins, dimensions, and certified metrics. An ontology describes what the things
in that data mean and how those business concepts relate.

<p class="so-key-point">A semantic model makes data queryable. An ontology gives that data shared business meaning.</p>

For example, a semantic model can say:

```text
subscriber.customer_id joins customer.customer_id
```

An ontology can say:

```text
Subscriber is a subtype of Customer
```

The first statement tells a SQL planner how two datasets join. The second tells
an agent what a Subscriber means. Both are useful, but they solve different
problems.

## How Apache Ossie ontology is evolving

The Apache Ossie community is developing an ontology specification alongside
the core semantic model specification. The current draft defines:

- entity concepts such as Customer, Employee, and Store,
- value concepts such as CurrencyAmount or CustomerId,
- inheritance between concepts,
- relationships, identifiers, and multiplicity,
- business constraints and derived concepts,
- mappings from ontology concepts to semantic-model fields.

The mappings are the bridge between conceptual meaning and queryable data.
Ossie currently describes `object_mappings`, `referent_mappings`, and
`link_mappings` that connect concepts and relationships to fields in one or
more semantic models. See the [Apache Ossie ontology
specification](https://github.com/apache/ossie/blob/main/ontology/ontology.md).

:::caution[This is an evolving community specification]
The ontology specification is currently a `0.2.0.dev0` draft. Its structure and
expression language may change as the Apache Ossie community develops it. The
design below is our current direction, not an implemented or committed API.
:::

## How this may fit Semantic Operator

An ontology can be shared by several semantic models, so it should not be
embedded in one `SemanticModel`. A likely Kubernetes design uses two additional
resources:

- `Ontology` owns concepts, relationships, and business rules.
- `OntologyBinding` maps those concepts to fields and metrics in one or more
  `SemanticModel` resources.

```text
Ontology
   |
   | conceptual meaning
   v
OntologyBinding
   |
   | validated field mappings
   v
SemanticModel
   |
   | physical datasets, joins, and metrics
   v
Trino or StarRocks
```

Semantic Operator could validate the ontology and its bindings, watch the
referenced semantic models, and publish a versioned compiled artifact. A broken
mapping would block the new version while the last valid version remained
available, following the same lifecycle used for semantic models today.

## How queries may work

Existing metric queries would remain unchanged and would not require an
ontology:

```text
query_metric
    |
    v
compiled SemanticModel
    |
    v
current planner
    |
    v
one governed SQL statement
```

A future ontology-aware query could first resolve business concepts through a
compiled ontology binding. The resolved request would then use the existing
semantic model and planner:

```text
concept request
    |
    v
compiled Ontology + OntologyBinding
    |
    | resolve concepts to certified fields and metrics
    v
compiled SemanticModel
    |
    v
current planner
    |
    v
one governed SQL statement
```

The two YAML documents would not be merged at query time. Ontology rules would
also not be handed to an LLM to turn into SQL. Semantic Operator would validate
and compile the supported mappings first. The existing deterministic planner
would remain the only component that generates SQL.

## Possible MCP tools

The first useful integration would improve discovery without changing SQL
generation:

- `list_ontologies`
- `list_concepts`
- `describe_concept`
- `list_concept_relationships`
- `resolve_concept`

These tools could help an LLM understand that Subscriber is a type of Customer
or that buyer refers to the Customer concept.

After ontology bindings and governance are proven, later tools could include
`list_concept_metrics` and `query_concept`. A concept query should return the
resolved semantic request, ontology version, semantic-model version, and SQL so
the result remains explainable and auditable.

Ontology support is not implemented today. The immediate priority is to follow
the Apache Ossie work and avoid creating a competing format. Once the community
schema stabilizes, support can begin with offline validation and discovery
before ontology mappings are allowed to influence query planning.
