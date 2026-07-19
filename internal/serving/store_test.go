package serving

import (
	"encoding/json"
	"testing"

	"github.com/KubedAI/ossie-semantic-operator/internal/planner"
)

func modelBlob(t *testing.T, name, version string) []byte {
	t.Helper()
	b, err := json.Marshal(planner.CompiledModel{Name: name, Version: version})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Two resources that declare the same Ossie model name must not overwrite each
// other, and name-based lookup must report the collision instead of guessing.
func TestStoreDuplicateModelNameDoesNotCollide(t *testing.T) {
	s := NewStore()
	if err := s.Put("sm-a-compiled", modelBlob(t, "sales", "v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("sm-b-compiled", modelBlob(t, "sales", "v2")); err != nil {
		t.Fatal(err)
	}

	if got := len(s.All()); got != 2 {
		t.Fatalf("All: want 2 stored models, got %d", got)
	}
	if _, ok := s.Get("sales"); ok {
		t.Fatal("Get(sales): expected ok=false for a duplicated name, not a silent pick")
	}
	if !s.Ambiguous("sales") {
		t.Fatal("Ambiguous(sales): expected true")
	}
	if names := s.Names(); len(names) != 1 || names[0] != "sales" {
		t.Fatalf("Names: want [sales], got %v", names)
	}

	// Deleting one resource resolves the ambiguity and keeps the survivor.
	s.Delete("sm-a-compiled")
	m, ok := s.Get("sales")
	if !ok {
		t.Fatal("Get(sales) after delete: expected ok=true")
	}
	if m.Version != "v2" {
		t.Fatalf("Get(sales): want surviving v2, got %s", m.Version)
	}
	if s.Ambiguous("sales") {
		t.Fatal("Ambiguous(sales) after delete: expected false")
	}
}

func TestStoreDistinctNames(t *testing.T) {
	s := NewStore()
	_ = s.Put("sm-a-compiled", modelBlob(t, "sales", "v1"))
	_ = s.Put("sm-b-compiled", modelBlob(t, "inventory", "v1"))

	if _, ok := s.Single(); ok {
		t.Fatal("Single: expected false with two models")
	}
	if m, ok := s.Get("sales"); !ok || m.Name != "sales" {
		t.Fatalf("Get(sales): want sales, got %v ok=%v", m, ok)
	}
	if m, ok := s.Get("inventory"); !ok || m.Name != "inventory" {
		t.Fatalf("Get(inventory): want inventory, got %v ok=%v", m, ok)
	}
	if names := s.Names(); len(names) != 2 {
		t.Fatalf("Names: want 2, got %v", names)
	}
}

func TestStoreSingle(t *testing.T) {
	s := NewStore()
	_ = s.Put("sm-only-compiled", modelBlob(t, "sales", "v1"))
	m, ok := s.Single()
	if !ok || m.Name != "sales" {
		t.Fatalf("Single: want sales, got %v ok=%v", m, ok)
	}
}

func TestStoreSyncState(t *testing.T) {
	s := NewStore()
	if s.Synced() {
		t.Fatal("Synced: want false before initial informer sync")
	}
	s.MarkSynced()
	if !s.Synced() {
		t.Fatal("Synced: want true after MarkSynced")
	}
}

func TestStoreCount(t *testing.T) {
	s := NewStore()
	if got := s.Count(); got != 0 {
		t.Fatalf("Count: want 0, got %d", got)
	}
	if err := s.Put("sm-a-compiled", modelBlob(t, "sales", "v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("sm-b-compiled", modelBlob(t, "inventory", "v1")); err != nil {
		t.Fatal(err)
	}
	if got := s.Count(); got != 2 {
		t.Fatalf("Count: want 2, got %d", got)
	}
	s.Delete("sm-a-compiled")
	if got := s.Count(); got != 1 {
		t.Fatalf("Count after delete: want 1, got %d", got)
	}
}
