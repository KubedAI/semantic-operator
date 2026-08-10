# Semantic Operator speaker notes

The slides carry one idea at a time. Use these notes for the explanation and transition.

## Slide 1: Stop agents from guessing SQL

AI agents are becoming a new interface to enterprise data. They can generate SQL, but a database schema does not explain what revenue means, which date defines a quarter, how returns are treated, or what a caller may see.

Semantic Operator gives agents a safer contract. Agents select certified business concepts. A deterministic planner produces the SQL, and governance is applied before that SQL exists.

This project runs Apache Ossie semantic models as an operational service on Kubernetes.

### Transition

To understand why this matters, start with a question that sounds simple.

## Slide 2: Business questions hide decisions

Ask, “What was revenue last quarter?” A person still needs to resolve several hidden decisions.

Which revenue definition applies? Which timestamp determines the quarter? Are refunds subtracted? Which joins preserve the correct grain? Can this caller see every region and customer?

Those decisions are business knowledge. They are rarely encoded in table and column names. If the knowledge is missing, every tool and every agent must reconstruct it.

### Transition

Valid SQL does not prove that those decisions were correct.

## Slide 3: Valid SQL can still be wrong

We compared direct text-to-SQL with semantic retrieval. The test used 30 business questions, three phrasings of each question, and temperature zero.

The direct text-to-SQL path produced 28 wrong answers across 90 prompts. Every query executed successfully. The failures were semantic, not syntactic. The model selected a plausible but incorrect metric, date, join, or filter.

This is the key problem. An agent can be confident, the database can accept the SQL, and the answer can still be wrong.

### Transition

We need a translation layer between business language and physical data.

### Sources

- Project benchmark methodology and results: `examples/retail/bench/RESULTS.md`

## Slide 4: What is a semantic layer?

Think of enterprise meaning as three connected layers.

The physical layer says where data lives. It contains tables, columns, engines, and catalogs.

The logical layer says what the data means for analysis. It defines datasets, fields, dimensions, metrics, relationships, and grain. For example, it defines revenue once instead of asking each consumer to invent it.

The conceptual layer says why those definitions matter. It describes business concepts, relationships, rules, and eventually reasoning. A Subscriber can be a type of Customer. A Refund can reduce Revenue.

Mappings connect conceptual knowledge to the logical model. Bindings connect the logical model to physical data.

Semantic Operator operationalizes the logical layer today. The conceptual ontology layer is the direction we are following with the Apache Ossie community.

### Transition

A shared logical layer is most valuable when its format is open and portable.

## Slide 5: Apache Ossie makes meaning portable

Apache Ossie is an incubating open source standard for semantic models. The project was formerly known as Open Semantic Interchange, or OSI.

An Ossie model can express datasets, fields, relationships, dimensions, metrics, and business context in a vendor-neutral YAML contract.

The example on the slide is deliberately small. The business term Revenue points to a certified metric named `total_sales`, and that metric has one approved definition. Agents, BI tools, and applications can reference the same definition.

The standard makes meaning portable. It does not by itself validate live schemas, publish runtime artifacts, enforce caller policies, or operate a query service. That is the gap Semantic Operator addresses.

### Transition

The standard defines meaning. The operator turns it into a running system.

### Sources

