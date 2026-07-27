package main

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// Tool is a function the model may call, described with a JSON-schema
// parameter object. Names and schemas come straight from the MCP servers.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolExecutor runs a tool call and returns its textual result. The agent
// wires this to the MCP dispatcher.
type ToolExecutor func(ctx context.Context, name string, args json.RawMessage) (string, error)

// LLM wraps the official OpenAI Go SDK client, configured for an
// OpenAI-compatible endpoint (Amazon Bedrock's bedrock-mantle) via base URL +
// API key. The agent uses the Responses API.
type LLM struct {
	client openai.Client
	model  string
}

// NewLLM builds the SDK client. baseURL should be the "…/v1" root; the SDK
// appends the resource path (…/v1/responses) itself.
func NewLLM(baseURL, apiKey, model string) *LLM {
	return &LLM{
		client: openai.NewClient(
			option.WithBaseURL(normalizeBaseURL(baseURL)),
			option.WithAPIKey(apiKey),
		),
		model: model,
	}
}

// normalizeBaseURL accepts "…/v1", "…/v1/", "…/v1/responses", or
// "…/v1/chat/completions" and returns the "…/v1" base the SDK expects, so a
// pasted full endpoint URL doesn't produce a doubled path.
func normalizeBaseURL(u string) string {
	u = strings.TrimRight(u, "/")
	for _, suffix := range []string{"/responses", "/chat/completions"} {
		u = strings.TrimSuffix(u, suffix)
	}
	return strings.TrimRight(u, "/")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
