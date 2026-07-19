package serving

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KubedAI/ossie-semantic-operator/internal/planner"
)

func identityBlob(t *testing.T, name, version, ns, resource string) []byte {
	t.Helper()
	b, err := json.Marshal(planner.CompiledModel{
		Name: name, Version: version, Namespace: ns, Resource: resource,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A model-name collision must be attributable: the listing carries the
// publishing resource, and the resolve error names both resources.
func TestModelsListingCarriesResourceIdentity(t *testing.T) {
	store := NewStore()
	_ = store.Put("sm-a-compiled", identityBlob(t, "sales", "v1", "team-a", "retail"))
	_ = store.Put("sm-b-compiled", identityBlob(t, "sales", "v1", "team-b", "retail"))
	svc := &Service{Store: store}

	models := svc.Models()
	if len(models) != 2 {
		t.Fatalf("Models: want both colliding entries listed, got %d", len(models))
	}
	for _, m := range models {
		if m.Namespace == "" || m.Resource == "" {
			t.Fatalf("Models: entry missing identity: %+v", m)
		}
	}

	_, err := svc.Resolve("sales")
	if err == nil {
		t.Fatal("Resolve(sales): expected ambiguity error")
	}
	for _, ref := range []string{"team-a/retail", "team-b/retail"} {
		if !strings.Contains(err.Error(), ref) {
			t.Fatalf("ambiguity error should name %s, got: %v", ref, err)
		}
	}
}

func TestResolveUnknownModel(t *testing.T) {
	store := NewStore()
	_ = store.Put("sm-a-compiled", identityBlob(t, "sales", "v1", "team-a", "retail"))
	svc := &Service{Store: store}
	if _, err := svc.Resolve("inventory"); err == nil {
		t.Fatal("Resolve(inventory): expected unknown-model error")
	}
	if m, err := svc.Resolve("sales"); err != nil || m.Name != "sales" {
		t.Fatalf("Resolve(sales): want ok, got %v err=%v", m, err)
	}
}

func TestResolveUnnamedAmbiguousSingleName(t *testing.T) {
	store := NewStore()
	_ = store.Put("sm-a-compiled", identityBlob(t, "sales", "v1", "team-a", "retail"))
	_ = store.Put("sm-b-compiled", identityBlob(t, "sales", "v2", "team-b", "retail"))
	svc := &Service{Store: store}

	_, err := svc.Resolve("")
	if err == nil {
		t.Fatal("Resolve(\"\"): expected ambiguity error")
	}
	for _, want := range []string{
		`model name "sales" is published by more than one resource`,
		"team-a/retail",
		"team-b/retail",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Resolve(\"\") error should contain %q, got: %v", want, err)
		}
	}
}
