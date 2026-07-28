package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KubedAI/semantic-operator/internal/planner"
	"github.com/KubedAI/semantic-operator/internal/serving"
)

// svcWithModel publishes one model so a request reaches body decoding.
// Authentication and model resolution both run first by design, so an empty
// store would answer 404 before the decoder ever sees the body.
func svcWithModel(t *testing.T, limits serving.Limits) *serving.Service {
	t.Helper()
	blob, err := json.Marshal(planner.CompiledModel{Name: "retail", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	store := serving.NewStore()
	if err := store.Put("retail-compiled", blob); err != nil {
		t.Fatal(err)
	}
	return &serving.Service{Store: store, Limits: limits}
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/models/retail/query", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A misspelled field used to be dropped silently, so the server ran a
// different query than the caller wrote and returned a confidently wrong
// answer. It must be an explicit error.
func TestUnknownFieldIsRejected(t *testing.T) {
	h := Handler(svcWithModel(t, serving.Limits{}), nil)
	w := post(t, h, `{"metrics":["revenue"],"dimension":["store.s_state"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "dimension") {
		t.Fatalf("error should name the offending field, got %s", w.Body.String())
	}
}

// Everything after the first JSON document used to be ignored, which hides a
// malformed or smuggled second request.
func TestTrailingJSONDocumentIsRejected(t *testing.T) {
	h := Handler(svcWithModel(t, serving.Limits{}), nil)
	w := post(t, h, `{"metrics":["revenue"]}{"metrics":["payroll_cost"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "exactly one JSON object") {
		t.Fatalf("unexpected error: %s", w.Body.String())
	}
}

// The body is bounded so an unbounded upload cannot be buffered into a pod
// with a fixed memory limit.
func TestOversizedBodyIsRejected(t *testing.T) {
	h := Handler(svcWithModel(t, serving.Limits{MaxRequestBytes: 64}), nil)
	w := post(t, h, `{"metrics":["`+strings.Repeat("x", 500)+`"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "maximum") {
		t.Fatalf("error should mention the ceiling, got %s", w.Body.String())
	}
}

// A well-formed body must still get past the decoder. Exercised directly
// rather than through the handler, because driving the full query path would
// require a wired cache, tracer, and engine to prove a point about decoding.
func TestWellFormedBodyPassesDecoding(t *testing.T) {
	body := `{"metrics":["revenue"],"dimensions":["store.s_state"],"filters":[{"field":"store.s_state","op":"=","value":"NY"}],"timeGrain":"month","limit":10}`
	r := httptest.NewRequest(http.MethodPost, "/v1/models/retail/query", strings.NewReader(body))
	var req planner.Request
	if err := decodeJSON(httptest.NewRecorder(), r, 1<<20, &req); err != nil {
		t.Fatalf("valid body was rejected: %v", err)
	}
	if len(req.Metrics) != 1 || req.Limit != 10 || len(req.Filters) != 1 {
		t.Fatalf("body decoded incorrectly: %+v", req)
	}
}
