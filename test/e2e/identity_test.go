//go:build e2e

package e2e

import (
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

// Static mode runs every query under the server's own engine credential.
func TestStaticDropsCallerMasking(t *testing.T) {
	if cfg.maskDim == "" {
		t.Skipf("engine %q has no column masking", cfg.engine)
	}
	tok := token(t, "bob")
	r, err := queryModel(testCtx(t), cfg.staticNS, cfg.model, bearer(tok), maskQuery())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.status != 200 {
		t.Fatalf("bob static masked column: want 200, got %d (%s)", r.status, r.raw)
	}
	if r.masked() {
		t.Fatalf("bob static masked column: want real value, got masked (%s)", r.raw)
	}
	if len(r.rows) == 0 {
		t.Fatalf("bob static masked column: no rows (%s)", r.raw)
	}
}

// bob reads the denied table in static mode.
// In passthrough mode the same query
// returns an access-denied error.
func TestStaticDropsCallerDenial(t *testing.T) {
	tok := token(t, "bob")
	r, err := queryModel(testCtx(t), cfg.staticNS, cfg.model, bearer(tok), denyQuery())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.status != 200 {
		t.Fatalf("bob static denied table: want 200, got %d (%s)", r.status, r.raw)
	}
	if strings.Contains(strings.ToLower(r.errMsg), "denied") {
		t.Fatalf("bob static denied table: want real rows, got access denied (%s)", r.raw)
	}
	if len(r.rows) == 0 {
		t.Fatalf("bob static denied table: no rows (%s)", r.raw)
	}
}

// bob is denied this table under his own identity,
// Numbers are compared with a tolerance, because the engine sums a
// DOUBLE column in an order that is not fixed across runs.
func TestStaticSameForDeniedAndAllowedCaller(t *testing.T) {
	q := denyQuery()
	alice, err := queryModel(testCtx(t), cfg.staticNS, cfg.model, bearer(token(t, "alice")), q)
	if err != nil {
		t.Fatalf("alice request: %v", err)
	}
	bob, err := queryModel(testCtx(t), cfg.staticNS, cfg.model, bearer(token(t, "bob")), q)
	if err != nil {
		t.Fatalf("bob request: %v", err)
	}
	if alice.status != 200 || bob.status != 200 {
		t.Fatalf("static: want 200/200, got %d/%d (alice %s) (bob %s)", alice.status, bob.status, alice.raw, bob.raw)
	}
	if len(alice.rows) == 0 || len(bob.rows) == 0 {
		t.Fatalf("static: empty rows (alice %s) (bob %s)", alice.raw, bob.raw)
	}
	if !rowsClose(alice.rows, bob.rows, 1e-9) {
		t.Fatalf("static: bob rows %v differ from alice's %v beyond tolerance; static mode must be uniform", bob.rows, alice.rows)
	}
}
