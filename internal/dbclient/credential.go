package dbclient

import "time"

// EngineCredential is a per-request identity used to execute a query under the
// caller rather than the engine client's static credential. It carries the
// caller's validated access token for identity propagation.
//
// The zero value means "use the client's own credential", which is the static
// default. A non-empty Token is security sensitive: it must never be placed on
// a governance identity, written to logs or traces, embedded in a SQL comment,
// or mixed into any cache key.
type EngineCredential struct {
	// Token is the caller's validated bearer token, forwarded to the engine.
	Token string
	// EngineUser is the caller's engine session user, resolved from a
	// configured token claim. It is used as the engine session identity so
	// engine-side per-user policy applies to the real caller, and must match
	// the engine's own principal claim.
	EngineUser string
	// Expiry is the token's expiration, so execution can avoid using a token
	// past its lifetime. Zero means unknown.
	Expiry time.Time
}

// IsZero reports whether no credential is set, meaning static execution.
func (c EngineCredential) IsZero() bool { return c.Token == "" }
