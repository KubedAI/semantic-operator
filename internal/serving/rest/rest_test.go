package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KubedAI/semantic-operator/internal/planner"
	"github.com/KubedAI/semantic-operator/internal/serving"
	"github.com/KubedAI/semantic-operator/internal/serving/authorization"
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
	r.Header.Set(PrincipalHeader, "test-user")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMissingPrincipalIsRejected(t *testing.T) {
	h := Handler(svcWithModel(t, serving.Limits{}), nil, serving.StaticResolver())
	r := httptest.NewRequest(http.MethodPost, "/v1/models/retail/query", strings.NewReader(`{"metrics":["revenue"]}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401. body: %s", w.Code, w.Body.String())
	}
}

// A misspelled field used to be dropped silently, so the server ran a
// different query than the caller wrote and returned a confidently wrong
// answer. It must be an explicit error.
func TestUnknownFieldIsRejected(t *testing.T) {
	h := Handler(svcWithModel(t, serving.Limits{}), nil, serving.StaticResolver())
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
	h := Handler(svcWithModel(t, serving.Limits{}), nil, serving.StaticResolver())
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
	h := Handler(svcWithModel(t, serving.Limits{MaxRequestBytes: 64}), nil, serving.StaticResolver())
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
	body := `{"metrics":["revenue"],"dimensions":["store.s_state"],"filters":[{"field":"store.s_state","op":"=","value":"NY"}],"timeGrain":"month","orderBy":[{"field":"revenue","direction":"desc"},{"field":"store.s_state","direction":"asc"}],"limit":10}`
	r := httptest.NewRequest(http.MethodPost, "/v1/models/retail/query", strings.NewReader(body))
	var req planner.Request
	if err := decodeJSON(httptest.NewRecorder(), r, 1<<20, &req); err != nil {
		t.Fatalf("valid body was rejected: %v", err)
	}
	if len(req.Metrics) != 1 || req.Limit != 10 || len(req.Filters) != 1 || len(req.OrderBy) != 2 {
		t.Fatalf("body decoded incorrectly: %+v", req)
	}
	if req.OrderBy[0].Field != "revenue" || req.OrderBy[0].Direction != "desc" {
		t.Fatalf("orderBy decoded incorrectly: %+v", req.OrderBy)
	}
}

func TestUnknownOrderByPropertyIsRejected(t *testing.T) {
	body := `{"metrics":["revenue"],"orderBy":[{"field":"revenue","direction":"desc","expression":"revenue DESC"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/models/retail/query", strings.NewReader(body))
	var req planner.Request
	err := decodeJSON(httptest.NewRecorder(), r, 1<<20, &req)
	if err == nil || !strings.Contains(err.Error(), "expression") {
		t.Fatalf("expected unknown nested property error, got %v", err)
	}
}

func TestExternalAuthorizationUnavailableIsServiceUnavailable(t *testing.T) {
	w := httptest.NewRecorder()
	writeErr(w, authorization.ErrUnavailable)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", w.Code, w.Body.String())
	}
}
