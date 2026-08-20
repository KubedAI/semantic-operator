package exchange

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// rtFunc adapts a function to http.RoundTripper so tests need no listener.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resp(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{contentType}},
	}
}

func newExchanger(t *testing.T, transport http.RoundTripper, logger *slog.Logger) *Exchanger {
	t.Helper()
	ex, err := New(Options{
		TokenURL:   "https://idp.test/token",
		ClientID:   "semantic-server",
		HTTPClient: &http.Client{Transport: transport},
		Logger:     logger,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ex
}

// The cache is keyed by the caller principal, not the subject token. Two calls
// for the same principal hit the token endpoint once; a different principal
// triggers a second exchange.
func TestExchangeCachesByPrincipalNotToken(t *testing.T) {
	var calls int32
	tr := rtFunc(func(*http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return resp(200, "application/json",
			`{"access_token":"engine-token","token_type":"Bearer","expires_in":300}`), nil
	})
	ex := newExchanger(t, tr, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Same principal, two different subject tokens: still one exchange, because
	// the key is the principal.
	if _, _, err := ex.Exchange(context.Background(), "alice", "subject-token-A"); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	if _, _, err := ex.Exchange(context.Background(), "alice", "subject-token-B"); err != nil {
		t.Fatalf("second exchange: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("same principal made %d token calls, want 1 (cache miss)", got)
	}

	// A different principal is a distinct key, so it exchanges again.
	if _, _, err := ex.Exchange(context.Background(), "bob", "subject-token-A"); err != nil {
		t.Fatalf("bob exchange: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("distinct principal made %d total calls, want 2", got)
	}
}

// An empty cache key disables caching, so every call exchanges.
func TestExchangeEmptyKeyBypassesCache(t *testing.T) {
	var calls int32
	tr := rtFunc(func(*http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return resp(200, "application/json",
			`{"access_token":"engine-token","token_type":"Bearer","expires_in":300}`), nil
	})
	ex := newExchanger(t, tr, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i := 0; i < 2; i++ {
		if _, _, err := ex.Exchange(context.Background(), "", "subject-token"); err != nil {
			t.Fatalf("exchange %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("empty key made %d calls, want 2 (no caching)", got)
	}
}

// A failed exchange must not log the response body, which can reflect the
// subject token. Only the status and RFC error code may be logged.
func TestExchangeFailureDoesNotLogBodyOrToken(t *testing.T) {
	const subjectToken = "eyHEADER.PAYLOAD.SUPERSECRETSIG"
	// A non-RFC error body (no "error" field, text/plain) is exactly the case
	// where oauth2.RetrieveError.Error() dumps the raw body. Have it reflect
	// the subject token, as a misbehaving endpoint might.
	tr := rtFunc(func(*http.Request) (*http.Response, error) {
		return resp(400, "text/plain", "upstream rejected subject_token="+subjectToken), nil
	})
	var buf bytes.Buffer
	ex := newExchanger(t, tr, slog.New(slog.NewTextHandler(&buf, nil)))

	_, _, err := ex.Exchange(context.Background(), "alice", subjectToken)
	if err != ErrExchangeFailed {
		t.Fatalf("err = %v, want ErrExchangeFailed", err)
	}
	logged := buf.String()
	if strings.Contains(logged, subjectToken) {
		t.Fatalf("log leaked the subject token: %q", logged)
	}
	if strings.Contains(logged, "subject_token=") || strings.Contains(logged, "upstream rejected") {
		t.Fatalf("log leaked the response body: %q", logged)
	}
	if !strings.Contains(logged, "status=400") {
		t.Fatalf("log should record the status, got: %q", logged)
	}
}
