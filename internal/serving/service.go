package serving

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/KubedAI/semantic-operator/internal/cache"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/observability"
	"github.com/KubedAI/semantic-operator/internal/planner"
)

// QueryExecutor is the read-only engine surface the service needs.
type QueryExecutor interface {
	Query(ctx context.Context, sql string) ([]string, [][]any, error)
}

// Service is the one query path shared by the MCP and REST adapters:
// plan (governed, cached), execute (cached), report.
type Service struct {
	Store   *Store
	Dialect emitter.Dialect
	Cache   *cache.Cache
	DB      QueryExecutor
	Metrics *observability.Metrics
	Log     *slog.Logger
	Tracer  trace.Tracer
	// ExposeExpressions includes each metric's raw SQL expression in listings.
	// Off by default: the expression is the definition itself, and an agent
	// grounds perfectly well on the name, description, and synonyms. Turn it
	// on for debugging or for a trusted internal console.
	ExposeExpressions bool
	// Limits bound request shape and result size. A zero value means the
	// defaults, never "unbounded".
	Limits Limits
}

// MetricInfo is a listing entry: everything an agent needs to ground a
// user's vocabulary onto certified metrics.
type MetricInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Expression  string   `json:"expression,omitempty"`
	Synonyms    []string `json:"synonyms,omitempty"`
}

