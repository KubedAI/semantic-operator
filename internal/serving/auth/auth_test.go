package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwksServer serves a JWKS for a generated RSA key and mints tokens with it.
type jwksServer struct {
	*httptest.Server
	key *rsa.PrivateKey
	kid string
}

func newJWKS(t *testing.T) *jwksServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &jwksServer{key: key, kid: "test-key"}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": s.kid,
			"alg": "RS256",
			"use": "sig",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	if _, exists := claims["sub"]; !exists {
		claims["sub"] = "test-user"
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.kid
	signed, err := tok.SignedString(s.key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func req(authz string, roleHeader string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/models/m/query", nil)
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	if roleHeader != "" {
		r.Header.Set(RoleHeader, roleHeader)
	}
	return r
}

func jwtAuth(t *testing.T, s *jwksServer, opts Options) *Authenticator {
	t.Helper()
	opts.Mode = ModeJWT
	opts.JWKSURL = s.URL
	a, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestHeaderModeTrustsHeader(t *testing.T) {
	a, err := New(context.Background(), Options{Mode: ModeHeader})
	if err != nil {
		t.Fatal(err)
	}
	r := req("", "analyst")
	r.Header.Set(PrincipalHeader, "alice")
	res, err := a.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	id := res.Identity
	if res.Token != "" {
		t.Errorf("header mode must not produce a token, got %q", res.Token)
	}
	if id.Principal != "alice" {
		t.Errorf("principal = %q, want alice", id.Principal)
	}
	if got := strings.Join(id.Roles, ","); got != "analyst" {
		t.Errorf("roles = %q, want analyst", got)
	}
	// The default (empty mode) must behave the same, so existing installs
	// keep working.
	def, err := New(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if def.Mode() != ModeHeader {
		t.Errorf("default mode = %q, want header", def.Mode())
	}
}

func TestHeaderModeRequiresPrincipal(t *testing.T) {
	a, err := New(context.Background(), Options{Mode: ModeHeader})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(req("", "analyst")); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTModeIgnoresRoleHeader(t *testing.T) {
	// The whole point: a client that sends its own role header must not be
	// able to escalate. The token says analyst, the header claims admin.
	s := newJWKS(t)
	a := jwtAuth(t, s, Options{})
	token := s.sign(t, jwt.MapClaims{
		"role": "analyst",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	res, err := a.Authenticate(req("Bearer "+token, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Identity
	if res.Token != token {
		t.Fatal("jwt mode must return the raw token for passthrough")
	}
	if !res.Expiry.After(time.Now()) {
		t.Fatalf("jwt mode must return a future expiry, got %v", res.Expiry)
	}
	if got := strings.Join(id.Roles, ","); got != "analyst" {
		t.Fatalf("roles = %q; the header must be ignored in jwt mode", got)
	}
}

func TestJWTModeRejectsBadTokens(t *testing.T) {
	s := newJWKS(t)
	a := jwtAuth(t, s, Options{Issuer: "https://issuer.example", Audience: "semantic"})
	base := func() jwt.MapClaims {
		return jwt.MapClaims{
			"role": "analyst",
			"iss":  "https://issuer.example",
			"aud":  "semantic",
			"exp":  time.Now().Add(time.Hour).Unix(),
		}
	}

	// A token signed by a different key must fail.
	other := newJWKS(t)
	wrongKey := other.sign(t, base())

	expired := base()
	expired["exp"] = time.Now().Add(-time.Minute).Unix()

	wrongIssuer := base()
	wrongIssuer["iss"] = "https://evil.example"

	wrongAudience := base()
	wrongAudience["aud"] = "someone-else"

	noRole := base()
	delete(noRole, "role")

	noPrincipal := base()
	noPrincipal["sub"] = ""

	noExp := jwt.MapClaims{"role": "analyst", "iss": "https://issuer.example", "aud": "semantic"}

	cases := map[string]*http.Request{
		"no header":         req("", ""),
		"not bearer":        req("Basic abc", ""),
		"garbage token":     req("Bearer not-a-jwt", ""),
		"wrong key":         req("Bearer "+wrongKey, ""),
		"expired":           req("Bearer "+s.sign(t, expired), ""),
		"wrong issuer":      req("Bearer "+s.sign(t, wrongIssuer), ""),
		"wrong audience":    req("Bearer "+s.sign(t, wrongAudience), ""),
		"missing role":      req("Bearer "+s.sign(t, noRole), ""),
		"missing principal": req("Bearer "+s.sign(t, noPrincipal), ""),
		"no expiry":         req("Bearer "+s.sign(t, noExp), ""),
	}
	for name, r := range cases {
		if _, err := a.Authenticate(r); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s: err = %v, want ErrUnauthenticated", name, err)
		}
	}
}

func TestJWTModeRejectsHMACToken(t *testing.T) {
	// The JWKS is public, so accepting HMAC would let anyone mint tokens.
	s := newJWKS(t)
	a := jwtAuth(t, s, Options{})
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"role": "admin",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	signed, err := tok.SignedString([]byte("public-jwks-is-not-a-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(req("Bearer "+signed, "")); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("HMAC token must be rejected, got %v", err)
	}
}

func TestNestedRoleClaimAndCopiedClaims(t *testing.T) {
	s := newJWKS(t)
	a := jwtAuth(t, s, Options{
		RoleClaim:    "resource_access.semantic.role",
		ClaimsToCopy: []string{"tenant", "sub"},
	})
	token := s.sign(t, jwt.MapClaims{
		"resource_access": map[string]any{
			"semantic": map[string]any{"role": []any{"tx_analyst"}},
		},
		"tenant": "acme",
		"sub":    "agent-7",
		"exp":    time.Now().Add(time.Hour).Unix(),
	})
	res, err := a.Authenticate(req("Bearer "+token, ""))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Identity
	if id.Principal != "agent-7" {
		t.Errorf("principal = %q, want agent-7", id.Principal)
	}
	if got := strings.Join(id.Roles, ","); got != "tx_analyst" {
		t.Errorf("nested single-element role claim = %q", got)
	}
	if id.Claims["tenant"] != "acme" || id.Claims["sub"] != "agent-7" {
		t.Errorf("claims = %v", id.Claims)
	}
}

func TestJWTKeepsPrincipalGroupsAndRolesSeparate(t *testing.T) {
	s := newJWKS(t)
	a := jwtAuth(t, s, Options{
		PrincipalClaim:  "preferred_username",
		RoleClaim:       "roles",
		GroupsClaim:     "groups",
		EngineUserClaim: "preferred_username",
	})
	token := s.sign(t, jwt.MapClaims{
		"preferred_username": "alice",
		"roles":              []any{"report-reader", "approver"},
		"groups":             []any{"analysts", "finance"},
		"exp":                time.Now().Add(time.Hour).Unix(),
	})
	res, err := a.Authenticate(req("Bearer "+token, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Identity
	if res.EngineUser != "alice" {
		t.Fatalf("engine user = %q, want alice", res.EngineUser)
	}
	if id.Principal != "alice" || strings.Join(id.Groups, ",") != "analysts,finance" || strings.Join(id.Roles, ",") != "report-reader,approver" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(context.Background(), Options{Mode: ModeJWT}); err == nil {
		t.Error("jwt mode without a JWKS URL must fail at startup")
	}
	if _, err := New(context.Background(), Options{Mode: Mode("wat")}); err == nil {
		t.Error("an unknown mode must fail")
	}
	// An unreachable issuer must fail fast rather than at first query.
	if _, err := New(context.Background(), Options{
		Mode:    ModeJWT,
		JWKSURL: fmt.Sprintf("http://127.0.0.1:%d/jwks", 1),
	}); err == nil {
		t.Error("an unreachable JWKS must fail at startup")
	}
}
