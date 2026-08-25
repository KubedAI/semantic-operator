//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A field is groupable only when its Ossie field contains a dimension
// declaration. Discovery and query validation must enforce the same contract.
func TestExplicitDimensionsDefineGroupableFields(t *testing.T) {
	tok := token(t, "alice")
	base, err := serverBaseURL(cfg.staticNS)
	if err != nil {
		t.Fatalf("server URL: %v", err)
	}
	headers := bearer(tok)

	t.Run("discovery", func(t *testing.T) {
		path := fmt.Sprintf("/v1/models/%s/dimensions", cfg.model)
		status, raw, err := doHTTP(testCtx(t), http.MethodGet, base, path, headers, nil, "")
		if err != nil {
			t.Fatalf("list dimensions: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("list dimensions: want 200, got %d (%s)", status, raw)
		}

		var out struct {
			Dimensions []struct {
				Name string `json:"name"`
			} `json:"dimensions"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode dimensions: %v (%s)", err, raw)
		}

		listed := make(map[string]bool, len(out.Dimensions))
		for _, dim := range out.Dimensions {
			listed[dim.Name] = true
		}
		if !listed[cfg.allowDim] {
			t.Errorf("declared dimension %q is missing from discovery: %s", cfg.allowDim, raw)
		}
		if listed[cfg.nonDim] {
			t.Errorf("undeclared field %q was returned as a dimension: %s", cfg.nonDim, raw)
		}
	})

	t.Run("query enforcement", func(t *testing.T) {
		body, err := json.Marshal(query(cfg.nonDim))
		if err != nil {
			t.Fatalf("encode query: %v", err)
		}
		path := fmt.Sprintf("/v1/models/%s/sql", cfg.model)
		status, raw, err := doHTTP(testCtx(t), http.MethodPost, base, path, headers, body, "application/json")
		if err != nil {
			t.Fatalf("compile query: %v", err)
		}
		if status != http.StatusBadRequest {
			t.Fatalf("undeclared dimension: want 400, got %d (%s)", status, raw)
		}

		var out struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode error: %v (%s)", err, raw)
		}
		if !strings.Contains(strings.ToLower(out.Error), "not a declared dimension") {
			t.Fatalf("undeclared dimension: want declaration error, got %q", out.Error)
		}
	})
}
