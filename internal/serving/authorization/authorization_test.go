package authorization

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/planner"
)

type stubProvider struct {
	decision Decision
	err      error
	input    Input
}

func (s *stubProvider) Decide(_ context.Context, input Input) (Decision, error) {
	s.input = input
	return s.decision, s.err
}

func TestRegistryAllowsAndForwardsDeterministicInput(t *testing.T) {
	provider := &stubProvider{decision: Decision{Allow: true, Revision: "bundle-7"}}
	registry := NewRegistry()
	if err := registry.Register("corp-opa", provider); err != nil {
		t.Fatal(err)
	}
	model := &planner.CompiledModel{Name: "retail", Version: "v1", Namespace: "analytics", Resource: "retail-model"}
	input := NewQueryInput(model, planner.Request{Metrics: []string{"revenue"}}, governance.Identity{
		Principal: "alice", Groups: []string{"analysts"}, Roles: []string{"regional", "analyst"},
		Claims: map[string]string{"tenant": "acme"},
	}, Environment{AccessTimeUnixMilli: 1234, Adapter: "rest"})
	decision, err := registry.Authorize(context.Background(), "corp-opa", input)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allow || decision.Revision != "bundle-7" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if provider.input.Model.Resource != "retail-model" {
		t.Fatalf("provider received wrong input: %+v", provider.input)
	}
	if !reflect.DeepEqual(provider.input.Identity.Roles, []string{"analyst", "regional"}) ||
		!reflect.DeepEqual(provider.input.Identity.Groups, []string{"analysts"}) {
		t.Fatalf("identity sets were not normalized: %+v", provider.input.Identity)
	}
	if provider.input.APIVersion != InputAPIVersion || provider.input.Environment.Adapter != "rest" ||
		provider.input.Environment.AccessTimeUnixMilli != 1234 {
		t.Fatalf("authorization contract metadata was not forwarded: %+v", provider.input)
	}
}

func TestRegistryDistinguishesDenyAndUnavailable(t *testing.T) {
	registry := NewRegistry()
	deny := &stubProvider{decision: Decision{Allow: false}}
	broken := &stubProvider{err: errors.New("connection refused")}
	if err := registry.Register("deny", deny); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("broken", broken); err != nil {
		t.Fatal(err)
	}
	input := Input{Action: ActionQuery}
	if _, err := registry.Authorize(context.Background(), "deny", input); !errors.Is(err, governance.ErrUnauthorized) {
		t.Fatalf("deny = %v, want ErrUnauthorized", err)
	}
	if _, err := registry.Authorize(context.Background(), "broken", input); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("provider failure = %v, want ErrUnavailable", err)
	}
	if _, err := registry.Authorize(context.Background(), "missing", input); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown provider = %v, want ErrUnavailable", err)
	}
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("opa", &stubProvider{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("opa", &stubProvider{}); err == nil {
		t.Fatal("duplicate provider was accepted")
	}
}

func TestRegisterRejectsUnreferenceableNames(t *testing.T) {
	for _, name := range []string{"", " corp-opa ", "Corp_OPA", "-opa", "opa-", "opa/provider"} {
		t.Run(name, func(t *testing.T) {
			if err := NewRegistry().Register(name, &stubProvider{}); err == nil {
				t.Fatalf("invalid provider name %q was accepted", name)
			}
		})
	}
}

func TestFingerprintIsStableAndScopesIdentityProviderAndRevision(t *testing.T) {
	decision := Decision{Allow: true, Revision: "v1"}
	identity := governance.Identity{Principal: "alice", Groups: []string{"analysts"}}
	a := Fingerprint("opa", identity, decision)
	b := Fingerprint("opa", identity, decision)
	if a != b || len(a) != 64 {
		t.Fatalf("fingerprint is not stable full-width sha256: %q %q", a, b)
	}
	if Fingerprint("opa", identity, Decision{Allow: true, Revision: "v2"}) == a {
		t.Fatal("policy revision did not scope fingerprint")
	}
	if Fingerprint("other", identity, decision) == a {
		t.Fatal("provider did not scope fingerprint")
	}
	if Fingerprint("opa", governance.Identity{Principal: "bob", Groups: []string{"analysts"}}, decision) == a {
		t.Fatal("principal did not scope fingerprint")
	}
	if Fingerprint("opa", governance.Identity{Principal: "alice", Roles: []string{"analysts"}}, decision) == a {
		t.Fatal("group and role identities collided")
	}
}

func TestNewQueryInputAppliesBuiltInDefaultRole(t *testing.T) {
	model := &planner.CompiledModel{
		Governance: &v1alpha1.GovernanceSpec{DefaultRole: "analyst"},
	}
	input := NewQueryInput(model, planner.Request{}, governance.Identity{}, Environment{})
	if !reflect.DeepEqual(input.Identity.Roles, []string{"analyst"}) {
		t.Fatalf("effective roles = %v, want default role", input.Identity.Roles)
	}
}
