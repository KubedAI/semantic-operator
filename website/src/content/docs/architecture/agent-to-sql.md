---
title: How an agent request becomes SQL
description: What an LLM sends over MCP, how it selects certified metrics, and how the semantic server generates SQL without an LLM.
---

Semantic Operator separates understanding a question from writing SQL.

<p class="so-key-point">The LLM selects certified names. The Semantic Server generates SQL.</p>

The LLM never sends SQL or a metric formula to the server.

## What the agent sends

The Semantic Server exposes four MCP tools.

| Tool | Purpose |
|---|---|
| `list_models` | Lists the published models the caller may use |
| `list_metrics` | Lists the certified metrics the caller may query |
| `list_dimensions` | Lists the dimensions the caller may group or filter by |
| `query_metric` | Runs a structured semantic request |

Suppose a user asks:

> What was monthly recurring revenue by region last quarter?

The agent first discovers the vocabulary it is allowed to use. It calls `list_models` if
several models are published, followed by `list_metrics` and `list_dimensions`. Those tools
return names, descriptions, and synonyms from the model. Dimensions also include their data
types and whether they are time dimensions.

The LLM uses that metadata to map the user's words to certified names. For example,
"monthly recurring revenue" may match the metric `mrr`, and "region" may match
`account.region`. The agent then calls `query_metric` with a structured request:

```json
{
  "model": "saas_revenue",
  "metrics": ["mrr"],
  "dimensions": ["account.region", "calendar.month"],
  "filters": [
    {
      "field": "calendar.quarter",
      "op": "=",
      "value": "2026-Q2"
    }
  ],
  "orderBy": [
    {
      "field": "mrr",
      "direction": "desc"
    }
  ],
  "limit": 100
}
```

The request contains only certified metric and dimension names, filters, ordering, and a
limit. It does not contain a table name, join, aggregation, metric expression, or SQL.

Discovery is governed. Metrics and dimensions denied to the caller are omitted from the
tool results. Raw metric expressions are also hidden by default. They can be exposed for a
trusted debugging client, but an agent does not need them to select a metric.

## How the server generates SQL without an LLM

The SQL is not invented at query time. A data or analytics engineer has already defined the
model and reviewed its business meaning. The compiled model contains:

- the expression for each certified metric,
- the physical table and column behind each field,
- the relationships and join keys between datasets,
- primary keys used to prevent join fan-out,
- field types and time dimensions,
- row, column, and metric access policies.

When `query_metric` arrives, the server follows the same fixed pipeline every time.

1. It resolves the requested names against the compiled model.
2. It checks that one role permits the complete request. A rejected request returns 403
   before any SQL exists.
3. It finds the smallest join tree needed by the requested metrics, dimensions, and filters.
4. It expands the stored metric definitions into aggregations. Ratio metrics use separate,
   primary-key-deduplicated aggregations when a direct join would multiply one side.
5. It adds dimensions, filters, time grain, row policies, ordering, and the limit.
6. It asks the Trino or StarRocks dialect emitter to render exactly one SQL statement.
7. It executes that statement and returns the rows together with the SQL, model version, and
   request hash.

This is ordinary compiler behavior. A compiler does not need an LLM to translate a typed
program into machine instructions. In the same way, the semantic planner does not need an
LLM to translate a bounded semantic request into SQL. The model supplies the definitions,
and the planner applies deterministic rules.

For the same model version, request, and caller identity, the generated SQL is byte
identical. The LLM may phrase or interpret a question differently, but once it selects the
same certified names, the Semantic Server produces the same query.

## Where the boundary sits

```text
User question
    |
    v
LLM or agent
selects certified names through MCP discovery
    |
    |  structured query_metric request, never SQL
    v
Semantic server
authorizes and compiles the request deterministically
    |
    |  exactly one governed SQL statement
    v
Trino or StarRocks
```

Applications can send the same structured request over REST. BI tools can use governed SQL
views. All three surfaces use the same model and planner.
