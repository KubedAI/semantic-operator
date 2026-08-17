package nlbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// SemanticSystemPrompt steers the model onto the certified path. It never
// sees table DDL and cannot write SQL; it can only select what the layer
// certifies.
const SemanticSystemPrompt = `You answer business questions using a governed semantic layer
exposed as tools. Workflow:
1. Call list_metrics and list_dimensions to see what is certified. Match the user's
   wording against names, descriptions, and synonyms.
2. Call query_metric with the chosen metric(s), dimensions, filters, grain, ordering,
   and limit. Filters use {"field":"dataset.field","op":"=","value":...}; use op
   "IN" with "values" for multiple values. For highest, lowest, or Top-N questions,
   order by the requested metric and add every requested dimension as an explicit
   tie-breaker before setting the limit.
3. Answer the question from the returned rows, briefly. Report numbers exactly as
   returned. If the layer refuses a request, say so; never invent numbers.`

// roleTransport injects the identity header on every MCP request.
type roleTransport struct {
	base http.RoundTripper
	role string
}

func (t roleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.role != "" {
		req.Header.Set("X-Semantic-User", "nlbench")
		req.Header.Set("X-Semantic-Role", t.role)
	}
	return t.base.RoundTrip(req)
}

// AnswerSemantic runs the with-semantic-layer path: a bounded Bedrock tool
// loop over the MCP server. The SQL in the result is the planner's, captured
// from the query_metric tool output.
func (r *Runner) AnswerSemantic(ctx context.Context, question string) PathResult {
	start := time.Now()
	res := PathResult{Path: "semantic"}

	client := sdk.NewClient(&sdk.Implementation{Name: "nlbench", Version: "0.1.0"}, nil)
	transport := &sdk.StreamableClientTransport{
		Endpoint:   r.MCPEndpoint,
		HTTPClient: &http.Client{Transport: roleTransport{http.DefaultTransport, r.Role}, Timeout: 120 * time.Second},
	}
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		res.Err = fmt.Sprintf("mcp connect: %v", err)
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res
	}
	defer func() { _ = sess.Close() }()

	listed, err := sess.ListTools(ctx, nil)
	if err != nil {
		res.Err = fmt.Sprintf("mcp list tools: %v", err)
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res
	}
	var tools []ToolDef
	for _, t := range listed.Tools {
		schema := map[string]any{"type": "object"}
		if t.InputSchema != nil {
			if b, err := json.Marshal(t.InputSchema); err == nil {
				_ = json.Unmarshal(b, &schema)
			}
		}
		tools = append(tools, ToolDef{Name: t.Name, Description: t.Description, InputSchema: schema})
	}

	// Capture the last successful query_metric structured output: that is
	// the planner's SQL and result set, the provenance of the answer.
	callTool := func(ctx context.Context, tc ToolCall) (string, error) {
		out, err := sess.CallTool(ctx, &sdk.CallToolParams{Name: tc.Name, Arguments: tc.Input})
		if err != nil {
			return "", err
		}
		text := flattenContent(out)
		if out.IsError {
			return "", fmt.Errorf("%s", text)
		}
		if tc.Name == "query_metric" {
			var qo struct {
				Columns []string `json:"columns"`
				Rows    [][]any  `json:"rows"`
				SQL     string   `json:"sql"`
			}
			if err := json.Unmarshal([]byte(text), &qo); err == nil && qo.SQL != "" {
				res.SQL, res.Columns, res.Rows = qo.SQL, qo.Columns, qo.Rows
			}
		}
		return text, nil
	}

	answer, trace, err := r.LLM.RunToolLoop(ctx, SemanticSystemPrompt, question, tools, callTool, 8)
	res.ToolCalls = trace
	if err != nil {
		res.Err = fmt.Sprintf("agent loop: %v", err)
	}
	res.Answer = answer
	res.ElapsedMs = time.Since(start).Milliseconds()
	return res
}

func flattenContent(out *sdk.CallToolResult) string {
	if out.StructuredContent != nil {
		if b, err := json.Marshal(out.StructuredContent); err == nil {
			return string(b)
		}
	}
	var text string
	for _, c := range out.Content {
		if t, ok := c.(*sdk.TextContent); ok {
			text += t.Text
		}
	}
	return text
}
