// Package mcp is the agent adapter: an MCP server on stateless streamable
// HTTP exposing list_metrics, list_dimensions, and query_metric. The LLM
// never writes SQL; it selects certified metrics and dimensions and the
// planner does the rest.
package mcp

import (
	"context"
	"net/http"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/planner"
	"github.com/KubedAI/semantic-operator/internal/serving"
	"github.com/KubedAI/semantic-operator/internal/serving/auth"
)

// Identity headers are trusted only in header mode; see internal/serving/auth.
const (
	PrincipalHeader = auth.PrincipalHeader
	RoleHeader      = auth.RoleHeader
)

type listModelsIn struct{}

type listIn struct {
	Model string `json:"model,omitempty" jsonschema:"Semantic model name. Optional when exactly one model is published."`
}

type filterIn struct {
	Field  string `json:"field" jsonschema:"Modeled field reference as dataset.field. The field does not need a dimension declaration."`
	Op     string `json:"op" jsonschema:"One of = != < <= > >= IN NOT IN LIKE BETWEEN"`
	Value  any    `json:"value,omitempty" jsonschema:"Scalar comparison value"`
	Values []any  `json:"values,omitempty" jsonschema:"Values for IN / NOT IN / BETWEEN"`
}

type orderByIn struct {
	Field     string `json:"field" jsonschema:"Requested certified metric or dimension to order by"`
	Direction string `json:"direction" jsonschema:"Sort direction: asc or desc"`
}

type queryIn struct {
	Model      string      `json:"model,omitempty" jsonschema:"Semantic model name. Optional when exactly one model is published."`
	Metrics    []string    `json:"metrics" jsonschema:"Certified metric names from list_metrics. At least one."`
	Dimensions []string    `json:"dimensions,omitempty" jsonschema:"Explicitly declared group-by dimensions as dataset.field from list_dimensions"`
	Filters    []filterIn  `json:"filters,omitempty" jsonschema:"Row filters applied before aggregation"`
	Grain      string      `json:"grain,omitempty" jsonschema:"Time grain: day, week, month, quarter, or year. Requires a time dimension in dimensions."`
	OrderBy    []orderByIn `json:"orderBy,omitempty" jsonschema:"Ordered requested fields. Add every requested dimension after a metric to guarantee stable Top-N ties."`
	Limit      int         `json:"limit,omitempty" jsonschema:"Row limit applied after ordering"`
}

func (in queryIn) plannerRequest() planner.Request {
	req := planner.Request{
		Metrics:    in.Metrics,
		Dimensions: in.Dimensions,
		TimeGrain:  in.Grain,
		Limit:      in.Limit,
	}
	for _, f := range in.Filters {
		req.Filters = append(req.Filters, planner.Filter{
			Field: f.Field, Op: f.Op, Value: f.Value, Values: f.Values,
		})
	}
	for _, order := range in.OrderBy {
		req.OrderBy = append(req.OrderBy, planner.OrderByClause{
			Field: order.Field, Direction: order.Direction,
		})
	}
	return req
}

