package confload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeKey(t *testing.T) {
	cases := map[string]string{
		"engine.connection.host":   "ENGINECONNECTIONHOST",
		"ENGINE__CONNECTION__HOST": "ENGINECONNECTIONHOST",
		"ENGINE_CONNECTION_HOST":   "ENGINECONNECTIONHOST",
		"auth.jwksURL":             "AUTHJWKSURL",
		"":                         "",
	}
	for in, want := range cases {
		if got := normalizeKey(in); got != want {
			t.Errorf("normalizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadEnvIsCaseAndUnderscoreInsensitive(t *testing.T) {
	type inner struct {
		JWKSURL string `yaml:"jwksURL"`
	}
	type cfg struct {
		Auth inner `yaml:"auth"`
	}
	t.Setenv(EnvPrefix+"AUTH__JWKS_URL", "https://idp/jwks")

	got, err := Load(cfg{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Auth.JWKSURL != "https://idp/jwks" {
		t.Errorf("auth.jwksURL = %q, want the env value", got.Auth.JWKSURL)
	}
}

func TestLoadRejectsCollidingKeys(t *testing.T) {
	type cfg struct {
		A string `yaml:"fooBar"`
		B string `yaml:"foo_bar"`
	}
	_, err := Load(cfg{}, "")
	if err == nil {
		t.Fatal("expected an error for colliding keys, got nil")
	}
	if !strings.Contains(err.Error(), "normalize") {
		t.Errorf("error = %v, want a normalization-collision message", err)
	}
}

func TestLoadRejectsUnknownEnvVar(t *testing.T) {
	type cfg struct {
		Host string `yaml:"host"`
	}
	t.Setenv(EnvPrefix+"NOPE__NOT_A_KEY", "x")
	_, err := Load(cfg{}, "")
	if err == nil {
		t.Fatal("expected an error for an unknown env var, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") || !strings.Contains(err.Error(), "NOT_A_KEY") {
		t.Errorf("error = %v, want it to name the unknown variable", err)
	}
}

func TestLoadRejectsUnknownFileKey(t *testing.T) {
	type cfg struct {
		Host string `yaml:"host"`
	}
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte("host: h\nbogus: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(cfg{}, path)
	if err == nil {
		t.Fatal("expected an error for an unknown file key, got nil")
	}
	if !strings.Contains(err.Error(), "invalid keys") || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error = %v, want it to name the unknown key", err)
	}
}

func TestLoadSliceFromEnvIsCommaSeparated(t *testing.T) {
	type cfg struct {
		Claims []string `yaml:"claims"`
	}
	t.Setenv(EnvPrefix+"CLAIMS", "tenant, sub ,groups")
	got, err := Load(cfg{}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tenant", "sub", "groups"}
	if len(got.Claims) != len(want) {
		t.Fatalf("claims = %v, want %v", got.Claims, want)
	}
	for i := range want {
		if got.Claims[i] != want[i] {
			t.Fatalf("claims = %v, want %v", got.Claims, want)
		}
	}
}

func TestLoadDurationRequiresUnit(t *testing.T) {
	type cfg struct {
		Timeout time.Duration `yaml:"timeout"`
	}
	// A bare number (no unit) must be rejected rather than read as nanoseconds.
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte("timeout: 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cfg{}, path); err == nil {
		t.Fatal("expected an error for a unit-less duration, got nil")
	}

	// With a unit it parses.
	if err := os.WriteFile(path, []byte("timeout: 30s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(cfg{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, want 30s", got.Timeout)
	}

	// A unit-less env duration is rejected too.
	t.Setenv(EnvPrefix+"TIMEOUT", "45")
	if _, err := Load(cfg{}, ""); err == nil {
		t.Fatal("expected an error for a unit-less env duration, got nil")
	}
}
