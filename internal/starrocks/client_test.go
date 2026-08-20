package starrocks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KubedAI/semantic-operator/internal/dbclient"
)

// The guards below all return before any network dial, so these tests need no
// live StarRocks. They lock in the passthrough preconditions.

func TestQueryRejectsExpiredToken(t *testing.T) {
	c, err := Open(Config{Host: "example.invalid", TLSEnabled: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()

	cred := dbclient.EngineCredential{
		Token:      "header.payload.sig",
		EngineUser: "alice",
		Expiry:     time.Now().Add(-time.Hour),
	}
	_, _, err = c.Query(context.Background(), cred, "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("want expiry error, got %v", err)
	}
}

func TestQueryRejectsMissingEngineUser(t *testing.T) {
	c, err := Open(Config{Host: "example.invalid", TLSEnabled: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()

	cred := dbclient.EngineCredential{Token: "header.payload.sig"} // no EngineUser
	_, _, err = c.Query(context.Background(), cred, "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "engine user") {
		t.Fatalf("want missing-engine-user error, got %v", err)
	}
}

func TestQueryRequiresTLSForPassthrough(t *testing.T) {
	c, err := Open(Config{Host: "example.invalid"}) // TLS disabled
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()

	cred := dbclient.EngineCredential{Token: "header.payload.sig", EngineUser: "alice"}
	_, _, err = c.Query(context.Background(), cred, "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("want TLS-required error, got %v", err)
	}
}

func TestClientSupportsPerRequestIdentity(t *testing.T) {
	c, err := Open(Config{Host: "example.invalid", TLSEnabled: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()

	if _, ok := any(c).(dbclient.PerRequestIdentityClient); !ok {
		t.Fatal("StarRocks client must implement dbclient.PerRequestIdentityClient")
	}
}