type queryOut struct {
	Columns                  []string `json:"columns"`
	Rows                     [][]any  `json:"rows"`
	RowCount                 int      `json:"rowCount"`
	SQL                      string   `json:"sql"`
	Model                    string   `json:"model"`
	ModelVersion             string   `json:"modelVersion"`
	RequestHash              string   `json:"requestHash"`
	AuthorizationFingerprint string   `json:"authorizationFingerprint,omitempty"`
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

// NewServer builds the MCP server over the shared service. The resolver turns
// the caller carried on the request context into the engine credential.
func NewServer(svc *serving.Service, version string, resolver serving.CredentialResolver) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{
		Name:    "ossie-semantic-layer",
		Title:   "Ossie Semantic Layer",
		Version: version,
	}, nil)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "list_models",
		Description: "List published semantic models.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, _ listModelsIn) (*sdk.CallToolResult, listModelsOut, error) {
		return nil, listModelsOut{Models: svc.Models(serving.IdentityFrom(ctx))}, nil
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "list_metrics",
		Description: "List certified metrics with descriptions and synonyms. " +
			"Always ground the user's vocabulary here before querying: a phrase like " +
			"'revenue' or 'CLV' maps to exactly one certified metric.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in listIn) (*sdk.CallToolResult, listMetricsOut, error) {
		m, err := svc.Resolve(in.Model, serving.IdentityFrom(ctx))
		if err != nil {
			return nil, listMetricsOut{}, err
		}
		metrics, err := svc.ListMetrics(m, serving.IdentityFrom(ctx))
		if err != nil {
			return nil, listMetricsOut{}, err
		}
		return nil, listMetricsOut{Model: m.Name, Metrics: metrics}, nil
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "list_dimensions",
		Description: "List explicitly declared group-by dimensions (dataset.field) with " +
			"types, synonyms, and time-dimension flags.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in listIn) (*sdk.CallToolResult, listDimensionsOut, error) {
		m, err := svc.Resolve(in.Model, serving.IdentityFrom(ctx))
		if err != nil {
			return nil, listDimensionsOut{}, err
		}
		dims, err := svc.ListDimensions(m, serving.IdentityFrom(ctx))
		if err != nil {
			return nil, listDimensionsOut{}, err
		}
		return nil, listDimensionsOut{Model: m.Name, Dimensions: dims}, nil
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "query_metric",
		Description: "Query one or more certified metrics grouped by dimensions with " +
			"optional filters, time grain, ordering, and a row limit. For highest, lowest, " +
			"or Top-N questions, order by the requested metric and add every requested " +
			"dimension to guarantee stable ties before setting the limit. The semantic planner compiles " +
			"the request into deterministic, governed SQL and executes it on the configured " +
			"query engine. The response includes the SQL for provenance. Do not write SQL yourself.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in queryIn) (*sdk.CallToolResult, queryOut, error) {
		m, err := svc.Resolve(in.Model, serving.IdentityFrom(ctx))
		if err != nil {
			return nil, queryOut{}, err
		}
		preq := in.plannerRequest()
		caller := serving.CallerFrom(ctx)
		cred, err := resolver(ctx, caller.Token, caller.EngineUser, caller.Expiry)
		if err != nil {
			return nil, queryOut{}, err
		}
		res, err := svc.Query(ctx, "mcp", m, preq, serving.IdentityFrom(ctx), cred)
		if err != nil {
			return nil, queryOut{}, err
		}
		return nil, queryOut{
			Columns: res.Columns, Rows: res.Rows, RowCount: res.RowCount,
			SQL: res.SQL, Model: res.Model, ModelVersion: res.ModelVersion,
			RequestHash: res.RequestHash, AuthorizationFingerprint: res.AuthorizationFingerprint,
		}, nil
	})

	return srv
}

// Handler wraps the MCP server in the stateless streamable HTTP transport and
// injects the resolved caller identity into the request context. A request the
// authenticator rejects never reaches the MCP server, so an agent cannot call
// a tool without a verified identity. A nil authenticator falls back to header
// mode so tests and embedders keep working.
func Handler(svc *serving.Service, version string, authn *auth.Authenticator, resolver serving.CredentialResolver) http.Handler {
	srv := NewServer(svc, version, resolver)
	h := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return srv },
		&sdk.StreamableHTTPOptions{Stateless: true},
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ac auth.Authenticated
		if authn == nil {
			principal := strings.TrimSpace(r.Header.Get(PrincipalHeader))
			if principal == "" {
				http.Error(w, "unauthenticated: no "+PrincipalHeader+" header", http.StatusUnauthorized)
				return
			}
			ac.Identity = governance.Single(r.Header.Get(RoleHeader))
			ac.Identity.Principal = principal
		} else {
			var err error
			ac, err = authn.Authenticate(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
		}
		ctx := serving.WithIdentity(r.Context(), ac.Identity)
		ctx = serving.WithCaller(ctx, serving.Caller{
			Token: ac.Token, EngineUser: ac.EngineUser, Expiry: ac.Expiry,
		})
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}