- [Apache Ossie](https://ossie.apache.org/)
- [Apache Ossie on GitHub](https://github.com/apache/ossie)

## Slide 6: Put the standard to work

The flow has three responsibilities.

Apache Ossie provides the portable semantic contract. Semantic Operator validates, compiles, governs, and plans requests from that contract. StarRocks or Trino executes the resulting SQL against the organization’s existing data platform.

The planner is deterministic. The same compiled model, request, dialect, and verified identity produce byte-identical SQL. The agent does not improvise joins or formulas.

This separation matters. The community standard remains portable. The Kubernetes operator handles operations. Existing query engines continue doing the work they are designed to do.

### Transition

Before a model can serve requests, teams need a practical way to create it.

## Slide 7: Start with metadata, finish with certified meaning

Writing a semantic model from a blank file is unnecessary work. `ossiectl` can discover physical structure from supported sources such as AWS Glue or a live engine schema. DataHub can enrich that scaffold with descriptions, glossary terms, ownership, and classifications.

Automation handles structure and existing metadata. It cannot decide the organization’s official revenue definition. A domain owner still certifies metrics, joins, synonyms, and governance policies.

The finished model is applied to Kubernetes as a `SemanticModel` custom resource.

This is a human-in-the-loop workflow. Machines reduce mechanical effort. People remain accountable for business meaning.

### Transition

Once submitted, the operator must protect production from incomplete or stale definitions.

## Slide 8: Bad models never replace good ones

The Kubernetes reconciliation loop provides a safety boundary.

First, the operator validates the model structure, metric grammar, and join graph. Next, it checks the physical bindings against the live engine schema to detect missing tables or columns. It then compiles a deterministic artifact and publishes that artifact for serving.

If validation or drift checking fails, the new version is not published. The last known good artifact keeps serving. This prevents a broken edit or schema change from silently changing production answers.

Kubernetes status and events show model readiness and failure reasons through the operational interface platform teams already use.

### Transition

Publication is only half the story. The request path must enforce the same meaning and policy every time.

## Slide 9: Agents choose meaning, never SQL

Agents use MCP and applications use REST, but both interfaces enter one service path.

The caller requests certified metrics, dimensions, filters, and time grain. Semantic Operator resolves the verified identity, authorizes metrics and columns, applies row policies, creates the plan, and emits one SQL statement for the query engine.

Governance runs before SQL exists. A prohibited metric or column is rejected instead of being fetched and hidden later. Row boundaries become part of the planned query.

The LLM is limited to selecting known business concepts. It never writes the SQL sent to StarRocks or Trino.

### Transition

That shared contract serves several groups without creating separate definitions for each tool.

## Slide 10: Who should use it?

Agent builders get trusted context and a constrained retrieval interface. They do not need to prompt an LLM with warehouse schemas and hope it chooses the right joins.

BI and analytics teams get consistent metrics across natural language experiences and conventional tools.

Data platform and governance teams get a Kubernetes-native lifecycle, live drift checks, identity-aware policies, deterministic SQL, and audit context.

All three groups use one certified model. That is the leverage: define and govern business meaning once, then reuse it across consumers.

### Transition

Metrics answer analytical questions. The next community challenge is richer business understanding.

## Slide 11: The ontology direction

Today the project focuses on deterministic semantic models: certified metrics, dimensions, joins, filters, and policies.

The Apache Ossie Ontology Working Group is exploring the conceptual layer above that model. This includes concepts, typed relationships, rules, and mappings into logical semantic elements.

That layer can help an agent understand that a Subscriber is a Customer, that a Refund affects Revenue, or that an Offer requires approval. It can also make the same conceptual knowledge portable across organizations and tools.

Our direction is to follow this community work and map ontology concepts into the existing deterministic planner. We should preserve the safety boundary. The ontology supplies richer context, while certified mappings and bounded planning still control what reaches the data engine.

### Transition

Now let us see the current end-to-end experience.

### Sources

- [Apache Ossie](https://ossie.apache.org/)
- Apache Ossie Ontology Working Group materials shared in the community channels

## Slide 12: Demo

Keep the demo centered on one business question: “What was revenue last quarter?”

1. Show the certified revenue metric and its physical bindings in the Ossie model.
2. Apply the `SemanticModel` and show that the operator validates and publishes it.
3. Ask the question through the agent or MCP path.
4. Show the semantic request and the single generated SQL statement.
5. Show the governed result.
6. If time permits, request a denied field or use a restricted role to demonstrate that policy is enforced before execution.

Close with the three promises the audience just saw: no guessed SQL, one certified meaning, and one governed path from agent to data.
