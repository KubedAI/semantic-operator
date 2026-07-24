# Interactive agent

A small, standalone Go program that answers customer-health questions by
composing two governed MCP servers:

- the **Semantic Operator MCP** (`localhost:8090/mcp`) — `list_models`,
  `list_metrics`, `list_dimensions`, `query_metric`. The agent selects certified
  metrics and dimensions; the operator compiles and runs the one governed SQL
  statement. **The agent never writes SQL.**
- the **DataHub MCP** (`localhost:8091/mcp`, optional) — search, entity metadata,
  schema fields, lineage, glossary, structured properties. The agent uses it to
  discover assets and judge whether they're trustworthy.

It calls an **OpenAI-compatible Responses API endpoint** via the official
`openai-go` SDK. With Amazon Bedrock that is the `bedrock-mantle` endpoint, so
only the base URL and key change. The model you pick must support the
**Responses API and function/tool calling** (the agent is driven entirely by
tool calls).

## Run

From the example root (`examples/datahub-polaris`), after the stack is up:

```bash
make datahub-mcp-build datahub-mcp-up      # deploy the in-cluster DataHub MCP
export OPENAI_BASE_URL="https://bedrock-mantle.<region>.api.aws/openai/v1"
export OPENAI_API_KEY="<your Bedrock API key>"
export BEDROCK_MODEL_ID="<your enabled model id>"
make agent                                 # interactive REPL
make agent ROLE=finance_analyst            # different governance role
make agent QUESTION="What is our NRR?"     # one-shot, then exit
```

> The Responses API on bedrock-mantle lives under **`/openai/v1`** (not `/v1`);
> the agent appends `/responses`. Use a model that supports the Responses API
> (e.g. the `gpt-5.6` family). The first call to a cold model can take ~60–90s;
> subsequent calls are ~1s.

Or run the binary directly from this directory:

```bash
go run . -role platform_analyst
```

## Configuration

| Env / flag | Default | Purpose |
|---|---|---|
| `OPENAI_BASE_URL` | — (required) | Responses API base URL (Bedrock `bedrock-mantle`, the `…/v1` root) |
| `OPENAI_API_KEY` | — (required) | Bearer key for the endpoint |
| `BEDROCK_MODEL_ID` / `-model` | — (required) | Model id |
| `SEMANTIC_MCP_URL` / `-operator-mcp` | `http://localhost:8090/mcp` | Semantic Operator MCP |
| `DATAHUB_MCP_URL` / `-datahub-mcp` | empty | DataHub MCP (empty ⇒ operator-only) |
| `DATAHUB_GMS_TOKEN` | `local-no-auth` | Bearer sent to the DataHub MCP (auth disabled locally) |
| `SEMANTIC_ROLE` / `-role` | `platform_analyst` | Sent as `X-Semantic-Role`; drives governance |
| `-question` | empty | Ask one question and exit |
| `-max-iters` | `12` | Max tool calls per question |

## Governance roles

The role travels in the `X-Semantic-Role` header. The operator enforces policy
at compile time — denied fields fail before any SQL exists; row filters are
injected automatically:

- `platform_analyst` (default) — PII **and** confidential contract $ denied.
- `finance_analyst` — sees contract $; PII denied.
- `na_customer_success` — contract $ denied + automatic `region = 'NA'` filter.

Ask the same question under two roles to see the difference.

## Layout

```
main.go     flags, env, REPL + one-shot
agent.go    multi-turn tool loop (bounded), conversation memory
llm.go      OpenAI-compatible Chat Completions client (net/http)
mcp.go      connect + merge operator/DataHub MCP tools, route calls, provenance
prompt.go   the authority-split system prompt
```

Standalone Go module (`chd.local/agent`). Dependencies: the MCP Go SDK and the
official `openai-go` v2 SDK. Build/test:

```bash
go mod tidy && go build ./... && go test ./...
```
