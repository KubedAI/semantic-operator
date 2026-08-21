//go:build e2e

package authe2e

import (
	"fmt"
	"strings"
	"testing"
)

// Passthrough runs the query under the caller identity. alice is unmasked and
// allowed, so a query on an allowed dimension returns real rows.
func TestPassthroughAllowedUserRealValue(t *testing.T) {
	tok := token(t, "alice")
	r, err := queryModel(testCtx(t), cfg.passthroughNS, cfg.model, bearer(tok), allowQuery())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.status != 200 {
		t.Fatalf("alice allowed: want 200, got %d (%s)", r.status, r.raw)
	}
	if r.masked() {
		t.Fatalf("alice allowed: want real value, got masked (%s)", r.raw)
	}
	if len(r.rows) == 0 {
		t.Fatalf("alice allowed: no rows (%s)", r.raw)
	}
}

// bob's masked column is masked by the engine. Skipped on engines with no
// column masking (StarRocks).
func TestPassthroughMaskedColumn(t *testing.T) {
	if cfg.maskDim == "" {
		t.Skipf("engine %q has no column masking", cfg.engine)
	}
	tok := token(t, "bob")
	r, err := queryModel(testCtx(t), cfg.passthroughNS, cfg.model, bearer(tok), maskQuery())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.status != 200 {
		t.Fatalf("bob masked: want 200, got %d (%s)", r.status, r.raw)
	}
	if !r.masked() {
		t.Fatalf("bob masked: want masked REDACTED, got %s", r.raw)
	}
}

// bob is denied the denied table by the engine. The server surfaces the engine
// access-denied rather than returning rows.
func TestPassthroughDeniedTable(t *testing.T) {
	tok := token(t, "bob")
	r, err := queryModel(testCtx(t), cfg.passthroughNS, cfg.model, bearer(tok), denyQuery())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	// Require the access-denied error by name; an empty 200 or an unrelated
	// failure must not count as a denial. Both engines phrase it as "denied".
	if !strings.Contains(strings.ToLower(r.errMsg), "denied") {
		t.Fatalf("bob denied table: want an access-denied error, got status %d (%s)", r.status, r.raw)
	}
}

// bob's denial is table-specific: bob still reads an allowed dimension.
func TestPassthroughDeniedUserAllowedElsewhere(t *testing.T) {
	tok := token(t, "bob")
	r, err := queryModel(testCtx(t), cfg.passthroughNS, cfg.model, bearer(tok), allowQuery())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.status != 200 {
		t.Fatalf("bob allowed table: want 200, got %d (%s)", r.status, r.raw)
	}
}

// Static mode runs every query under the server's own identity, so all callers
// get identical results.
func TestStaticAllUsersIdentical(t *testing.T) {
	var first string
	for i, user := range []string{"alice", "bob", "carol"} {
		tok := token(t, user)
		r, err := queryModel(testCtx(t), cfg.staticNS, cfg.model, bearer(tok), minimalQuery())
		if err != nil {
			t.Fatalf("%s request: %v", user, err)
		}
		if r.status != 200 {
			t.Fatalf("%s static: want 200, got %d (%s)", user, r.status, r.raw)
		}
		got := fmt.Sprintf("%v", r.rows)
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("%s static: rows %s differ from alice's %s; static mode must be uniform", user, got, first)
		}
	}
}
