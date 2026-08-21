//go:build e2e

package authe2e

import (
	"strings"
	"testing"
)

// A jwt-mode server rejects an unverified caller with 401.
func TestJWTNoAuthorizationIs401(t *testing.T) {
	r, err := queryModel(testCtx(t), cfg.passthroughNS, cfg.model, nil, minimalQuery())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.status != 401 {
		t.Fatalf("no Authorization: want 401, got %d (%s)", r.status, r.raw)
	}
}

func TestJWTMalformedBearerIs401(t *testing.T) {
	r, err := queryModel(testCtx(t), cfg.passthroughNS, cfg.model, bearer("not-a-real-token"), minimalQuery())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.status != 401 {
		t.Fatalf("malformed bearer: want 401, got %d (%s)", r.status, r.raw)
	}
}

func TestJWTTamperedSignatureIs401(t *testing.T) {
	tok := token(t, "alice")
	tampered := tamperSignature(tok)
	if tampered == tok {
		t.Fatal("failed to tamper token")
	}
	r, err := queryModel(testCtx(t), cfg.passthroughNS, cfg.model, bearer(tampered), minimalQuery())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.status != 401 {
		t.Fatalf("tampered signature: want 401, got %d (%s)", r.status, r.raw)
	}
}

func TestJWTValidTokenIs200(t *testing.T) {
	tok := token(t, "alice")
	r, err := queryModel(testCtx(t), cfg.passthroughNS, cfg.model, bearer(tok), minimalQuery())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.status != 200 {
		t.Fatalf("valid token: want 200, got %d (%s)", r.status, r.raw)
	}
}

// A valid token wins over identity headers. alice may read the denied table, so
// a bob header must not deny the query; if the header were honored the query
// would fail as bob.
func TestJWTHeadersIgnoredWithValidToken(t *testing.T) {
	tok := token(t, "alice")
	h := bearer(tok)
	h["X-Semantic-User"] = "bob"
	h["X-Semantic-Role"] = "admin"
	r, err := queryModel(testCtx(t), cfg.passthroughNS, cfg.model, h, denyQuery())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.status != 200 || len(r.rows) == 0 {
		t.Fatalf("headers must be ignored (identity alice reads the table): got %d rows=%d (%s)", r.status, len(r.rows), r.raw)
	}
}

// tamperSignature flips the first character of a JWT signature segment. The
// first character's bits are fully significant, so the decoded signature always
// changes. The last character carries padding bits and can decode identically,
// which would leave the token valid.
func tamperSignature(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[2] == "" {
		return tok
	}
	sig := []byte(parts[2])
	if sig[0] == 'A' {
		sig[0] = 'B'
	} else {
		sig[0] = 'A'
	}
	parts[2] = string(sig)
	return strings.Join(parts, ".")
}
