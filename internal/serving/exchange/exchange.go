// Package exchange performs OAuth 2.0 Token Exchange (RFC 8693) to obtain an
// engine-audience access token for the caller, keeping the same subject. It
// wraps golang.org/x/oauth2, which handles the token request, client
// authentication, response parsing, and expiry. No cryptography lives here:
// the authorization server signs the exchanged token and the engine verifies
// it.
package exchange

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const (
	grantTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	accessTokenType    = "urn:ietf:params:oauth:token-type:access_token"
	// refreshMargin drops a cached token slightly before it expires so an
	// in-flight query never uses a token that lapses mid-request.
	refreshMargin = 30 * time.Second
)

// ErrExchangeFailed is the generic error surfaced to callers when a token
// exchange fails. The authorization server's error body can carry sensitive
// detail, so it is logged server-side and never returned to the caller.
var ErrExchangeFailed = errors.New("token exchange failed")

// Options configures the exchanger.
type Options struct {
	// TokenURL is the authorization server token endpoint. Required, and must
	// be https unless AllowInsecureHTTP is set.
	TokenURL string
	// ClientID and ClientSecret authenticate the server as the exchange client.
	ClientID     string
	ClientSecret string
	// AllowInsecureHTTP permits a plaintext http TokenURL. The exchange request
	// carries the caller's subject token and the client secret, so this is only
	// acceptable for in-cluster traffic to a trusted endpoint. Off by default.
	AllowInsecureHTTP bool
	// HTTPClient is optional; a default client is used when nil. Its redirect
	// policy is always overridden to refuse redirects.
	HTTPClient *http.Client
	// Logger receives the detailed cause of a failed exchange. Defaults to
	// slog.Default().
	Logger *slog.Logger
}

// Exchanger exchanges a caller access token for an engine-audience token and
// caches the result by the caller principal (a non-secret cache key) until
// shortly before expiry. The caller's subject token is never used as or mixed
// into a cache key.
type Exchanger struct {
	cfg        *oauth2.Config
	httpClient *http.Client
	log        *slog.Logger

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	token  string
	expiry time.Time
}

// New builds an Exchanger.
func New(o Options) (*Exchanger, error) {
	if o.TokenURL == "" {
		return nil, errors.New("exchange: token URL is required")
	}
	if o.ClientID == "" {
		return nil, errors.New("exchange: client id is required")
	}
	u, err := url.Parse(o.TokenURL)
	if err != nil {
		return nil, fmt.Errorf("exchange: invalid token URL: %w", err)
	}
	if u.Scheme != "https" && !o.AllowInsecureHTTP {
		return nil, fmt.Errorf("exchange: token URL %q must use https; set AllowInsecureHTTP to permit plaintext http for in-cluster traffic", o.TokenURL)
	}

	// The exchange request body carries the caller's subject token and the
	// client secret. Never follow redirects: a 3xx from the token endpoint
	// could otherwise resend both to another host.
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if o.HTTPClient != nil {
		cp := *o.HTTPClient
		httpClient = &cp
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("refusing to follow a redirect from the token endpoint")
	}

	logger := o.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Exchanger{
		cfg: &oauth2.Config{
			ClientID:     o.ClientID,
			ClientSecret: o.ClientSecret,
			Endpoint:     oauth2.Endpoint{TokenURL: o.TokenURL, AuthStyle: oauth2.AuthStyleInParams},
		},
		httpClient: httpClient,
		log:        logger,
		cache:      map[string]entry{},
	}, nil
}

// Exchange returns an engine-audience access token for the caller identified by
// cacheKey, serving a cached token when one is still valid. cacheKey must be a
// non-secret, stable caller identifier (the resolved engine principal); the
// subject token is the exchange input only and is never used as a cache key. An
// empty cacheKey disables caching for the call.
func (e *Exchanger) Exchange(ctx context.Context, cacheKey, subjectToken string) (string, time.Time, error) {
	if subjectToken == "" {
		return "", time.Time{}, errors.New("exchange: subject token is empty")
	}
	now := time.Now()

	if cacheKey != "" {
		e.mu.Lock()
		if c, ok := e.cache[cacheKey]; ok {
			if now.Before(c.expiry.Add(-refreshMargin)) {
				e.mu.Unlock()
				return c.token, c.expiry, nil
			}
			delete(e.cache, cacheKey)
		}
		e.mu.Unlock()
	}

	reqCtx := context.WithValue(ctx, oauth2.HTTPClient, e.httpClient)
	tok, err := e.cfg.Exchange(reqCtx, "",
		oauth2.SetAuthURLParam("grant_type", grantTokenExchange),
		oauth2.SetAuthURLParam("subject_token", subjectToken),
		oauth2.SetAuthURLParam("subject_token_type", accessTokenType),
	)
	if err != nil {
		// oauth2.RetrieveError.Error embeds the raw response body when the
		// server returns no RFC 6749 error code, and a reflecting or
		// misconfigured endpoint could echo the subject token there. Log only
		// non-reflective fields, never the raw error, body, or description.
		var rerr *oauth2.RetrieveError
		if errors.As(err, &rerr) {
			status := 0
			if rerr.Response != nil {
				status = rerr.Response.StatusCode
			}
			e.log.Error("token exchange failed", "status", status, "oauthError", rerr.ErrorCode)
		} else {
			// Transport errors reference the endpoint, not the request body that
			// carries the subject token, so they are safe to log.
			e.log.Error("token exchange failed", "err", err)
		}
		return "", time.Time{}, ErrExchangeFailed
	}
	if tok.AccessToken == "" {
		return "", time.Time{}, errors.New("exchange: authorization server returned no access token")
	}

	if cacheKey != "" {
		e.mu.Lock()
		// Opportunistically drop expired entries so the cache cannot grow without
		// bound as callers come and go.
		for k, c := range e.cache {
			if !now.Before(c.expiry) {
				delete(e.cache, k)
			}
		}
		e.cache[cacheKey] = entry{token: tok.AccessToken, expiry: tok.Expiry}
		e.mu.Unlock()
	}
	return tok.AccessToken, tok.Expiry, nil
}
