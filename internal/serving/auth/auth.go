// Package auth resolves the caller identity the planner authorizes against.
//
// Two modes exist, and the difference is who is trusted.
//
// Header mode reads the role straight from the X-Semantic-Role header. It is
// the historical default and is only safe when something in front of the
// server authenticates the caller and overwrites that header, because any
// client that can reach the port may otherwise claim any role, including one
// with unrestricted access.
//
// JWT mode requires a bearer token signed by the configured issuer, validates
// it against the issuer's JWKS, and takes the role from a claim. In this mode
// any inbound X-Semantic-Role is ignored outright, so a client cannot assert
// its own identity. This is the mode to run in production.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/KubedAI/semantic-operator/internal/governance"
)

// Mode selects how identity is established.
type Mode string

const (
	// ModeHeader trusts the X-Semantic-Role header (requires a front proxy).
	ModeHeader Mode = "header"
	// ModeJWT requires a JWKS-validated bearer token.
	ModeJWT Mode = "jwt"
)

// RoleHeader is the identity header, trusted only in header mode.
const RoleHeader = "X-Semantic-Role"

// ErrUnauthenticated marks a missing or invalid token. Adapters map it to 401,
// which is distinct from governance.ErrUnauthorized (403): the caller is
// unknown rather than known and disallowed.
var ErrUnauthenticated = errors.New("unauthenticated")

// Options configures identity resolution. Everything except Mode applies to
// JWT mode only.
type Options struct {
	Mode Mode
	// JWKSURL serves the issuer's signing keys. Required in JWT mode.
	JWKSURL string
	// Issuer, when set, must equal the token's iss claim.
	Issuer string
	// Audience, when set, must appear in the token's aud claim.
	Audience string
	// RoleClaim names the claim holding the role. Dots address nested
	// objects, e.g. "resource_access.semantic.role". Default "role".
	RoleClaim string
	// ClaimsToCopy are claim names carried into Identity.Claims, so row
	// filters can reference caller attributes (tenant, region) later.
	ClaimsToCopy []string
	// RefreshInterval bounds how long a rotated signing key takes to be
	// picked up. Default 1h.
	RefreshInterval time.Duration
}

// Authenticator resolves an identity from a request.
type Authenticator struct {
	mode      Mode
	keyfunc   jwt.Keyfunc
	roleClaim []string
	copy      []string
	parser    *jwt.Parser
}

// New builds an Authenticator. In JWT mode it fetches the JWKS immediately,
// so a misconfigured issuer fails at startup rather than on first query.
func New(ctx context.Context, opts Options) (*Authenticator, error) {
	switch opts.Mode {
	case "", ModeHeader:
		return &Authenticator{mode: ModeHeader}, nil
	case ModeJWT:
	default:
		return nil, fmt.Errorf("unknown auth mode %q: use header or jwt", opts.Mode)
	}
	if opts.JWKSURL == "" {
		return nil, errors.New("auth: jwksURL is required in jwt mode")
	}
	if opts.RefreshInterval == 0 {
		opts.RefreshInterval = time.Hour
	}
	claim := opts.RoleClaim
	if claim == "" {
		claim = "role"
	}

	// keyfunc refreshes in the background and does not report an unreachable
	// endpoint, so a typo in the issuer URL would start cleanly and then
	// reject every query. Probe it here to fail at startup instead.
	if err := probeJWKS(ctx, opts.JWKSURL); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	k, err := keyfunc.NewDefaultCtx(ctx, []string{opts.JWKSURL})
	if err != nil {
		return nil, fmt.Errorf("auth: loading JWKS from %s: %w", opts.JWKSURL, err)
	}

	// Signature algorithms are restricted to asymmetric families: accepting
	// HMAC would let anyone holding the (public) JWKS mint valid tokens.
	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "PS256", "PS384", "PS512"}),
		jwt.WithExpirationRequired(),
	}
	if opts.Issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(opts.Issuer))
	}
	if opts.Audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(opts.Audience))
	}

	return &Authenticator{
		mode:      ModeJWT,
		keyfunc:   k.Keyfunc,
		roleClaim: strings.Split(claim, "."),
		copy:      opts.ClaimsToCopy,
		parser:    jwt.NewParser(parserOpts...),
	}, nil
}

// Mode reports the configured mode, for logging and readiness reporting.
func (a *Authenticator) Mode() Mode { return a.mode }

// Identity resolves the caller. In header mode it returns the header value
// verbatim. In JWT mode it validates the bearer token and reads the role
// claim, ignoring any client-supplied role header.
func (a *Authenticator) Identity(r *http.Request) (governance.Identity, error) {
	if a.mode == ModeHeader {
		return governance.Identity{Role: r.Header.Get(RoleHeader)}, nil
	}

	raw, err := bearerToken(r)
	if err != nil {
		return governance.Identity{}, err
	}
	claims := jwt.MapClaims{}
	if _, err := a.parser.ParseWithClaims(raw, claims, a.keyfunc); err != nil {
		return governance.Identity{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	role, ok := stringClaim(claims, a.roleClaim)
	if !ok || role == "" {
		return governance.Identity{}, fmt.Errorf("%w: token has no %q claim",
			ErrUnauthenticated, strings.Join(a.roleClaim, "."))
	}
	id := governance.Identity{Role: role}
	for _, name := range a.copy {
		if v, ok := stringClaim(claims, strings.Split(name, ".")); ok {
			if id.Claims == nil {
				id.Claims = map[string]string{}
			}
			id.Claims[name] = v
		}
	}
	return id, nil
}

// probeJWKS verifies the endpoint answers with a usable key set.
func probeJWKS(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("jwksURL %q: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching JWKS from %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching JWKS from %s: HTTP %d", url, resp.StatusCode)
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return fmt.Errorf("parsing JWKS from %s: %w", url, err)
	}
	if len(set.Keys) == 0 {
		return fmt.Errorf("JWKS at %s contains no keys", url)
	}
	return nil
}

func bearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", fmt.Errorf("%w: no Authorization header", ErrUnauthenticated)
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", fmt.Errorf("%w: Authorization must be a Bearer token", ErrUnauthenticated)
	}
	return strings.TrimSpace(h[len(prefix):]), nil
}

// stringClaim walks a dotted claim path and renders the leaf as a string.
func stringClaim(claims map[string]any, path []string) (string, bool) {
	var cur any = claims
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[p]
		if !ok {
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case []any:
		// A role claim is often a single-element list.
		if len(v) == 1 {
			if s, ok := v[0].(string); ok {
				return s, true
			}
		}
	}
	return "", false
}
