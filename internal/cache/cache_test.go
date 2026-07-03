package cache

import (
	"context"
	"strings"
	"testing"
)

func TestKeysScopeByModelVersionAndRequest(t *testing.T) {
	a := PlanKey("m", "v1", "req1")
	b := PlanKey("m", "v2", "req1")
	c := PlanKey("m", "v1", "req2")
	if a == b || a == c {
		t.Fatal("plan keys must vary by model version and request hash")
	}
	if !strings.HasPrefix(a, "sl:plan:m:v1:") {
		t.Fatalf("unexpected key shape: %s", a)
	}

	r1 := ResultKey("m", "v1", "SELECT 1")
	r2 := ResultKey("m", "v1", "SELECT 2")
	if r1 == r2 {
		t.Fatal("result keys must vary by SQL")
	}
}

func TestNilCacheIsNoOp(t *testing.T) {
	var c *Cache
	ctx := context.Background()
	if _, ok := c.GetPlan(ctx, "k"); ok {
		t.Fatal("nil cache must miss")
	}
	c.SetPlan(ctx, "k", []byte("v")) // must not panic
	if err := c.Ping(ctx); err != nil {
		t.Fatal("nil cache must be ready")
	}
}
