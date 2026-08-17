package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validAuthorizationProvidersYAML = `
- name: corp-opa
  type: opa
  url: https://opa:8181
  timeoutSeconds: 1
  bearerTokenEnv: AUTHORIZATION_PROVIDER_TOKEN_OPA
  opa:
    decisionPath: semantic/query/allow
`

func TestAuthorizationRegistryFromInlineYAML(t *testing.T) {
	t.Setenv("AUTHORIZATION_PROVIDER_TOKEN_OPA", "secret")
	t.Setenv(authorizationProvidersEnv, validAuthorizationProvidersYAML)
	t.Setenv(authorizationProvidersFileEnv, "")

	registry, err := authorizationRegistryFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if registry == nil {
		t.Fatal("provider configuration returned a nil registry")
	}
}

func TestAuthorizationRegistryFromRangerYAML(t *testing.T) {
	t.Setenv("AUTHORIZATION_PROVIDER_TOKEN_RANGER", "service-token")
	t.Setenv(authorizationProvidersEnv, `
- name: corp-ranger
  type: ranger
  url: https://ranger-pdp:6500/authz/v1
  bearerTokenEnv: AUTHORIZATION_PROVIDER_TOKEN_RANGER
  ranger:
    authenticationMode: service
    serviceType: semantic-operator
    serviceName: semantic-prod
    resource: "semantic-model:namespace={namespace},model={resource}"
    permission: query
    contextAttributes:
      environment: production
      clusterName: analytics-prod
`)
	t.Setenv(authorizationProvidersFileEnv, "")
	registry, err := authorizationRegistryFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if registry == nil {
		t.Fatal("Ranger provider configuration returned a nil registry")
	}
}

