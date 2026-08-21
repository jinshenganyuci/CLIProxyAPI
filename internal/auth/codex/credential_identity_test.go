package codex

import (
	"testing"

	"github.com/google/uuid"
)

func TestEnsureCredentialIdentityInitializesAndPreserves(t *testing.T) {
	metadata := map[string]any{}
	first, created, err := EnsureCredentialIdentity(metadata)
	if err != nil {
		t.Fatalf("EnsureCredentialIdentity() error = %v", err)
	}
	if !created {
		t.Fatal("EnsureCredentialIdentity() created = false, want true")
	}
	if _, errParse := uuid.Parse(first); errParse != nil {
		t.Fatalf("namespace %q is not UUID: %v", first, errParse)
	}

	second, createdAgain, err := EnsureCredentialIdentity(metadata)
	if err != nil {
		t.Fatalf("EnsureCredentialIdentity() second error = %v", err)
	}
	if createdAgain {
		t.Fatal("EnsureCredentialIdentity() second created = true, want false")
	}
	if second != first {
		t.Fatalf("preserved namespace = %q, want %q", second, first)
	}
}

func TestDeriveCredentialIdentityInvariants(t *testing.T) {
	namespaceA := uuid.MustParse("5feb40d6-9d60-44f6-9810-19fbb7b21d1f")
	namespaceB := uuid.MustParse("9d0e5a45-9097-499e-ae58-64d74784c2ea")

	a1 := DeriveCredentialIdentity(namespaceA, "installation-id", "client-a")
	a2 := DeriveCredentialIdentity(namespaceA, "installation-id", "client-a")
	b1 := DeriveCredentialIdentity(namespaceB, "installation-id", "client-a")
	aOther := DeriveCredentialIdentity(namespaceA, "installation-id", "client-b")
	if a1 != a2 {
		t.Fatalf("same namespace/value produced %q and %q", a1, a2)
	}
	if a1 == b1 {
		t.Fatal("different credential namespaces produced the same identity")
	}
	if a1 == aOther {
		t.Fatal("different client identifiers produced the same identity")
	}
}

func TestParseCredentialIdentityRejectsPartialOrInvalidMetadata(t *testing.T) {
	tests := []map[string]any{
		{},
		{CredentialIdentityVersionMetadataKey: 1},
		{CredentialIdentityNamespaceMetadataKey: uuid.NewString()},
		{CredentialIdentityVersionMetadataKey: 2, CredentialIdentityNamespaceMetadataKey: uuid.NewString()},
		{CredentialIdentityVersionMetadataKey: 1, CredentialIdentityNamespaceMetadataKey: "not-a-uuid"},
	}
	for _, metadata := range tests {
		if _, _, err := ParseCredentialIdentity(metadata); err == nil {
			t.Fatalf("ParseCredentialIdentity(%v) error = nil, want error", metadata)
		}
	}
}
