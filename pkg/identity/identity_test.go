package identity_test

import (
	"strings"
	"testing"

	"agent-compose/pkg/identity"
)

func TestNewIDAndShortID(t *testing.T) {
	id := identity.NewID(identity.ResourceProject, "demo", "/repo/agent-compose.yml")
	if strings.HasPrefix(id, identity.Prefix) || len(id) != 64 {
		t.Fatalf("NewID = %q, want bare SHA-256 hex", id)
	}
	if !identity.IsID(id) {
		t.Fatalf("NewID = %q, want sha256 id", id)
	}
	if got := identity.ShortID(id); len(got) != 12 || !identity.IsShortID(got) {
		t.Fatalf("ShortID(%q) = %q, want 12 lowercase hex chars", id, got)
	}
	if again := identity.NewID(identity.ResourceProject, "demo", "/repo/agent-compose.yml"); again != id {
		t.Fatalf("NewID changed: %q != %q", again, id)
	}
	if other := identity.NewID(identity.ResourceAgent, "demo", "/repo/agent-compose.yml"); other == id {
		t.Fatalf("NewID ignored resource kind: %q", id)
	}
}

func TestNewRandomID(t *testing.T) {
	first := identity.NewRandomID(identity.ResourceRun)
	second := identity.NewRandomID(identity.ResourceRun)
	if !identity.IsID(first) || !identity.IsID(second) {
		t.Fatalf("NewRandomID returned invalid ids: %q %q", first, second)
	}
	if first == second {
		t.Fatalf("NewRandomID returned duplicate ids: %q", first)
	}
}

func TestIDValidationRejectsInvalidForms(t *testing.T) {
	for _, value := range []string{
		"",
		"not-a-sha256-identity",
		"sha256:123",
		"sha256:" + strings.Repeat("g", 64),
		"SHA256:" + strings.Repeat("a", 64),
	} {
		if identity.IsID(value) {
			t.Fatalf("IsID(%q) = true, want false", value)
		}
	}
	for _, value := range []string{"", "123", "123456789abz", "123456789abc0"} {
		if identity.IsShortID(value) {
			t.Fatalf("IsShortID(%q) = true, want false", value)
		}
	}
}

func TestLegacyPrefixedIDRemainsReadable(t *testing.T) {
	id := identity.NewID(identity.ResourceProject, "legacy")
	legacyID := identity.Prefix + id
	if !identity.IsID(legacyID) {
		t.Fatalf("IsID(%q) = false, want legacy compatibility", legacyID)
	}
	if got := identity.ShortID(legacyID); got != id[:12] {
		t.Fatalf("ShortID(%q) = %q, want %q", legacyID, got, id[:12])
	}
	if got, err := identity.Hash(legacyID); err != nil || got != id {
		t.Fatalf("Hash(%q) = %q, %v, want %q", legacyID, got, err, id)
	}
}
