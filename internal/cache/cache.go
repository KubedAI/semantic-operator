// Package cache provides the Valkey-backed plan and result caches. Caching
// is an optimization only: a miss or an unreachable Valkey degrades to
// recompute-and-query, never to an error.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache wraps a Redis-protocol client (Valkey). A nil *Cache is a valid
// no-op cache, so callers never branch on "caching enabled".
type Cache struct {
	rdb       *redis.Client
	planTTL   time.Duration
	resultTTL time.Duration
	log       *slog.Logger
}

// Options configures the cache from Helm-provided env.
type Options struct {
	Addr      string // host:port; empty disables caching
	Password  string
	DB        int
	PlanTTL   time.Duration // default 24h
	ResultTTL time.Duration // default 60s
}

// New returns a Cache, or nil (no-op) when Addr is empty.
func New(opts Options, log *slog.Logger) *Cache {
	if opts.Addr == "" {
		return nil
	}
	if opts.PlanTTL == 0 {
		opts.PlanTTL = 24 * time.Hour
	}
	if opts.ResultTTL == 0 {
		opts.ResultTTL = 60 * time.Second
	}
	return &Cache{
		rdb:       redis.NewClient(&redis.Options{Addr: opts.Addr, Password: opts.Password, DB: opts.DB}),
		planTTL:   opts.PlanTTL,
		resultTTL: opts.ResultTTL,
		log:       log,
	}
}

// PlanKey derives the compiled-plan cache key. The model version segment
// makes entries for old model versions unreachable; the request hash already
// includes the effective role, so plans cannot leak across identities. The
// dialect segment keeps plans engine-specific: without it, switching
// SQL_DIALECT on a live install would serve cached SQL from the previous
// engine until the TTL expired. An external authorization fingerprint, when
// present, isolates plans evaluated under different provider decisions.
func PlanKey(dialect, model, modelVersion, requestHash string, authorizationFingerprint ...string) string {
	key := "sl:plan:" + dialect + ":" + model + ":" + modelVersion + ":" + requestHash
	if len(authorizationFingerprint) > 0 && authorizationFingerprint[0] != "" {
		key += ":authz:" + authorizationFingerprint[0]
	}
	return key
}

// ResultKey derives the result cache key from the emitted SQL, which embeds
// governance row filters, so results are role-safe by construction. External
// authorization is checked before every lookup; its fingerprint additionally
// isolates entries for future decision types that affect result scope.
func ResultKey(model, modelVersion, sql string, authorizationFingerprint ...string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(sql))
	if len(authorizationFingerprint) > 0 && authorizationFingerprint[0] != "" {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(authorizationFingerprint[0]))
	}
	return "sl:result:" + model + ":" + modelVersion + ":" + hex.EncodeToString(h.Sum(nil))[:24]
}

func (c *Cache) get(ctx context.Context, key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	b, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if err != redis.Nil {
			c.log.Warn("cache get failed", "key", key, "err", err)
		}
		return nil, false
	}
	return b, true
}

func (c *Cache) set(ctx context.Context, key string, val []byte, ttl time.Duration) {
	if c == nil {
		return
	}
	if err := c.rdb.Set(ctx, key, val, ttl).Err(); err != nil {
		c.log.Warn("cache set failed", "key", key, "err", err)
	}
}

// GetPlan returns a cached plan JSON blob.
func (c *Cache) GetPlan(ctx context.Context, key string) ([]byte, bool) { return c.get(ctx, key) }

// SetPlan stores a plan JSON blob.
func (c *Cache) SetPlan(ctx context.Context, key string, val []byte) {
	c.set(ctx, key, val, c.ttlOr(func() time.Duration { return c.planTTL }))
}

// GetResult returns a cached result JSON blob.
func (c *Cache) GetResult(ctx context.Context, key string) ([]byte, bool) { return c.get(ctx, key) }

// SetResult stores a result JSON blob.
func (c *Cache) SetResult(ctx context.Context, key string, val []byte) {
	c.set(ctx, key, val, c.ttlOr(func() time.Duration { return c.resultTTL }))
}

func (c *Cache) ttlOr(f func() time.Duration) time.Duration {
	if c == nil {
		return 0
	}
	return f()
}

// Ping verifies connectivity for readiness probes. Nil cache is ready.
func (c *Cache) Ping(ctx context.Context) error {
	if c == nil {
		return nil
	}
	return c.rdb.Ping(ctx).Err()
}
