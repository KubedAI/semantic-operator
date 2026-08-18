// Package exchange performs OAuth 2.0 Token Exchange (RFC 8693) to obtain an
// engine-audience access token for the caller, keeping the same subject. It
// wraps golang.org/x/oauth2, which handles the token request, client
// authentication, response parsing, and expiry. No cryptography lives here:
// the authorization server signs the exchanged token and the engine verifies
// it.
package exchange

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
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

// Options configures the exchanger.
type Options struct {
	// TokenURL is the authorization server token endpoint. Required.
	TokenURL string
	// ClientID and ClientSecret authenticate the server as the exchange client.
	ClientID     string
	ClientSecret string
	// HTTPClient is optional; a default client is used when nil.
	HTTPClient *http.Client
}

// Exchanger exchanges a caller access token for an engine-audience token and
// caches the result by subject token until shortly before expiry.
type Exchanger struct {
	cfg        *oauth2.Config
	httpClient *http.Client

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
	return &Exchanger{
		cfg: &oauth2.Config{
			ClientID:     o.ClientID,
			ClientSecret: o.ClientSecret,
			Endpoint:     oauth2.Endpoint{TokenURL: o.TokenURL, AuthStyle: oauth2.AuthStyleInParams},
		},
		httpClient: o.HTTPClient,
		cache:      map[string]entry{},
	}, nil
}

// Exchange returns an engine-audience access token for the given caller token,
// serving a cached token when one is still valid.
func (e *Exchanger) Exchange(ctx context.Context, subjectToken string) (string, time.Time, error) {
	if subjectToken == "" {
		return "", time.Time{}, errors.New("exchange: subject token is empty")
	}
	key := hash(subjectToken)
	now := time.Now()

	e.mu.Lock()
	if c, ok := e.cache[key]; ok {
		if now.Before(c.expiry.Add(-refreshMargin)) {
			e.mu.Unlock()
			return c.token, c.expiry, nil
		}
		delete(e.cache, key)
	}
	e.mu.Unlock()

	reqCtx := ctx
	if e.httpClient != nil {
		reqCtx = context.WithValue(ctx, oauth2.HTTPClient, e.httpClient)
	}
	tok, err := e.cfg.Exchange(reqCtx, "",
		oauth2.SetAuthURLParam("grant_type", grantTokenExchange),
		oauth2.SetAuthURLParam("subject_token", subjectToken),
		oauth2.SetAuthURLParam("subject_token_type", accessTokenType),
	)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("exchange: %w", err)
	}
	if tok.AccessToken == "" {
		return "", time.Time{}, errors.New("exchange: authorization server returned no access token")
	}

	e.mu.Lock()
	// Opportunistically drop expired entries so the cache cannot grow without
	// bound as caller tokens rotate.
	for k, c := range e.cache {
		if !now.Before(c.expiry) {
			delete(e.cache, k)
		}
	}
	e.cache[key] = entry{token: tok.AccessToken, expiry: tok.Expiry}
	e.mu.Unlock()
	return tok.AccessToken, tok.Expiry, nil
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
