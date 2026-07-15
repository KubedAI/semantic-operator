# Demo: proving accuracy

Goal: show that the *same* LLM answering the *same* business questions is measurably more accurate and more consistent through the semantic layer than through raw text-to-SQL. The only variable is whether the LLM writes SQL or selects a certified metric.

Prerequisites: the stack deployed and the demo model applied (see [RUNBOOK.md](RUNBOOK.md) through step 8). The NL/benchmark parts additionally need a Claude model enabled in Amazon Bedrock in your region.

## 1. The single-question demo (most visceral)

```bash
export MCP_ENDPOINT=http://localhost:8090/mcp
export BEDROCK_MODEL_ID=<your enabled Claude model id>   # e.g. us.anthropic.claude-sonnet-4-5-...
make demo-nl QUESTION="What is our sales per employee by state?"
```

What the audience sees:

- **Raw path** — the LLM joins `store → store_sales → employees`; the join fans out and multiplies the employee denominator. Result: a confidently wrong number with plausible-looking SQL.
- **Semantic path** — the agent calls `list_metrics` → `query_metric` on the certified `store_productivity` metric. The planner compiles each side of the ratio as its own aggregation subquery (fan-out safe) and returns the correct number with the exact SQL.

Run two or three live, including a **paraphrase** of each:

```bash
make demo-nl QUESTION="What is our customer lifetime value?"
make demo-nl QUESTION="Average revenue per customer over their history?"   # paraphrase
```

The raw path gives *different answers to the same question reworded*; the semantic path is identical every time.

## 2. The quantified demo (the slide with numbers)

```bash
make bench          # ~90 phrasings × 2 paths → bench/RESULTS.md
# cost/time control:
go run ./bench/runner -limit 5 -out /tmp/results.md
```

`bench/RESULTS.md` reports three metrics per path — this is the accuracy story:

- **Accuracy** — % of phrasing runs matching hand-written ground truth (0.5% numeric tolerance).
- **Consistency across paraphrases** — % of questions where all phrasings return the same answer. This is where raw text-to-SQL collapses.
- **Hallucination rate** — failed runs that referenced nonexistent tables or columns.

The seed data is fixed and temperature is 0, so runs with the same model id are directly comparable and reproducible.

## 3. Show it without an LLM (deterministic + governance)

No Bedrock required — these hit the serving layer directly.

```bash
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &

# Same request twice → identical requestHash, second is a cache hit
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["item.i_category"],"filters":[{"field":"date_dim.d_year","op":"=","value":2001}]}' | jq '{requestHash, cachedResult, rowCount}'

# Governance: analyst asking for a PII column → HTTP 403 at compile time
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["customer.c_email_address"]}'
```

Every emitted statement carries `/* semantic-layer model=<m> version=<v> request=<sha> */`, so you can trace any StarRocks audit-log line back to the exact semantic request.

## 4. The BI-consistency demo (optional)

Show the governed view returns the same numbers as the API path:

```sql
SELECT * FROM default_catalog.semantic_views.sales_by_category_year
ORDER BY 1 LIMIT 8;
```

Put this side by side with the REST result for the same metric — they match to the cent, because both are the same compiled SQL. In Superset, put a naive fan-out query next to the governed view in SQL Lab (see [../demo/superset/README.md](../demo/superset/README.md)).

## Framing that lands

> "Same question, same model, temperature 0. The only variable is whether the LLM writes SQL or selects a metric."

Then show the two SQL statements next to each other: one hand-rolled and wrong, one compiled and traceable.
