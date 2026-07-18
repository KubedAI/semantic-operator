// Package mcp is the agent adapter: an MCP server on stateless streamable
// HTTP exposing list_metrics, list_dimensions, and query_metric. The LLM
// never writes SQL; it selects certified metrics and dimensions and the
// planner does the rest.
package mcp

import (
	"context"
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KubedAI/ossie-semantic-operator/internal/governance"
	"github.com/KubedAI/ossie-semantic-operator/internal/planner"
	"github.com/KubedAI/ossie-semantic-operator/internal/serving"
)

// RoleHeader mirrors the REST adapter's identity header.
const RoleHeader = "X-Semantic-Role"

type listModelsIn struct{}

type listIn struct {
	Model string `json:"model,omitempty" jsonschema:"Semantic model name. Optional when exactly one model is published."`
}

type filterIn struct {
	Field  string `json:"field" jsonschema:"Dimension reference as dataset.field"`
	Op     string `json:"op" jsonschema:"One of = != < <= > >= IN NOT IN LIKE BETWEEN"`
	Value  any    `json:"value,omitempty" jsonschema:"Scalar comparison value"`
	Values []any  `json:"values,omitempty" jsonschema:"Values for IN / NOT IN / BETWEEN"`
}

type queryIn struct {
	Model      string     `json:"model,omitempty" jsonschema:"Semantic model name. Optional when exactly one model is published."`
	Metrics    []string   `json:"metrics" jsonschema:"Certified metric names from list_metrics. At least one."`
	Dimensions []string   `json:"dimensions,omitempty" jsonschema:"Group-by dimensions as dataset.field from list_dimensions"`
	Filters    []filterIn `json:"filters,omitempty" jsonschema:"Row filters applied before aggregation"`
	Grain      string     `json:"grain,omitempty" jsonschema:"Time grain: day, week, month, quarter, or year. Requires a time dimension in dimensions."`
	Limit      int        `json:"limit,omitempty" jsonschema:"Row limit"`
}

type queryOut struct {
	Columns      []string `json:"columns"`
	Rows         [][]any  `json:"rows"`
	RowCount     int      `json:"rowCount"`
	SQL          string   `json:"sql"`
	Model        string   `json:"model"`
	ModelVersion string   `json:"modelVersion"`
	RequestHash  string   `json:"requestHash"`
}

type listMetricsOut struct {
	Model   string               `json:"model"`
	Metrics []serving.MetricInfo `json:"metrics"`
}

type listDimensionsOut struct {
	Model      string                  `json:"model"`
	Dimensions []serving.DimensionInfo `json:"dimensions"`
}

type listModelsOut struct {
	Models []serving.ModelInfo `json:"models"`
}

// NewServer builds the MCP server over the shared service.
func NewServer(svc *serving.Service, version string) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{
		Name:    "ossie-semantic-layer",
		Title:   "Ossie Semantic Layer",
		Version: version,
	}, nil)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "list_models",
		Description: "List published semantic models.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, _ listModelsIn) (*sdk.CallToolResult, listModelsOut, error) {
		return nil, listModelsOut{Models: svc.Models()}, nil
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "list_metrics",
		Description: "List certified metrics with descriptions and synonyms. " +
			"Always ground the user's vocabulary here before querying: a phrase like " +
			"'revenue' or 'CLV' maps to exactly one certified metric.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in listIn) (*sdk.CallToolResult, listMetricsOut, error) {
		m, err := svc.Resolve(in.Model)
		if err != nil {
			return nil, listMetricsOut{}, err
		}
		return nil, listMetricsOut{Model: m.Name, Metrics: svc.ListMetrics(m)}, nil
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "list_dimensions",
		Description: "List groupable and filterable dimensions (dataset.field) with " +
			"types, synonyms, and time-dimension flags.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in listIn) (*sdk.CallToolResult, listDimensionsOut, error) {
		m, err := svc.Resolve(in.Model)
		if err != nil {
			return nil, listDimensionsOut{}, err
		}
		return nil, listDimensionsOut{Model: m.Name, Dimensions: svc.ListDimensions(m)}, nil
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "query_metric",
		Description: "Query one or more certified metrics grouped by dimensions with " +
			"optional filters and time grain. The semantic planner compiles the request " +
			"into deterministic, governed SQL and executes it on StarRocks. The response " +
			"includes the SQL for provenance. Do not write SQL yourself.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in queryIn) (*sdk.CallToolResult, queryOut, error) {
		m, err := svc.Resolve(in.Model)
		if err != nil {
			return nil, queryOut{}, err
		}
		preq := planner.Request{
			Metrics:    in.Metrics,
			Dimensions: in.Dimensions,
			TimeGrain:  in.Grain,
			Limit:      in.Limit,
		}
		for _, f := range in.Filters {
			preq.Filters = append(preq.Filters, planner.Filter{
				Field: f.Field, Op: f.Op, Value: f.Value, Values: f.Values,
			})
		}
		res, err := svc.Query(ctx, "mcp", m, preq, serving.IdentityFrom(ctx))
		if err != nil {
			return nil, queryOut{}, err
		}
		return nil, queryOut{
			Columns: res.Columns, Rows: res.Rows, RowCount: res.RowCount,
			SQL: res.SQL, Model: res.Model, ModelVersion: res.ModelVersion,
			RequestHash: res.RequestHash,
		}, nil
	})

	return srv
}

// Handler wraps the MCP server in the stateless streamable HTTP transport
// and injects the caller identity from headers into the request context.
func Handler(svc *serving.Service, version string) http.Handler {
	srv := NewServer(svc, version)
	h := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return srv },
		&sdk.StreamableHTTPOptions{Stateless: true},
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := governance.Identity{Role: r.Header.Get(RoleHeader)}
		h.ServeHTTP(w, r.WithContext(serving.WithIdentity(r.Context(), id)))
	})
}
