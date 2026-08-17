package ranger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func rangerResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestClientCallsTypedEndpointsWithAuthentication(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		mu.Lock()
		paths = append(paths, request.URL.Path)
		bodies = append(bodies, string(body))
		mu.Unlock()
		if request.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", request.Method)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer jwt-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("X-Remote-User"); got != "semantic-server" {
			t.Errorf("X-Remote-User = %q", got)
		}

		switch request.URL.Path {
		case "/authz/v1/authorize":
			return rangerResponse(http.StatusOK, `{"requestId":"req-1","decision":"ALLOW"}`), nil
		case "/authz/v1/authorizeMulti":
			return rangerResponse(http.StatusOK, `{"requestId":"req-2","decision":"PARTIAL","accesses":[{"decision":"ALLOW"},{"decision":"DENY"}]}`), nil
		case "/authz/v1/permissions":
			return rangerResponse(http.StatusOK, `{"resource":{"name":"semantic-model:retail"},"users":{}}`), nil
		default:
			return rangerResponse(http.StatusNotFound, `{}`), nil
		}
	})
	headers := map[string]string{"x-remote-user": "semantic-server"}
	client, err := New(Options{
		URL: "https://ranger-pdp:6500/authz/v1", BearerToken: "jwt-token", Headers: headers,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	headers["x-remote-user"] = "mutated"

	result, err := client.Authorize(context.Background(), AuthorizationRequest{
		RequestID: "req-1", User: &UserInfo{Name: "alice"},
		Access:  &AccessInfo{Resource: &ResourceInfo{Name: "semantic-model:retail"}, Permissions: []string{"select"}},
		Context: &AccessContext{ServiceName: "semantic"},
	})
	if err != nil || result.Decision != DecisionAllow {
		t.Fatalf("authorize result=%+v err=%v", result, err)
	}
	multi, err := client.AuthorizeMulti(context.Background(), MultiAuthorizationRequest{
		RequestID: "req-2", User: &UserInfo{Name: "alice"},
		Accesses: []*AccessInfo{
			{Resource: &ResourceInfo{Name: "metric:revenue"}, Permissions: []string{"select"}},
			{Resource: &ResourceInfo{Name: "dimension:region"}, Permissions: []string{"select"}},
		},
		Context: &AccessContext{ServiceName: "semantic"},
	})
	if err != nil || multi.Decision != DecisionPartial || len(multi.Accesses) != 2 {
		t.Fatalf("authorizeMulti result=%+v err=%v", multi, err)
	}
	permissions, err := client.Permissions(context.Background(), ResourcePermissionsRequest{
		RequestID: "req-3", Resource: &ResourceInfo{Name: "semantic-model:retail"},
		Context: &AccessContext{ServiceName: "semantic"},
	})
	if err != nil || permissions.Resource == nil || permissions.Resource.Name != "semantic-model:retail" {
		t.Fatalf("permissions result=%+v err=%v", permissions, err)
	}

	wantPaths := []string{"/authz/v1/authorize", "/authz/v1/authorizeMulti", "/authz/v1/permissions"}
	if fmt.Sprint(paths) != fmt.Sprint(wantPaths) {
		t.Fatalf("paths = %v, want %v", paths, wantPaths)
	}
	for i, body := range bodies {
		if !strings.Contains(body, fmt.Sprintf(`"requestId":"req-%d"`, i+1)) {
			t.Errorf("request %d body = %s", i+1, body)
		}
	}
}

func TestNewNormalizesAndValidatesURLs(t *testing.T) {
	for _, tc := range []struct {
		name          string
		baseURL       string
		wantAuthorize string
	}{
		{name: "root API base", baseURL: "https://ranger:6500", wantAuthorize: "https://ranger:6500/authorize"},
		{name: "root API base slash", baseURL: "https://ranger:6500/", wantAuthorize: "https://ranger:6500/authorize"},
		{name: "standard Ranger API base", baseURL: "https://ranger:6500/authz/v1", wantAuthorize: "https://ranger:6500/authz/v1/authorize"},
		{name: "standard Ranger API base slash", baseURL: "https://ranger:6500/authz/v1/", wantAuthorize: "https://ranger:6500/authz/v1/authorize"},
		{name: "gateway API base", baseURL: "https://gateway.example/ranger/pdp/v2", wantAuthorize: "https://gateway.example/ranger/pdp/v2/authorize"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New(Options{URL: tc.baseURL})
			if err != nil {
				t.Fatal(err)
			}
			if client.authorizeURL != tc.wantAuthorize {
				t.Fatalf("authorize URL = %q, want %q", client.authorizeURL, tc.wantAuthorize)
			}
		})
	}

	for _, rawURL := range []string{
		"", "ranger:6500", "ftp://ranger:6500", "https://", "https://user:pass@ranger:6500",
		"https://ranger:6500?x=1", "https://ranger:6500#fragment", "https://ranger:6500/%zz",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if _, err := New(Options{URL: rawURL}); err == nil {
				t.Fatalf("invalid URL %q was accepted", rawURL)
			}
		})
	}
	if _, err := New(Options{URL: "http://ranger:6500", BearerToken: "token"}); err == nil {
		t.Fatal("bearer token over HTTP was accepted")
	}
	if _, err := New(Options{URL: "http://ranger:6500", Headers: map[string]string{"X-Remote-User": "server"}}); err == nil {
		t.Fatal("trusted header over HTTP was accepted")
	}
	if _, err := New(Options{URL: "http://ranger:6500"}); err != nil {
		t.Fatalf("credential-free HTTP URL rejected: %v", err)
	}
}

