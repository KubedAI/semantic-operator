# Teaching guide: how to onboard users

Three different people touch this system. Teach each by role.

## Mental model (teach this first, to everyone)

> **The LLM selects; the compiler writes.**

The LLM (or analyst, or app) only chooses *certified metrics and dimensions*. A deterministic planner turns that choice into exactly one governed SQL statement. Nobody hand-writes analytical SQL. Everything else hangs on this idea.

---

## A. The metric author (data engineer)

Owns the `SemanticModel` CR. This is the only role that edits Apache Ossie YAML.

1. **Start from the worked example:** `demo/model/semanticmodel.yaml`.
2. **Learn the authoring loop:**
   ```bash
   osictl validate -f model.yaml        # offline, instant feedback — no cluster needed
   git commit && git push               # ArgoCD applies it, or:
   kubectl apply -f model.yaml
   kubectl -n semantic-system get semanticmodels -w   # watch Validated / Compiled / Published / DriftDetected
   ```
3. **You maintain only metrics, joins, and synonyms.** Physical field lists come from Glue:
   ```bash
   osictl derive --database osi_demo --out model.yaml   # generates dataset stubs + candidate joins
   ```
   Candidate joins are emitted commented-out for a human to confirm; metrics and relationships are never auto-modified.
4. **Governance:** show `spec.governance` roles (e.g. `analyst`, `admin`), how `denyFields` makes a query return 403, and how `rowFilters` become WHERE conjuncts that participate in join planning.
5. **Golden rules to teach:**
   - A metric is a *certified definition*. If two teams disagree on "revenue," that is resolved in the CR, once.
   - Drift is detected, not silently served — a missing bound column flags `DriftDetected` while the last-good artifact keeps serving.
   - The `spec.osi` block round-trips byte-for-byte through `osictl`, so it stays portable Apache Ossie.

## B. The AI / app developer (consumer)

Never edits the model. Consumes the planner.

- **The contract** (one page): request `{metrics, dimensions, filters, identity}` → response `{columns, rows, sql, requestHash, ...}`.
- **For agents (MCP):** point the MCP client at the `/mcp` endpoint. The tools are self-describing:
  - `list_metrics`, `list_dimensions` — return names, descriptions, and `ai_context` synonyms so the model can ground user vocabulary.
  - `query_metric(metric, dimensions?, filters?, grain?, limit?)` — returns data *and* the emitted SQL.
  The agent learns the model by calling these; you do not prompt-stuff the schema.
- **For apps (REST):** the endpoints in [RUNBOOK.md](RUNBOOK.md) §8. Use `POST /v1/models/{m}/sql` for a dry-run that returns SQL without executing.
- **Teach:** pass identity (`X-Semantic-Role` for REST) — governance is enforced server-side; a forbidden field is a 403, not an empty result.

## C. The BI analyst (Superset)

Touches no YAML at all.

- Connect Superset to StarRocks over MySQL protocol.
- Use the governed views in `semantic_views.*` (e.g. `sales_by_category_year`) — **not** the raw Iceberg tables.
- **Teach the one rule:** the view has already computed the metric correctly (including fan-out-safe ratios). Building your own aggregate over raw tables is how you get the wrong number. See [../demo/superset/README.md](../demo/superset/README.md).

---

## Teaching assets to build (priority order)

1. **90-second screencast** of `make demo-nl` showing raw-wrong vs semantic-right. Sells it faster than any doc.
2. **One-page "request → SQL" cheatsheet** — the JSON contract plus the 403 example.
3. **15-minute quickstart** at the top of the RUNBOOK that stops after the first successful REST query.
4. **The mental-model one-pager** — "the LLM selects, the compiler writes."

## Suggested learning path

Everyone: read [OVERVIEW.md](OVERVIEW.md) → watch the screencast → run the §3 no-LLM demo in [DEMO.md](DEMO.md). Then branch by role into the section above. Authors finish with `osictl validate` on a metric of their own; consumers finish with one successful `query_metric` call; analysts finish with one governed view in a Superset chart.
