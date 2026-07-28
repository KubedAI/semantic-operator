---
title: Where ontology fits
description: What an ontology is, what the semantic model can and cannot express today, and how the two would work together.
---

People who work with knowledge graphs ask why this project talks about semantic models and
never about ontology. It is a fair question. The two solve neighbouring problems and the
words are often used as if they meant the same thing. They do not.

## Structure and meaning are different problems

The model this operator compiles describes structure. It records which tables exist, which
columns they hold, how they join, which metrics are certified, what a metric is called in
everyday speech, and who is allowed to read what. That is enough to turn a request into one
correct SQL statement, which is the job it was built for.

An ontology describes meaning. It records what the things in your business actually are,
how they relate, and what has to be true about them. It is written to be reasoned over
rather than executed.

The difference shows up as soon as you ask a question the schema cannot answer. A model can
tell you that the `subscriber` table joins to the `customer` table on a key. It cannot tell
you that every subscriber **is** a customer. Nothing in a join says that. A person knows it
and writes queries accordingly. An agent does not.

## What the model cannot say today

Each of these is a real statement about a business that the current model has no way to
record.

- A subscriber is a kind of customer, so anything true of customers is true of subscribers.
- Cancellation is a state change of a subscription, not simply a row in a table.
- Net revenue is derived from orders, discounts, refunds, and chargebacks, and that
  derivation is part of what the number means.
- Refund rate is meaningful per order per day and meaningless at a customer snapshot grain.
- A failed payment often appears alongside churn, but appearing alongside is not causing.
- An agent may propose a retention offer and may not send one without a human approving it.
- Two fields with different names in two systems refer to the same business concept.

None of these are exotic. They are the things a good analyst holds in their head. Today they
survive as tribal knowledge, and the model cannot check them or hand them to an agent.

## Why this matters more with agents

A human reading a metric list fills in the missing meaning without noticing. They know a
refund rate at customer grain is nonsense, so they never ask for it.

An agent has no such instinct. It sees a metric and a dimension and combines them. The
result runs and returns a number, and nothing in the output says the combination was
meaningless. This is the same failure the semantic layer already fixes for arithmetic, one
level up. The compiler stops an agent computing a metric the wrong way. It cannot yet stop
an agent asking a question that has no valid answer.

Governance has the same gap. The model can say a role may not read a column. It cannot say
an action needs approval before anyone takes it.

## The pieces that already exist

This is a solved area with mature open standards, so there is nothing to invent.

[OWL](https://www.w3.org/TR/owl2-primer/) provides classes, properties, hierarchies, and
inference. It is how you state that a subscriber is a customer and have software draw the
consequences.

[SHACL](https://www.w3.org/TR/shacl/) provides constraints and validation. It is how you
state that every certified metric must name an owner, a unit, and a valid grain, and then
fail a pull request that omits one.

The two are complementary. OWL says what the domain means. SHACL checks whether a document
obeys the rules. A useful setup needs both.

[SKOS](https://www.w3.org/TR/skos-primer/) is the lighter option for the common case, which
is a glossary of concepts with preferred labels, synonyms, and broader and narrower terms.
Many teams need SKOS long before they need OWL.

## How this would help the operator

The interesting part is that the operator already has the right shape for it.

Validation happens at compile time and refuses to publish a model that does not hold up.
SHACL shapes are the same idea expressed over a graph, so ontology checks belong in the
reconcile loop next to the schema drift check that is already there. A model referring to a
retired concept would fail the same way a model referring to a dropped column fails now.

Serving already answers questions about the model over MCP. An agent can list metrics and
dimensions. It could also resolve a business term to a concept, explain where a metric comes
from, or ask which metrics a change would affect.

Authoring already imports meaning from DataHub. Glossary terms and concept identifiers are
more of the same work a steward has already done.

## The line we would not cross

An ontology should inform planning and help an agent reason. It should not rewrite certified
SQL while a query is running.

The whole value of this system is that a request compiles to one predictable statement that
a person approved the definition of. Inference in the query path would trade that away for
cleverness. The graph belongs beside the compiler, not inside it.

## Where this stands

Exploratory. Nothing described here is implemented, and the model gains nothing from
ontology until the parts above are real.

The direction is clear enough to state. [Apache Ossie](https://ossie.apache.org/) has
working groups covering ontology representation and catalog integration. Following that
standard beats inventing a parallel model that only this operator understands.

The likely stack is all open source and mostly things teams already run. Ossie stays the
analytics model. SKOS and OWL carry concepts and relationships. SHACL validates in CI.
DataHub remains the catalog and glossary. An RDF store such as
[Eclipse RDF4J](https://rdf4j.org/) or [Apache Jena](https://jena.apache.org/) comes in only
when a graph is genuinely needed. [Ontop](https://ontop-vkg.org/guide/) is worth knowing
about, because it answers graph queries over relational data without copying rows. That
suits a warehouse that already holds the facts.

The first useful step needs no graph database at all. Carrying concept identifiers, owners,
units, valid grains, and derivation links in the model itself, through the Ossie fields
meant for exactly that, would answer many agent questions on its own.

If this is something you need, say so on the repository. Interest is what moves it up the
list.

Next, read [how it works](/architecture) or [see it running](/start/quickstart).
