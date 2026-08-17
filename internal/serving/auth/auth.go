// Package auth resolves the caller identity the planner authorizes against.
//
// Two modes exist, and the difference is who is trusted.
//
// Header mode reads the principal and roles from X-Semantic-User and
// X-Semantic-Role. It is the historical default and is only safe when
// something in front of the server authenticates the caller, strips both
// inbound headers, and sets both itself. Any client that can reach the port
// could otherwise claim an arbitrary identity.
//
// JWT mode requires a bearer token signed by the configured issuer, validates
// it against the issuer's JWKS, and resolves principal, groups, and roles from
// configured claims. In this mode inbound identity headers are ignored
// outright. This is the mode to run in production.
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

// RoleHeader carries application roles, trusted only in header mode.
const RoleHeader = "X-Semantic-Role"

// PrincipalHeader identifies the authenticated caller, trusted only in header
// mode. A front proxy must strip and replace both identity headers.
const PrincipalHeader = "X-Semantic-User"

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
	// PrincipalClaim identifies the authenticated principal presented to policy
	// providers. Dots address nested objects. Default "sub".
	PrincipalClaim string
	// RoleClaim names a claim holding one or more application roles. Dots
	// address nested objects, e.g. "resource_access.semantic.roles". Default
	// "role".
	RoleClaim string
	// GroupsClaim names a claim holding directory-group membership. Groups are
	// kept separate from roles for external providers; built-in governance uses
	// their union as policy labels.
	GroupsClaim string
	// ClaimsToCopy are claim names carried into Identity.Claims, so row
	// filters can reference caller attributes (tenant, region).
	ClaimsToCopy []string
	// RefreshInterval bounds how long a rotated signing key takes to be
	// picked up. Default 1h.
	RefreshInterval time.Duration
}

// Authenticator resolves an identity from a request.
type Authenticator struct {
	mode           Mode
	keyfunc        jwt.Keyfunc
	principalClaim []string
	roleClaim      []string
	groupsClaim    []string
	copy           []string
	parser         *jwt.Parser
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
	principalClaim := opts.PrincipalClaim
	if principalClaim == "" {
		principalClaim = "sub"
	}
	roleClaim := opts.RoleClaim
	if roleClaim == "" {
		roleClaim = "role"
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

	a := &Authenticator{
		mode:           ModeJWT,
		keyfunc:        k.Keyfunc,
		principalClaim: strings.Split(principalClaim, "."),
		roleClaim:      strings.Split(roleClaim, "."),
		copy:           opts.ClaimsToCopy,
		parser:         jwt.NewParser(parserOpts...),
	}
	if opts.GroupsClaim != "" {
		a.groupsClaim = strings.Split(opts.GroupsClaim, ".")
	}
	return a, nil
}

// Mode reports the configured mode, for logging and readiness reporting.
func (a *Authenticator) Mode() Mode { return a.mode }

// Identity resolves the caller. Header mode trusts the principal and role
// headers. JWT mode validates a bearer token and ignores both inbound identity
// headers.
func (a *Authenticator) Identity(r *http.Request) (governance.Identity, error) {
	if a.mode == ModeHeader {
		id := headerIdentity(r)
		if id.Principal == "" {
			return governance.Identity{}, fmt.Errorf("%w: no %s header", ErrUnauthenticated, PrincipalHeader)
		}
		return id, nil
	}

	raw, err := bearerToken(r)
	if err != nil {
		return governance.Identity{}, err
	}
	claims := jwt.MapClaims{}
	if _, err := a.parser.ParseWithClaims(raw, claims, a.keyfunc); err != nil {
		return governance.Identity{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}

	principal, ok := stringClaim(claims, a.principalClaim)
	if !ok || strings.TrimSpace(principal) == "" {
		return governance.Identity{}, fmt.Errorf("%w: token carries no non-empty %q principal claim",
			ErrUnauthenticated, strings.Join(a.principalClaim, "."))
	}
	roles := listClaim(claims, a.roleClaim)
	var groups []string
	if len(a.groupsClaim) > 0 {
		groups = listClaim(claims, a.groupsClaim)
	}
	if len(roles) == 0 && len(groups) == 0 {
		return governance.Identity{}, fmt.Errorf("%w: token carries neither a %q role claim nor any group",
			ErrUnauthenticated, strings.Join(a.roleClaim, "."))
	}

	id := governance.Identity{Principal: principal, Groups: dedupe(groups), Roles: dedupe(roles)}
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

// headerIdentity reads the trusted role header. Several roles may be given as
// a comma-separated list, matching the multi-role model of JWT mode.
func headerIdentity(r *http.Request) governance.Identity {
	principal := strings.TrimSpace(r.Header.Get(PrincipalHeader))
	raw := r.Header.Get(RoleHeader)
	if raw == "" {
		return governance.Identity{Principal: principal}
	}
	var roles []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			roles = append(roles, p)
		}
	}
	return governance.Identity{Principal: principal, Roles: dedupe(roles)}
}

// listClaim reads a claim holding an array of strings, which is how issuers
// normally encode group membership. A single string is accepted too, because
// some issuers collapse a one-element list.
func listClaim(claims jwt.MapClaims, path []string) []string {
	v, ok := claimAt(claims, path)
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
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

// claimAt walks a dotted claim path and returns the raw leaf value.
func claimAt(claims map[string]any, path []string) (any, bool) {
	var cur any = claims
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// stringClaim walks a dotted claim path and renders the leaf as a string.
func stringClaim(claims map[string]any, path []string) (string, bool) {
	cur, ok := claimAt(claims, path)
	if !ok {
		return "", false
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