func TestAuthorizationRegistryFromYAMLFile(t *testing.T) {
	t.Setenv("AUTHORIZATION_PROVIDER_TOKEN_OPA", "secret")
	t.Setenv(authorizationProvidersEnv, "")
	path := filepath.Join(t.TempDir(), "providers.yaml")
	if err := os.WriteFile(path, []byte(validAuthorizationProvidersYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(authorizationProvidersFileEnv, path)

	registry, err := authorizationRegistryFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if registry == nil {
		t.Fatal("provider file returned a nil registry")
	}
}

func TestAuthorizationRegistryAllowsNoConfigurationOrEmptyArray(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inline string
	}{
		{name: "no source"},
		{name: "empty array", inline: `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(authorizationProvidersEnv, tc.inline)
			t.Setenv(authorizationProvidersFileEnv, "")
			registry, err := authorizationRegistryFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if registry == nil {
				t.Fatal("empty provider configuration returned a nil registry")
			}
		})
	}
}

func TestAuthorizationRegistryRejectsSourceErrors(t *testing.T) {
	t.Run("both sources", func(t *testing.T) {
		t.Setenv(authorizationProvidersEnv, `[]`)
		t.Setenv(authorizationProvidersFileEnv, "/tmp/providers.yaml")
		_, err := authorizationRegistryFromEnv()
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("error = %v, want mutually exclusive", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		t.Setenv(authorizationProvidersEnv, "")
		path := filepath.Join(t.TempDir(), "missing.yaml")
		t.Setenv(authorizationProvidersFileEnv, path)
		_, err := authorizationRegistryFromEnv()
		if err == nil || !strings.Contains(err.Error(), authorizationProvidersFileEnv) {
			t.Fatalf("error = %v, want file source", err)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		t.Setenv(authorizationProvidersEnv, "")
		path := filepath.Join(t.TempDir(), "providers.yaml")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(authorizationProvidersFileEnv, path)
		_, err := authorizationRegistryFromEnv()
		if err == nil || !strings.Contains(err.Error(), "YAML array") {
			t.Fatalf("error = %v, want YAML array", err)
		}
	})
}

func TestAuthorizationRegistryRejectsBadYAMLConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "null", raw: `null`, want: "YAML array"},
		{name: "blank-padded name", raw: `[{name: " corp-opa ", type: opa, url: "http://opa:8181", opa: {decisionPath: p}}]`, want: "lowercase DNS label"},
		{name: "uppercase name", raw: `[{name: Corp-opa, type: opa, url: "http://opa:8181", opa: {decisionPath: p}}]`, want: "lowercase DNS label"},
		{name: "underscore name", raw: `[{name: corp_opa, type: opa, url: "http://opa:8181", opa: {decisionPath: p}}]`, want: "lowercase DNS label"},
		{name: "leading hyphen name", raw: `[{name: -corp-opa, type: opa, url: "http://opa:8181", opa: {decisionPath: p}}]`, want: "lowercase DNS label"},
		{name: "trailing hyphen name", raw: `[{name: corp-opa-, type: opa, url: "http://opa:8181", opa: {decisionPath: p}}]`, want: "lowercase DNS label"},
		{name: "unknown field", raw: `[{name: p, type: opa, url: "http://opa:8181", opa: {decisionPath: p}, extra: true}]`, want: "unknown field \"extra\""},
		{name: "duplicate field", raw: "- name: p\n  name: q\n  type: opa\n  url: http://opa:8181\n  opa: {decisionPath: p}\n", want: "already set"},
		{name: "unknown OPA field", raw: `[{name: p, type: opa, url: "http://opa:8181", opa: {decisionPath: p, extra: true}}]`, want: "unknown field \"extra\""},
		{name: "legacy endpoint", raw: `[{name: p, type: opa, endpoint: "http://opa:8181", opa: {decisionPath: p}}]`, want: "unknown field \"endpoint\""},
		{name: "unknown type", raw: `[{name: p, type: wat, url: "http://provider:6080"}]`, want: "not supported"},
		{name: "missing url", raw: `[{name: p, type: opa, opa: {decisionPath: p}}]`, want: ".url is required"},
		{name: "missing OPA block", raw: `[{name: p, type: opa, url: "http://opa:8181"}]`, want: ".opa is required"},
		{name: "Ranger block on OPA", raw: `[{name: p, type: opa, url: "http://opa:8181", opa: {decisionPath: p}, ranger: {authenticationMode: service}}]`, want: ".ranger is not allowed"},
		{name: "missing decision path", raw: `[{name: p, type: opa, url: "http://opa:8181", opa: {}}]`, want: ".opa.decisionPath is required"},
		{name: "missing Ranger block", raw: `[{name: p, type: ranger, url: "http://ranger:6500/authz/v1"}]`, want: ".ranger is required"},
		{name: "OPA block on Ranger", raw: `[{name: p, type: ranger, url: "http://ranger:6500/authz/v1", opa: {decisionPath: p}, ranger: {authenticationMode: service}}]`, want: ".opa is not allowed"},
		{name: "unsupported Ranger auth mode", raw: `[{name: p, type: ranger, url: "http://ranger:6500/authz/v1", ranger: {authenticationMode: passthrough}}]`, want: "authenticationMode must be service"},
		{name: "missing Ranger service type", raw: `[{name: p, type: ranger, url: "http://ranger:6500/authz/v1", ranger: {authenticationMode: service, serviceName: s, resource: "model:{resource}", permission: query}}]`, want: "serviceType is required"},
		{name: "managed Ranger context", raw: `[{name: p, type: ranger, url: "http://ranger:6500/authz/v1", ranger: {authenticationMode: service, serviceType: semantic, serviceName: s, resource: "model:{resource}", permission: query, contextAttributes: {requestData: forged}}}]`, want: "managed by the Ranger provider"},
		{name: "duplicate provider", raw: `[{name: p, type: opa, url: "http://opa:8181", opa: {decisionPath: p}}, {name: p, type: opa, url: "http://opa:8181", opa: {decisionPath: p}}]`, want: "more than once"},
		{name: "missing token", raw: `[{name: p, type: opa, url: "https://opa:8181", bearerTokenEnv: AUTHORIZATION_PROVIDER_TOKEN_MISSING, opa: {decisionPath: p}}]`, want: "AUTHORIZATION_PROVIDER_TOKEN_MISSING"},
		{name: "unrelated secret alias", raw: `[{name: p, type: opa, url: "https://attacker.example", bearerTokenEnv: ENGINE_PASSWORD, opa: {decisionPath: p}}]`, want: "must match AUTHORIZATION_PROVIDER_TOKEN_"},
		{name: "multiple documents", raw: "[]\n---\n[]\n", want: "exactly one YAML document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(authorizationProvidersEnv, tc.raw)
			t.Setenv(authorizationProvidersFileEnv, "")
			_, err := authorizationRegistryFromEnv()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}
