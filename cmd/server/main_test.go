package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KubedAI/semantic-operator/internal/serving"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestReadyzRequiresStoreSync(t *testing.T) {
	store := serving.NewStore()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	readyzHandler(store, fakePinger{}, "trino").ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
	if got := rr.Body.String(); got != "compiled-model store not synced\n" {
		t.Fatalf("body = %q, want compiled-model sync error", got)
	}
}

func TestReadyzRequiresQueryEngine(t *testing.T) {
	store := serving.NewStore()
	store.MarkSynced()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	readyzHandler(store, fakePinger{err: errors.New("no route")}, "trino").ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
	if got := rr.Body.String(); got != "query engine (trino) unreachable: no route\n" {
		t.Fatalf("body = %q, want engine readiness error naming the active engine", got)
	}
}

func TestReadyzHealthy(t *testing.T) {
	store := serving.NewStore()
	store.MarkSynced()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	readyzHandler(store, fakePinger{}, "trino").ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}
