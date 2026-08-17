package opa

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/planner"
	"github.com/KubedAI/semantic-operator/internal/serving/authorization"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestDecidePostsOPAInputAndAcceptsRevision(t *testing.T) {
	var got authorization.Input
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/data/semantic/query/allow" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer secret" {
			t.Errorf("Authorization = %q", auth)
		}
		var body struct {
			Input authorization.Input `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		got = body.Input
		return response(http.StatusOK, `{"decision_id":"per-request","result":{"allow":true,"revision":"bundle-42"}}`), nil
	})

	client, err := New(Options{
		URL: "https://opa:8181", DecisionPath: "semantic/query/allow",
		BearerToken: "secret", HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := authorization.Input{
		APIVersion: authorization.InputAPIVersion, Action: authorization.ActionQuery,
		Identity: governance.Identity{
			Principal: "alice", Groups: []string{"analysts"}, Roles: []string{"report-reader"},
			Claims: map[string]string{"tenant": "acme"},
		},
		Model:       authorization.Model{Name: "retail", Version: "v1"},
		Request:     planner.Request{Metrics: []string{"revenue"}},
		Environment: authorization.Environment{AccessTimeUnixMilli: 1234, Adapter: "rest"},
	}
	decision, err := client.Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allow || decision.Revision != "bundle-42" {
		t.Fatalf("decision = %+v", decision)
	}
	if got.APIVersion != authorization.InputAPIVersion || got.Model.Name != "retail" || len(got.Request.Metrics) != 1 ||
		got.Identity.Principal != "alice" || !reflect.DeepEqual(got.Identity.Groups, []string{"analysts"}) ||
		!reflect.DeepEqual(got.Identity.Roles, []string{"report-reader"}) || got.Identity.Claims["tenant"] != "acme" ||
		got.Environment.Adapter != "rest" || got.Environment.AccessTimeUnixMilli != 1234 {
		t.Fatalf("OPA input lost semantic context: %+v", got)
	}
}

func TestClientRejectsRedirectsWithoutForwardingCredentials(t *testing.T) {
	calls := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			t.Fatalf("redirect followed to %s with Authorization=%q", request.URL, request.Header.Get("Authorization"))
		}
		resp := response(http.StatusFound, `{}`)
		resp.Header.Set("Location", "http://attacker.example/v1/data/allow")
		return resp, nil
	})
	client, err := New(Options{
		URL: "https://opa:8181", DecisionPath: "semantic/query/allow", BearerToken: "secret",
		HTTPClient: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Decide(context.Background(), authorization.Input{}); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("error = %v, want HTTP 302", err)
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want one", calls)
	}
}

func TestDecodeDecisionShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    authorization.Decision
		wantErr bool
	}{
		{name: "boolean allow", body: `{"result":true}`, want: authorization.Decision{Allow: true}},
		{name: "boolean deny", body: `{"result":false}`, want: authorization.Decision{Allow: false}},
		{name: "undefined denies", body: `{}`, want: authorization.Decision{Allow: false}},
		{name: "null denies", body: `{"result":null}`, want: authorization.Decision{Allow: false}},
		{name: "object", body: `{"result":{"allow":true,"revision":"v9"}}`, want: authorization.Decision{Allow: true, Revision: "v9"}},
		{name: "missing allow", body: `{"result":{"revision":"v9"}}`, wantErr: true},
		{name: "unknown obligation", body: `{"result":{"allow":true,"rowFilters":[]}}`, wantErr: true},
		{name: "unknown envelope field", body: `{"result":true,"obligations":[]}`, wantErr: true},
		{name: "null envelope", body: `null`, wantErr: true},
		{name: "trailing value", body: `{"result":true} {}`, wantErr: true},
		{name: "wrong type", body: `{"result":"allow"}`, wantErr: true},
		{name: "control revision", body: `{"result":{"allow":true,"revision":"bad\nrevision"}}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeDecision([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v err=%v, want %+v", got, err, tc.want)
			}
		})
	}
}

func TestDecideFailsOnHTTPErrorOversizeAndTimeout(t *testing.T) {
	t.Run("http status", func(t *testing.T) {
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusInternalServerError, "internal details"), nil
		})
		client, _ := New(Options{URL: "http://opa:8181", DecisionPath: "p", HTTPClient: &http.Client{Transport: transport}})
		if _, err := client.Decide(context.Background(), authorization.Input{}); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("oversize", func(t *testing.T) {
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, `{"result":true,"padding":"`+strings.Repeat("x", 100)+`"}`), nil
		})
		client, _ := New(Options{
			URL: "http://opa:8181", DecisionPath: "p", MaxResponseBytes: 32,
			HTTPClient: &http.Client{Transport: transport},
		})
		if _, err := client.Decide(context.Background(), authorization.Input{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		client, _ := New(Options{
			URL: "http://opa:8181", DecisionPath: "p", Timeout: 5 * time.Millisecond,
			HTTPClient: &http.Client{Transport: transport},
		})
		_, err := client.Decide(context.Background(), authorization.Input{})
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
	})
}

func TestNewAndDecisionPathValidation(t *testing.T) {
	for _, rawURL := range []string{"", "ftp://opa:8181", "http://user:pass@opa:8181", "http://opa:8181?x=1"} {
		if _, err := New(Options{URL: rawURL, DecisionPath: "p"}); err == nil {
			t.Fatalf("invalid URL accepted: %q", rawURL)
		}
	}
	if _, err := New(Options{URL: "http://opa:8181", DecisionPath: "p", BearerToken: "secret"}); err == nil {
		t.Fatal("bearer token over plaintext HTTP was accepted")
	}
	if _, err := New(Options{URL: "http://opa:8181", DecisionPath: "p"}); err != nil {
		t.Fatalf("credential-free HTTP URL rejected: %v", err)
	}
	for _, decisionPath := range []string{"", "/allow", "a//b", "../secret", "a?b"} {
		if _, err := New(Options{URL: "http://opa:8181", DecisionPath: decisionPath}); err == nil {
			t.Fatalf("invalid decisionPath accepted: %q", decisionPath)
		}
	}
}