func TestClientRejectsRedirectsWithoutForwardingCredentials(t *testing.T) {
	calls := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			t.Fatalf("redirect followed to %s with Authorization=%q X-Remote-User=%q", request.URL, request.Header.Get("Authorization"), request.Header.Get("X-Remote-User"))
		}
		response := rangerResponse(http.StatusFound, ``)
		response.Header.Set("Location", "http://attacker.example/authz/v1/authorize")
		return response, nil
	})
	client, err := New(Options{
		URL: "https://ranger:6500", BearerToken: "secret",
		Headers: map[string]string{"X-Remote-User": "semantic-server"},
		HTTPClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Authorize(context.Background(), AuthorizationRequest{})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusFound {
		t.Fatalf("error = %v, want typed HTTP 302", err)
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want one", calls)
	}
}

func TestNewRejectsUnsafeOrManagedHeaders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers map[string]string
		token   string
	}{
		{name: "invalid name", headers: map[string]string{"Bad Header": "x"}},
		{name: "newline value", headers: map[string]string{"X-Test": "x\ny"}},
		{name: "authorization", headers: map[string]string{"Authorization": "Basic x"}},
		{name: "content type", headers: map[string]string{"content-type": "text/plain"}},
		{name: "accept", headers: map[string]string{"Accept": "text/plain"}},
		{name: "host", headers: map[string]string{"Host": "other"}},
		{name: "duplicate canonical name", headers: map[string]string{"X-Test": "one", "x-test": "two"}},
		{name: "newline token", token: "x\ny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(Options{URL: "https://ranger:6500", Headers: tc.headers, BearerToken: tc.token}); err == nil {
				t.Fatal("unsafe header configuration was accepted")
			}
		})
	}
}

func TestClientClosesResponseBody(t *testing.T) {
	body := &trackedBody{Reader: strings.NewReader(`{"decision":"ALLOW"}`)}
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})
	client, err := New(Options{URL: "https://ranger:6500", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Authorize(context.Background(), AuthorizationRequest{}); err != nil {
		t.Fatal(err)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}

func TestHTTPErrorIsTypedAndBounded(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return rangerResponse(http.StatusForbidden, `{"code":"FORBIDDEN","message":"caller is not authorized"}`), nil
	})
	client, err := New(Options{URL: "https://ranger:6500", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Authorize(context.Background(), AuthorizationRequest{})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusForbidden || httpErr.Code != "FORBIDDEN" || httpErr.Message != "caller is not authorized" {
		t.Fatalf("HTTPError = %+v", httpErr)
	}
	if got := err.Error(); strings.Contains(got, "{") || !strings.Contains(got, "FORBIDDEN") {
		t.Fatalf("unsafe or incomplete error = %q", got)
	}
}

func TestStrictResponseDecoding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "unknown field", body: `{"decision":"ALLOW","extra":true}`},
		{name: "unknown decision", body: `{"decision":"FUTURE"}`},
		{name: "trailing value", body: `{"decision":"ALLOW"} {}`},
		{name: "null", body: `null`},
		{name: "empty", body: ``},
		{name: "wrong shape", body: `[]`},
		{name: "non-200 success", status: http.StatusCreated, body: `{"decision":"ALLOW"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				status := tc.status
				if status == 0 {
					status = http.StatusOK
				}
				return rangerResponse(status, tc.body), nil
			})
			client, err := New(Options{URL: "https://ranger:6500", HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Authorize(context.Background(), AuthorizationRequest{}); err == nil {
				t.Fatal("invalid response was accepted")
			}
		})
	}
}

func TestResponseSizeAndTimeoutBounds(t *testing.T) {
	t.Run("response size", func(t *testing.T) {
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return rangerResponse(http.StatusOK, `{"decision":"ALLOW","padding":"`+strings.Repeat("x", 100)+`"}`), nil
		})
		client, err := New(Options{
			URL: "https://ranger:6500", MaxResponseBytes: 32,
			HTTPClient: &http.Client{Transport: transport},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Authorize(context.Background(), AuthorizationRequest{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("error = %v, want response-size error", err)
		}
	})

	t.Run("maximum configured bound", func(t *testing.T) {
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return rangerResponse(http.StatusOK, `{"decision":"ALLOW"}`), nil
		})
		client, err := New(Options{
			URL: "https://ranger:6500", MaxResponseBytes: math.MaxInt64,
			HTTPClient: &http.Client{Transport: transport},
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Authorize(context.Background(), AuthorizationRequest{})
		if err != nil || result.Decision != DecisionAllow {
			t.Fatalf("result=%+v error=%v", result, err)
		}
	})

	t.Run("timeout and no retry", func(t *testing.T) {
		calls := 0
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			<-request.Context().Done()
			return nil, request.Context().Err()
		})
		client, err := New(Options{
			URL: "https://ranger:6500", Timeout: 5 * time.Millisecond,
			HTTPClient: &http.Client{Transport: transport},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Authorize(context.Background(), AuthorizationRequest{})
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
		if calls != 1 {
			t.Fatalf("transport calls = %d, want one", calls)
		}
	})
}

func TestRequestEncodingAndTransportErrors(t *testing.T) {
	client, err := New(Options{URL: "https://ranger:6500"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Authorize(context.Background(), AuthorizationRequest{
		User: &UserInfo{Attributes: map[string]any{"invalid": make(chan struct{})}},
	})
	if err == nil || !strings.Contains(err.Error(), "encoding") {
		t.Fatalf("error = %v, want encoding error", err)
	}

	transportErr := errors.New("network unavailable")
	calls := 0
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, transportErr
	})
	client, err = New(Options{URL: "https://ranger:6500", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Authorize(context.Background(), AuthorizationRequest{})
	if !errors.Is(err, transportErr) || calls != 1 {
		t.Fatalf("error = %v calls=%d", err, calls)
	}
}
