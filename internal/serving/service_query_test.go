package serving

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/dbclient"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/starrocks"
	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/observability"
	"github.com/KubedAI/semantic-operator/internal/planner"
)

// fakeDB returns a fixed number of rows regardless of the SQL. The refusal
// under test is decided by the row count the service reads back, so the SQL
// itself does not matter here.
type fakeDB struct{ rows int }

func (f fakeDB) Query(context.Context, dbclient.EngineCredential, string) ([]string, [][]any, error) {
	rows := make([][]any, f.rows)
	for i := range rows {
		rows[i] = []any{int64(i)}
	}
	return []string{"revenue"}, rows, nil
}

// testMetrics builds the instruments Query touches on fresh collectors, so a
// test never registers on the default registry and never collides with another
// test that also builds metrics.
func testMetrics() *observability.Metrics {
	return &observability.Metrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_requests_total", Help: "t"},
			[]string{"adapter", "model", "outcome"}),
		PlanCacheHits:   prometheus.NewCounter(prometheus.CounterOpts{Name: "test_plan_cache_hits_total", Help: "t"}),
		ResultCacheHits: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_result_cache_hits_total", Help: "t"}),
		QueryDuration:   prometheus.NewHistogram(prometheus.HistogramOpts{Name: "test_query_duration_seconds", Help: "t"}),
		StoreSynced:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_store_synced", Help: "t"}),
		LoadedModels:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_loaded_models", Help: "t"}),
	}
}

// rowLimitModel is the smallest model Query can plan and execute: one dataset,
// one metric, one role that may read it.
func rowLimitModel(t *testing.T) *planner.CompiledModel {
	t.Helper()
	f := func(name string) v1alpha1.Field {
		return v1alpha1.Field{Name: name, Expression: v1alpha1.Expression{
			Dialects: []v1alpha1.DialectExpression{{Dialect: "ANSI_SQL", Expression: name}}}}
	}
	spec := &v1alpha1.SemanticModelSpec{
		Connection: v1alpha1.ConnectionSpec{Catalog: "iceberg", Database: "demo"},
		Ossie: v1alpha1.OssieModel{
			Name: "retail",
			Datasets: []v1alpha1.Dataset{
				{Name: "store", Source: "store", PrimaryKey: []string{"s_store_sk"},
					Fields: []v1alpha1.Field{f("s_store_sk"), f("s_state")}},
			},
			Metrics: []v1alpha1.Metric{
				{Name: "revenue", Expression: v1alpha1.Expression{
					Dialects: []v1alpha1.DialectExpression{{Dialect: "ANSI_SQL", Expression: "SUM(store.s_store_sk)"}}}},
			},
		},
		Governance: &v1alpha1.GovernanceSpec{
			DefaultRole: "analyst",
			Roles:       []v1alpha1.RolePolicy{{Name: "analyst", AllowMetrics: []string{"*"}}},
		},
	}
	cm, err := planner.Compile(spec, "ns", "retail")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return cm
}

func rowLimitService(t *testing.T, rows int) *Service {
	t.Helper()
	d, err := emitter.Get("starrocks")
	if err != nil {
		t.Fatalf("dialect: %v", err)
	}
	return &Service{
		Store:   NewStore(),
		Dialect: d,
		DB:      fakeDB{rows: rows},
		Metrics: testMetrics(),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tracer:  otel.Tracer("test"),
	}
}

// A request that names no limit and whose result is larger than the default is
// refused, not truncated. This is the behavior the whole probe exists for: a
// caller cannot tell a silent prefix from a complete answer.
func TestQueryRefusesWhenDefaultLimitIsExceeded(t *testing.T) {
	def := DefaultLimits().DefaultRowLimit
	svc := rowLimitService(t, def+1)
	m := rowLimitModel(t)

	_, err := svc.Query(context.Background(), "test", m, planner.Request{Metrics: []string{"revenue"}},
		governance.Single("analyst"), dbclient.EngineCredential{})
	if !errors.Is(err, ErrResultIncomplete) {
		t.Fatalf("want ErrResultIncomplete, got %v", err)
	}
}

// A defaulted request whose result fits inside the default is returned in full.
// The probe row is only a detector, so a complete result at exactly the default
// is not refused.
func TestQueryReturnsCompleteResultAtTheDefault(t *testing.T) {
	def := DefaultLimits().DefaultRowLimit
	svc := rowLimitService(t, def)
	m := rowLimitModel(t)

	res, err := svc.Query(context.Background(), "test", m, planner.Request{Metrics: []string{"revenue"}},
		governance.Single("analyst"), dbclient.EngineCredential{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if res.RowCount != def {
		t.Fatalf("rowCount = %d, want the full %d", res.RowCount, def)
	}
}

// An explicit limit is the caller's stated intent, so a result that fills it is
// returned without a refusal even though it may be truncated. Only a defaulted
// request is refused.
func TestQueryHonorsExplicitLimitWithoutRefusing(t *testing.T) {
	def := DefaultLimits().DefaultRowLimit
	svc := rowLimitService(t, def+1)
	m := rowLimitModel(t)

	res, err := svc.Query(context.Background(), "test", m,
		planner.Request{Metrics: []string{"revenue"}, Limit: def + 1},
		governance.Single("analyst"), dbclient.EngineCredential{})
	if err != nil {
		t.Fatalf("query with explicit limit: %v", err)
	}
	if res.RowCount != def+1 {
		t.Fatalf("rowCount = %d, want %d", res.RowCount, def+1)
	}
}
