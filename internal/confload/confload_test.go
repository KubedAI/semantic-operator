package confload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadEnvOverrideByTag(t *testing.T) {
	type inner struct {
		JWKSURL string `yaml:"jwksURL" env:"AUTH_JWKS_URL"`
	}
	type cfg struct {
		Auth inner `yaml:"auth"`
	}
	t.Setenv(EnvPrefix+"AUTH_JWKS_URL", "https://idp/jwks")

	got, err := Load(cfg{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Auth.JWKSURL != "https://idp/jwks" {
		t.Errorf("auth.jwksURL = %q, want the env value", got.Auth.JWKSURL)
	}
}

func TestLoadRejectsDuplicateEnvTag(t *testing.T) {
	type cfg struct {
		A string `yaml:"a" env:"DUP"`
		B string `yaml:"b" env:"DUP"`
	}
	_, err := Load(cfg{}, "")
	if err == nil {
		t.Fatal("expected an error for a duplicate env tag, got nil")
	}
	if !strings.Contains(err.Error(), "DUP") {
		t.Errorf("error = %v, want it to name the duplicated tag", err)
	}
}

func TestLoadRejectsUnknownEnvVar(t *testing.T) {
	type cfg struct {
		Host string `yaml:"host" env:"HOST"`
	}
	t.Setenv(EnvPrefix+"NOT_A_KEY", "x")
	_, err := Load(cfg{}, "")
	if err == nil {
		t.Fatal("expected an error for an unknown env var, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") || !strings.Contains(err.Error(), "NOT_A_KEY") {
		t.Errorf("error = %v, want it to name the unknown variable", err)
	}
}

func TestLoadUntaggedFieldIsNotEnvOverridable(t *testing.T) {
	type cfg struct {
		// No env tag: not settable from the environment.
		Secret string `yaml:"secret"`
		Host   string `yaml:"host" env:"HOST"`
	}
	// This var matches no env tag and must be rejected, not silently applied.
	t.Setenv(EnvPrefix+"SECRET", "x")
	_, err := Load(cfg{}, "")
	if err == nil || !strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error = %v, want unknown-var error naming SECRET", err)
	}
}

func TestLoadRejectsUnknownFileKey(t *testing.T) {
	type cfg struct {
		Host string `yaml:"host" env:"HOST"`
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
		Claims []string `yaml:"claims" env:"CLAIMS"`
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
		Timeout time.Duration `yaml:"timeout" env:"TIMEOUT"`
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
