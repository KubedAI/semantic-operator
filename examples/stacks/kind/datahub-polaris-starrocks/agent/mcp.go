package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// headerTransport injects fixed headers on every MCP HTTP request. The
// operator MCP reads the caller principal and role from X-Semantic-User and
// X-Semantic-Role; the DataHub MCP
// reads a bearer token from Authorization.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	return t.base.RoundTrip(req)
}

// mcpServer is one connected MCP endpoint.
type mcpServer struct {
	name string
	sess *sdk.ClientSession
}

// Provenance is the audit trail captured from the last query_metric call:
// the deterministic SQL the planner emitted and how to trace it.
type Provenance struct {
	SQL          string `json:"sql"`
	Model        string `json:"model"`
	ModelVersion string `json:"modelVersion"`
	RequestHash  string `json:"requestHash"`
	RowCount     int    `json:"rowCount"`
}

// MCP connects to one or more MCP servers, merges their tools, and routes
// each tool call to the owning server.
type MCP struct {
	servers []*mcpServer
	route   map[string]*mcpServer
	// LastProvenance holds the provenance of the most recent query_metric.
	LastProvenance *Provenance
}

// connect opens one streamable-HTTP MCP session.
func connect(ctx context.Context, name, endpoint string, headers map[string]string) (*mcpServer, error) {
	client := sdk.NewClient(&sdk.Implementation{Name: "chd-agent", Version: "0.1.0"}, nil)
	transport := &sdk.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: headerTransport{http.DefaultTransport, headers}, Timeout: 120 * time.Second},
	}
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s (%s): %w", name, endpoint, err)
	}
	return &mcpServer{name: name, sess: sess}, nil
}

// NewMCP connects the operator MCP (required) and, if datahubEndpoint is set,
// the DataHub MCP (optional). It returns the merged tool set for the LLM.
func NewMCP(ctx context.Context, operatorEndpoint, role, datahubEndpoint, datahubToken string) (*MCP, []Tool, error) {
	m := &MCP{route: map[string]*mcpServer{}}

	op, err := connect(ctx, "operator", operatorEndpoint, map[string]string{
		"X-Semantic-User": "demo-agent",
		"X-Semantic-Role": role,
	})
	if err != nil {
		return nil, nil, err
	}
	m.servers = append(m.servers, op)

	if datahubEndpoint != "" {
		dh, err := connect(ctx, "datahub", datahubEndpoint, map[string]string{"Authorization": bearer(datahubToken)})
		if err != nil {
			return nil, nil, err
		}
		m.servers = append(m.servers, dh)
	}

	var tools []Tool
	for _, s := range m.servers {
		listed, err := s.sess.ListTools(ctx, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("list tools on %s: %w", s.name, err)
		}
		for _, t := range listed.Tools {
			if _, dup := m.route[t.Name]; dup {
				// First server to expose a name wins; skip collisions.
				continue
			}
			m.route[t.Name] = s
			tools = append(tools, Tool{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schemaToMap(t.InputSchema),
			})
		}
	}
	return m, tools, nil
}

// Call dispatches a tool call to its owning server and returns the textual
// result. It captures query_metric provenance as a side effect.
func (m *MCP) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	s, ok := m.route[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	var argMap map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return "", fmt.Errorf("bad arguments for %s: %w", name, err)
		}
	}
	out, err := s.sess.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: argMap})
	if err != nil {
		return "", err
	}
	text := flatten(out)
	if out.IsError {
		return "", fmt.Errorf("%s", text)
	}
	if name == "query_metric" {
		var p Provenance
		if err := json.Unmarshal([]byte(text), &p); err == nil && p.SQL != "" {
			m.LastProvenance = &p
		}
	}
	return text, nil
}

// Close ends all MCP sessions.
func (m *MCP) Close() {
	for _, s := range m.servers {
		_ = s.sess.Close()
	}
}

// schemaToMap converts an MCP tool input schema into a plain map for the
// Chat Completions "parameters" field via a JSON round-trip. A missing or
// empty schema becomes an empty object schema.
func schemaToMap(schema any) map[string]any {
	def := map[string]any{"type": "object"}
	b, err := json.Marshal(schema)
	if err != nil {
		return def
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || len(m) == 0 {
		return def
	}
	return m
}

func flatten(out *sdk.CallToolResult) string {
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

func bearer(token string) string {
	if token == "" {
		return ""
	}
	return "Bearer " + token
}
