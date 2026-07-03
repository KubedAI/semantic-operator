// Package observability wires structured logging, Prometheus metrics, and
// optional OpenTelemetry tracing for the semantic server.
package observability

import (
	"context"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Logger returns a JSON slog logger at the level named by env (debug, info,
// warn, error).
func Logger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

// Metrics are the server's Prometheus instruments.
type Metrics struct {
	Requests        *prometheus.CounterVec
	PlanCacheHits   prometheus.Counter
	ResultCacheHits prometheus.Counter
	QueryDuration   prometheus.Histogram
}

// NewMetrics registers instruments on the default registry.
func NewMetrics() *Metrics {
	return &Metrics{
		Requests: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "semantic_requests_total",
			Help: "Semantic requests by adapter, model, and outcome.",
		}, []string{"adapter", "model", "outcome"}),
		PlanCacheHits: promauto.NewCounter(prometheus.CounterOpts{
			Name: "semantic_plan_cache_hits_total",
			Help: "Compiled-plan cache hits.",
		}),
		ResultCacheHits: promauto.NewCounter(prometheus.CounterOpts{
			Name: "semantic_result_cache_hits_total",
			Help: "Result cache hits.",
		}),
		QueryDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "semantic_starrocks_query_duration_seconds",
			Help:    "StarRocks execution latency for planned queries.",
			Buckets: prometheus.DefBuckets,
		}),
	}
}

// Tracer configures OTLP/HTTP tracing when endpoint is non-empty (standard
// OTEL_EXPORTER_OTLP_ENDPOINT form) and returns the tracer plus shutdown.
// With no endpoint it returns a no-op tracer.
func Tracer(ctx context.Context, serviceName, endpoint string) (trace.Tracer, func(context.Context) error, error) {
	if endpoint == "" {
		return otel.Tracer(serviceName), func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
	if err != nil {
		return nil, nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return tp.Tracer(serviceName), tp.Shutdown, nil
}
