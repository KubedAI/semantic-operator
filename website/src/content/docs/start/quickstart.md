---
title: Quickstart
description: Run the whole stack locally on kind with one command, then run a governed query.
---

This runs the operator, a query engine, and a governed model on your own machine in a local
[kind](https://kind.sigs.k8s.io/) cluster.

## Prerequisites

Install these and make sure Docker is running:

- [Docker](https://docs.docker.com/get-docker/), [kind](https://kind.sigs.k8s.io/), `kubectl`, `helm`, `make`, `git`, `curl`, and a Go toolchain matching `go.mod`.
- `jq` is optional; drop `| jq` from the commands below if you do not have it.
- Roughly 4 GB of memory free for the cluster.

Clone the repository and run every command from its root:

```bash
git clone https://github.com/KubedAI/semantic-operator
cd semantic-operator
```

## 1. Stand up the stack

```bash
make quickstart
```

That one command:

- creates a local kind cluster,
- deploys a plaintext Trino,
- builds the operator and server images and installs them with header auth and no external providers,
- loads a small retail dataset into Trino,
- applies the retail model and waits for it to publish.

The first run takes a few minutes because it builds the images. It is done when the model
reports `VALIDATED=True PUBLISHED=True DRIFT=False`.

## 2. Run a metric query

The request names certified metrics and dimensions, not prose, and the server plans and runs
it. Producing that request from a question is the agent's job, over MCP (below).

Point `kubectl` at the repo-local kubeconfig, port-forward the server, and send both a user
and a role, since header auth means you supply the identity.

```bash
export KUBECONFIG=$PWD/.kube/config
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090 &

curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: demo-user' -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' | jq
```

You get one row per state, and the response includes the exact SQL the compiler produced:

```json
{
  "columns": ["store.s_state", "store_productivity"],
  "rows": [
    ["CA", "5170.461466"], ["GA", "5788.077143"], ["IL", "2518.138246"],
    ["NY", "3071.262570"], ["TX", "2403.497766"], ["WA", "1916.452131"]
  ],
  "rowCount": 6,
  "requestHash": "8530d598edd07c561b1cc5aecd1337b7",
  "cachedResult": false,
  "sql": "/* semantic-layer model=tpcds_retail_model version=8da02bd3bade request=8530d598... */ WITH ..."
}
```

`store_productivity` is store sales over store headcount, a ratio across a join. A naive join
counts each store's headcount once per sale and returns a far smaller number. This certified
metric tells the planner to deduplicate the denominator before dividing.

Run it again and the `requestHash` matches, because the same request and model version always
compile to the same SQL. Result caching is optional and off here, covered in
[Configuration and deployment](/reference/configuration).

> **Header auth is for local use only.** It trusts the `X-Semantic-User` and
> `X-Semantic-Role` headers, so anyone who reaches the port can claim any identity. Keep it
> behind the `port-forward`. Production validates a JWT instead, described in
> [Identity and the engine](/architecture/identity).

## 3. See governance in action

That query returned a number, which a raw warehouse connection would have done too. The
difference is what the layer does around the number. The two roles used below come from the
model's `governance` block:

```yaml
governance:
  roles:
    - name: analyst
      allowMetrics: ["*"]
      denyFields: ["customer.c_email_address"]
    - name: tx_analyst
      allowMetrics: ["*"]
      denyFields: ["customer.c_email_address"]
      rowFilters:
        - dataset: store
          predicate: "s_state = 'TX'"
```

**It refuses what a role may not read, before any SQL exists.** Ask, as an `analyst`, for a
column that role is denied:

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: demo-user' -H 'X-Semantic-Role: analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["customer.c_email_address"]}' | jq
```

```json
{"error":"unauthorized: role \"analyst\" may not read field \"customer.c_email_address\""}
```

The request is rejected with a 403 while the statement is still being compiled, so no SQL is
generated and there is nothing that could leak the column. A broadly privileged raw
connection could return it unless the engine enforced equivalent controls.

**It scopes the answer to who is asking.** The `tx_analyst` role has a row filter. Run the
same question you ran in step 2 as that role instead:

```bash
curl -s -X POST localhost:8090/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: demo-user' -H 'X-Semantic-Role: tx_analyst' -H 'Content-Type: application/json' \
  -d '{"metrics":["store_productivity"],"dimensions":["store.s_state"]}' | jq
```

Step 2 returned every state. This returns only Texas, because the role's `s_state = 'TX'`
filter was compiled into the SQL. Nobody added a `WHERE` clause to the request.

Both requests used the same certified definition, and the caller's role decided which fields
and rows the compiler allowed. The [benchmark](/examples/benchmark-results) shows the other
half of the story. Raw text-to-SQL was wrong in 28 of 90 trials, and every wrong query ran
without an error.

## MCP, REST, and SQL views

You reached the model over REST above. The same certified definitions are open two other ways.

- **AI agents** connect over [MCP](https://modelcontextprotocol.io/) at
  `http://localhost:8090/mcp`. An agent calls `list_metrics` and `list_dimensions` to read the
  certified vocabulary, then `query_metric` by name, so it selects a definition rather than
  writing SQL. The governance and determinism from the previous step apply. A
  denied field is still refused and a role's row filter is still enforced. Any MCP host that
  supports Streamable HTTP can connect, using a hosted or local model.
  Optionally, if [Claude Code](https://code.claude.com/docs) is installed and
  the port-forward from step 2 is still running, register the server from the repository root.

  ```bash
  claude mcp add --transport http --scope project semantic-layer http://localhost:8090/mcp \
    -H "X-Semantic-User: demo-user" -H "X-Semantic-Role: analyst"
  ```

  `--scope project` applies to this repository only, not your whole machine, and writes a
  `.mcp.json` you can remove later. Run `/mcp` in Claude Code to confirm the connection, then
  ask a question:

  - "What can I analyze with this model? What metrics and dimensions are available?"
  - "What's store productivity by state?"
  - "What's the customer lifetime value?"

- **Applications** use REST, as above. `POST /v1/models/{model}/sql` returns the SQL for a
  request without running it, which helps in review and tests.
- **BI tools** read the governed SQL views the operator created in the engine, with no server
  in the path.

## Tear down

```bash
kind delete cluster --name semantic-operator-dev
```

If you registered the server with Claude Code, remove it too, since it persists in
`.mcp.json`:

```bash
claude mcp remove semantic-layer
```

## Next

- [Retail on Glue and StarRocks](/examples/glue-starrocks) runs the same idea on a cloud engine, with a verification after every step.
- [How it works](/architecture) explains what the compiler is doing.
- [The benchmark](/examples/benchmark-results) is the raw-SQL-versus-semantic-layer comparison behind the numbers on the landing page.