// DimensionInfo is a listing entry for a groupable/filterable field.
type DimensionInfo struct {
	Name        string   `json:"name"` // dataset.field
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`
	IsTime      bool     `json:"isTime,omitempty"`
	Synonyms    []string `json:"synonyms,omitempty"`
}

// ModelInfo describes one published model.
type ModelInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Metrics     int    `json:"metrics"`
	Datasets    int    `json:"datasets"`
	// Namespace and Resource identify the SemanticModel that published this
	// model, so colliding entries in a listing are attributable.
	Namespace string `json:"namespace,omitempty"`
	Resource  string `json:"resource,omitempty"`
}

// QueryResult is the adapter-facing result envelope. SQL is always included:
// provenance is part of the product.
type QueryResult struct {
	Columns      []string `json:"columns"`
	Rows         [][]any  `json:"rows"`
	RowCount     int      `json:"rowCount"`
	SQL          string   `json:"sql"`
	Model        string   `json:"model"`
	ModelVersion string   `json:"modelVersion"`
	RequestHash  string   `json:"requestHash"`
	Role         string   `json:"role,omitempty"`
	CachedPlan   bool     `json:"cachedPlan"`
	CachedResult bool     `json:"cachedResult"`
	ElapsedMs    int64    `json:"elapsedMs"`
}

// ErrUnknownModel distinguishes 404 from 400 in adapters.
type ErrUnknownModel struct{ Name string }

func (e ErrUnknownModel) Error() string {
	return fmt.Sprintf("unknown model %q (published models are listed at /v1/models)", e.Name)
}

// Resolve finds a model by name, or the single published model when name is
// empty.
func (s *Service) Resolve(name string) (*planner.CompiledModel, error) {
	if name != "" {
		if m, ok := s.Store.Get(name); ok {
			return m, nil
		}
		if dupes := s.Store.ByName(name); len(dupes) > 1 {
			var refs []string
			for _, d := range dupes {
				refs = append(refs, d.Namespace+"/"+d.Resource)
			}
			return nil, fmt.Errorf("model name %q is published by more than one resource (%s); model names must be unique across SemanticModels",
				name, strings.Join(refs, ", "))
		}
		return nil, ErrUnknownModel{name}
	}
	if m, ok := s.Store.Single(); ok {
		return m, nil
	}
	if names := s.Store.Names(); len(names) == 1 {
		name = names[0]
		if dupes := s.Store.ByName(name); len(dupes) > 1 {
			var refs []string
			for _, d := range dupes {
				refs = append(refs, d.Namespace+"/"+d.Resource)
			}
			return nil, fmt.Errorf("model name %q is published by more than one resource (%s); model names must be unique across SemanticModels",
				name, strings.Join(refs, ", "))
		}
	}
	return nil, fmt.Errorf("model name required: %d models are published (%v)", len(s.Store.Names()), s.Store.Names())
}

// Models lists the published models this identity may use, including any
// duplicated names so an operator can see the collision. A model whose
// governance has no policy for the caller's role is omitted, because every
// query against it would be refused anyway.
func (s *Service) Models(id governance.Identity) []ModelInfo {
	out := []ModelInfo{}
	for _, m := range s.Store.All() {
		if _, err := governance.Visible(m.Governance, id); err != nil {
			continue
		}
		out = append(out, ModelInfo{
			Name: m.Name, Version: m.Version, Description: m.Description,
			Metrics: len(m.MetricOrder), Datasets: len(m.DatasetOrder),
			Namespace: m.Namespace, Resource: m.Resource,
		})
	}
	return out
}

// ListMetrics returns the certified metrics this identity may query, in model
// order. A metric the role could not query is omitted rather than listed and
// refused later, so discovery leaks neither the metric's existence nor its
// definition.
func (s *Service) ListMetrics(m *planner.CompiledModel, id governance.Identity) ([]MetricInfo, error) {
	vis, err := governance.Visible(m.Governance, id)
	if err != nil {
		return nil, err
	}
	out := []MetricInfo{}
	for _, name := range m.MetricOrder {
		mt := m.Metrics[name]
		if !vis.Metric(mt.Name) {
			continue
		}
		info := MetricInfo{
			Name: mt.Name, Description: mt.Description,
			Synonyms: mt.AIContext.Synonyms,
		}
		if s.ExposeExpressions {
			info.Expression = mt.Raw
		}
		out = append(out, info)
	}
	return out, nil
}

// ListDimensions returns the dataset fields this identity may read, in model
// order. Fields denied to the role are omitted, so a column name a role may
// never see is never disclosed by listing it.
func (s *Service) ListDimensions(m *planner.CompiledModel, id governance.Identity) ([]DimensionInfo, error) {
	vis, err := governance.Visible(m.Governance, id)
	if err != nil {
		return nil, err
	}
	out := []DimensionInfo{}
	for _, dsName := range m.DatasetOrder {
		ds := m.Datasets[dsName]
		for _, fName := range ds.FieldOrder {
			f := ds.Fields[fName]
			ref := dsName + "." + fName
			if !vis.Field(ref) {
				continue
			}
			out = append(out, DimensionInfo{
				Name: ref, Description: f.Description,
				Type: f.Type, IsTime: f.IsTime, Synonyms: f.AIContext.Synonyms,
			})
		}
	}
	return out, nil
}

// MaxRequestBytes is the largest request body an adapter should accept, for
// adapters that read from the network before the service sees the request.
func (s *Service) MaxRequestBytes() int64 {
	return int64(s.Limits.withDefaults().MaxRequestBytes)
}

// Plan compiles a request without executing it (dry run and first half of
// Query). The plan cache key includes model version and effective role.
func (s *Service) Plan(ctx context.Context, m *planner.CompiledModel, req planner.Request, id governance.Identity) (*planner.Plan, bool, error) {
	req, err := s.Limits.apply(req)
	if err != nil {
		return nil, false, err
	}
	// Keyed on the whole identity, roles and claims both. Roles alone would
	// let two tenants sharing a role collide on one compiled plan.
	key := cache.PlanKey(s.Dialect.Name(), m.Name, m.Version,
		planner.RequestHash(req, governance.IdentityKey(m.Governance, id)))
	if blob, ok := s.Cache.GetPlan(ctx, key); ok {
		var p planner.Plan
		if err := json.Unmarshal(blob, &p); err == nil {
			s.Metrics.PlanCacheHits.Inc()
			return &p, true, nil
		}
	}
	p, err := planner.Build(m, s.Dialect, req, id)
	if err != nil {
		return nil, false, err
	}
	if blob, err := json.Marshal(p); err == nil {
		s.Cache.SetPlan(ctx, key, blob)
	}
	return p, false, nil
}

// Query plans and executes. Every emitted query carries the model version
// and request hash in its SQL comment; the same fields are logged and traced.
func (s *Service) Query(ctx context.Context, adapter string, m *planner.CompiledModel, req planner.Request, id governance.Identity) (*QueryResult, error) {
	start := time.Now()
	ctx, span := s.Tracer.Start(ctx, "semantic.query", trace.WithAttributes(
		attribute.String("semantic.model", m.Name),
		attribute.String("semantic.model_version", m.Version),
		attribute.String("semantic.adapter", adapter),
	))
	defer span.End()

	plan, cachedPlan, err := s.Plan(ctx, m, req, id)
	if err != nil {
		s.Metrics.Requests.WithLabelValues(adapter, m.Name, "plan_error").Inc()
		return nil, err
	}
	span.SetAttributes(attribute.String("semantic.request_hash", plan.RequestHash))

	res := &QueryResult{
		SQL: plan.SQL, Model: plan.Model, ModelVersion: plan.ModelVersion,
		RequestHash: plan.RequestHash, Role: plan.Role, CachedPlan: cachedPlan,
	}
	rkey := cache.ResultKey(m.Name, m.Version, plan.SQL)
	if blob, ok := s.Cache.GetResult(ctx, rkey); ok {
		var cached struct {
			Columns []string `json:"columns"`
			Rows    [][]any  `json:"rows"`
		}
		if err := json.Unmarshal(blob, &cached); err == nil {
			s.Metrics.ResultCacheHits.Inc()
			res.Columns, res.Rows, res.CachedResult = cached.Columns, cached.Rows, true
			res.RowCount = len(cached.Rows)
			res.ElapsedMs = time.Since(start).Milliseconds()
			s.Metrics.Requests.WithLabelValues(adapter, m.Name, "ok").Inc()
			return res, nil
		}
	}

	qStart := time.Now()
	cols, rows, err := s.DB.Query(ctx, plan.SQL)
	s.Metrics.QueryDuration.Observe(time.Since(qStart).Seconds())
	if err != nil {
		s.Metrics.Requests.WithLabelValues(adapter, m.Name, "exec_error").Inc()
		s.Log.Error("engine execution failed", "engine", s.Dialect.Name(),
			"model", m.Name, "version", m.Version,
			"request", plan.RequestHash, "err", err)
		return nil, fmt.Errorf("executing planned query: %w", err)
	}
	if rows == nil {
		rows = [][]any{}
	}
	// The engine client already abandons an oversized result while scanning,
	// which is the only place the allocation can actually be prevented. This
	// second check is defence in depth for a client that does not enforce the
	// ceiling, and it is what decides whether the result may be cached.
	lim := s.Limits.withDefaults()
	blob, marshalErr := json.Marshal(map[string]any{"columns": cols, "rows": rows})
	if marshalErr == nil && len(blob) > lim.MaxResultBytes {
		s.Metrics.Requests.WithLabelValues(adapter, m.Name, "result_too_large").Inc()
		return nil, fmt.Errorf("%w: result is %d bytes, the maximum is %d; narrow the request or lower the limit",
			ErrRequestTooLarge, len(blob), lim.MaxResultBytes)
	}
	res.Columns, res.Rows, res.RowCount = cols, rows, len(rows)
	// Caching is an optimization, so an oversized result is served and simply
	// not cached.
	if marshalErr == nil && len(blob) <= lim.MaxCacheEntryBytes {
		s.Cache.SetResult(ctx, rkey, blob)
	}
	res.ElapsedMs = time.Since(start).Milliseconds()
	s.Metrics.Requests.WithLabelValues(adapter, m.Name, "ok").Inc()
	s.Log.Info("semantic query served", "adapter", adapter, "model", m.Name,
		"version", m.Version, "request", plan.RequestHash, "role", plan.Role,
		"rows", res.RowCount, "cached_plan", cachedPlan, "elapsed_ms", res.ElapsedMs)
	return res, nil
}

// identityKey carries the caller identity through context from HTTP
// middleware into adapters.
type identityKey struct{}

// WithIdentity stores an identity in the context.
func WithIdentity(ctx context.Context, id governance.Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom reads the identity; zero value means "use the default role".
func IdentityFrom(ctx context.Context) governance.Identity {
	if id, ok := ctx.Value(identityKey{}).(governance.Identity); ok {
		return id
	}
	return governance.Identity{}
}
