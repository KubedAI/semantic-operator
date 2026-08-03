# Semantic Operator speaker notes

The slide carries the idea. These notes provide the explanation and the transition to the next slide.

## Slide 1: Semantic Operator

Reliable business answers depend on more than generating valid SQL. They depend on retrieving the data that matches the business definition of the question.

Semantic Operator makes that retrieval governed and deterministic. Given the same certified model, semantic request, and caller identity, it produces the same query plan and SQL.

### Transition

Why is this necessary? Because the language used by the business and the structure stored in the warehouse are not the same thing.

## Slide 2: The semantic gap

Business questions often sound simple, but retrieving the right data is difficult, even for a human. You need to decide what the metric means, where the data lives, how the tables relate, which timestamp applies, and which records count.

### Transition

Consider a simple example: What was revenue last quarter?

## Slide 3: Agent variability

If we ask an agent to answer it by writing SQL, it has to guess at all of those decisions.
An agent can generate SQL that looks reasonable and executes successfully. That does not prove that it selected the intended business definition.

Changing the phrasing can cause the agent to choose a different metric, date field, join path, or treatment of returns. Each choice can produce valid SQL and a different result.

We tested 30 business questions with three phrasings each at temperature zero. The raw text-to-SQL path returned 28 wrong answers across 90 prompts. Every incorrect query executed successfully.

The failure was not SQL syntax. The failure was uncodified business meaning.

### Transition

What if the agent did not need to reconstruct that meaning for every question? What if we defined it once and reused it?

## Slide 4: Define meaning once

Apache Ossie is an incubating, vendor-neutral specification for semantic models. It defines datasets, fields, metrics, relationships, and business context in a form that different tools can share.

The simplified YAML on the slide shows those concepts working together. It identifies the physical sales and date datasets, defines how the datasets relate, certifies the `total_sales` formula, and records the business phrases that refer to it.

It provides a common source of business meaning across AI, BI, and data tools. The project is developed by a broad industry community with participation from companies including Databricks, Snowflake, and Salesforce.

Instead of asking each tool to redefine revenue, we define and certify that meaning once.

### Transition

A specification can describe what the data means. But a specification is not a running system. How do we use it to answer a real question?

## Slide 5: Put the specification to work


Semantic Operator applies that meaning when data is requested. The existing Query Engines execute against the underlying data platform.

This connects a portable semantic contract to real infrastructure without asking every agent or application to recreate the definitions.

Apache Ossie defines the meaning. Semantic Operator applies it. Query Engines execute against the data.

### Transition

Now we can zoom out. There are two ways people interact with the system: creating the shared model and consuming it.

## Slide 6: Two paths

The architecture has two distinct flows connected by one semantic contract.

In the creation flow, metadata sources provide physical structure and existing business context. `ossiectl` combines those inputs into an Ossie YAML scaffold. People review and certify the business meaning, then Semantic Operator publishes the model so it is ready to use.

In the consumption flow, agents and applications ask for business concepts through MCP or REST. Semantic Operator uses the same published meaning to retrieve data from the existing StarRocks or Trino platform.

Both flows depend on the same model. Meaning is defined once and reused for every consumer.

### Transition

First, let us look more closely at how the model is created.

## Slide 7: Creating the model

Automation can discover physical structure and enrich it with business metadata already available to the organization. `ossiectl` combines schemas, descriptions, and terminology into a starting scaffold.

That scaffold is only a starting point. Existing context is useful input, but it is not certified business meaning. A person must define or verify the relationships, primary keys, metrics, synonyms, and governance policies that reflect how the organization actually uses its data.

Once the model is ready, it is applied to Kubernetes as a `SemanticModel`. Semantic Operator validates and publishes it. The model is then ready for agents and applications to consume.

Automation saves the mechanical work. People remain responsible for the meaning.

### Transition

Business meaning also includes who is allowed to use each part of the model. Those access rules are defined alongside the metrics and relationships.

## Slide 8: Access policies

Governance is part of the semantic contract rather than an afterthought applied to the result.

Metric policies determine which certified metrics a role may request. Column policies deny access to sensitive fields such as customer email. Row policies restrict the data by region, tenant, or another identity-specific boundary.

The same model can therefore serve several audiences without creating separate copies. An analyst can use approved metrics without seeing sensitive fields. A regional analyst can use those metrics over only the rows they are authorized to access.

Unauthorized metric or column requests are rejected. Row restrictions are applied before the query reaches the engine. Access is shaped by the verified identity of the caller.

### Transition

With both meaning and access defined, every consumer can use the same governed path.

## Slide 9: Consuming the model

An application can request certified metrics and dimensions through REST. An agent can select those same concepts through MCP.

Neither consumer needs to recreate the metric definition or understand the physical joins. Semantic Operator uses the published model, applies the relevant governance, retrieves the data from StarRocks or Trino, and returns the result.

The agent selects certified business concepts. It never writes the SQL that reaches the query engine.

This gives applications and agents different interfaces without giving them different definitions of the business.

### Transition

With the model published and the consumption path established, we can see the complete experience in the demo.

## Slide 10: Demo

Demonstrate the three promises from the presentation:

1. Show the certified `total_sales` metric and its relationship to the business term revenue.
2. Ask “What was revenue last quarter?” through MCP and show the returned result.
3. Show the equivalent REST request using the same certified concepts.
4. Demonstrate an access policy with a denied field or a role-scoped row filter.

Keep the demo focused on the business question, consistent meaning, and governed result. Avoid turning it into a deployment walkthrough.

### Transition

The demo shows the complete path from defined meaning to governed retrieval.

## Slide 11: Thank You

Close with the central takeaway:

Define meaning once. Govern it once. Use it everywhere.

Invite questions and discussion.

### Transition

Open the floor for questions.
