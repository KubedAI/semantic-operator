package serving

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/KubedAI/semantic-operator/internal/dbclient"
	"github.com/KubedAI/semantic-operator/internal/serving/exchange"
)

// CredentialResolver turns the caller's authenticated identity into the engine
// credential used to execute a query. The zero credential means static
// execution under the engine client's own credential.
type CredentialResolver func(ctx context.Context, token, engineUser string, expiry time.Time) (dbclient.EngineCredential, error)

// StaticResolver always executes under the engine client's own credential.
func StaticResolver() CredentialResolver {
	return func(context.Context, string, string, time.Time) (dbclient.EngineCredential, error) {
		return dbclient.EngineCredential{}, nil
	}
}

// PassthroughResolver forwards the caller's own token to the engine.
func PassthroughResolver() CredentialResolver {
	return func(_ context.Context, token, engineUser string, expiry time.Time) (dbclient.EngineCredential, error) {
		return dbclient.EngineCredential{Token: token, EngineUser: engineUser, Expiry: expiry}, nil
	}
}

// ExchangeResolver exchanges the caller's token for an engine-audience token
// via the exchanger, then verifies the exchange stayed same-subject by
// comparing the engine-user claim of the exchanged token with the caller's
// engine user. A mismatch fails closed.
func ExchangeResolver(ex *exchange.Exchanger, engineUserClaim string) CredentialResolver {
	path := strings.Split(engineUserClaim, ".")
	return func(ctx context.Context, token, engineUser string, _ time.Time) (dbclient.EngineCredential, error) {
		exchanged, expiry, err := ex.Exchange(ctx, token)
		if err != nil {
			return dbclient.EngineCredential{}, err
		}
		got, ok := jwtClaimString(exchanged, path)
		if !ok || got != engineUser {
			return dbclient.EngineCredential{}, fmt.Errorf("exchanged token engine user %q does not match caller %q", got, engineUser)
		}
		return dbclient.EngineCredential{Token: exchanged, EngineUser: engineUser, Expiry: expiry}, nil
	}
}

// Caller carries the authenticated caller's engine-relevant identity from the
// request boundary to the point where the credential is resolved. It is used
// by the MCP adapter, whose tool boundary only sees a context.
type Caller struct {
	Token      string
	EngineUser string
	Expiry     time.Time
}

type callerKey struct{}

// WithCaller stores the caller identity in the context.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom reads the caller identity; the zero value means no caller token.
func CallerFrom(ctx context.Context) Caller {
	if c, ok := ctx.Value(callerKey{}).(Caller); ok {
		return c
	}
	return Caller{}
}

// jwtClaimString decodes an unverified JWT payload and returns a dotted string
// claim. The signature is not checked here on purpose: this only confirms the
// exchange stayed same-subject, and the engine independently verifies the
// token's signature before trusting it.
func jwtClaimString(token string, path []string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false
	}
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
	s, ok := cur.(string)
	return s, ok
}
