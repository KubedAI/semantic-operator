//go:build e2e

package authe2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// setupCluster stands up the auth e2e for one engine. It composes the bash
// primitives rather than reimplementing them: keycloak-deploy and the engine
// setup (engine plus data and grants), then auth-operator.sh once per identity
// mode into its own namespace. The engine and mode matrix lives in the profile,
// not spread across the scripts.
func setupCluster(p profile) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	base := clusterEnv()

	if err := run(root, base, "make", "keycloak-deploy"); err != nil {
		return fmt.Errorf("keycloak-deploy: %w", err)
	}
	if err := run(root, base, "make", p.setup...); err != nil {
		return fmt.Errorf("engine setup %v: %w", p.setup, err)
	}
	script := filepath.Join(root, "hack", "auth-operator.sh")
	// Deploy each mode into the namespace the harness queries, so an override
	// of either stays in sync.
	modes := []struct{ name, ns string }{
		{"static", cfg.staticNS},
		{"passthrough", cfg.passthroughNS},
		{"exchange", cfg.exchangeNS},
	}
	for _, m := range modes {
		env := append(clusterEnv(),
			"KIND_ENGINE_TYPE="+p.engine,
			"KIND_NAMESPACE="+m.ns,
			"AUTH_IDENTITY_MODE="+m.name,
		)
		if err := run(root, env, script); err != nil {
			return fmt.Errorf("auth-operator %s/%s: %w", p.engine, m.name, err)
		}
	}
	return nil
}

// clusterEnv passes the harness's kubeconfig and cluster name down to the
// scripts, so setup and assertions target the same cluster.
func clusterEnv() []string {
	var env []string
	if cfg.kubeconfig != "" {
		env = append(env, "KIND_KUBECONFIG="+cfg.kubeconfig)
	}
	if cluster := strings.TrimPrefix(cfg.context, "kind-"); cluster != "" {
		env = append(env, "KIND_CLUSTER_NAME="+cluster)
	}
	return env
}

// run executes a command from the repo root with the base environment plus the
// given additions, streaming output so progress is visible.
func run(dir string, extraEnv []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("resolve repo root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
