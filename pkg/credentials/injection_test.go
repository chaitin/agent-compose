package credentials_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

func testSnapshot(handle credentials.Handle, value string) credentials.Snapshot {
	return credentials.NewSnapshot([]credentials.Material{{Handle: handle, Value: value}})
}

func TestPlanInjectionProducesHostScopedTLSRequiredSpec(t *testing.T) {
	handle := baseHandle()
	specs, err := credentials.PlanInjection(testSnapshot(handle, "secret-truth"), []credentials.InjectionRequest{
		{HandleID: handle.ID, Request: credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}}},
	}, testNow)
	if err != nil {
		t.Fatalf("PlanInjection() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("PlanInjection() specs = %#v, want exactly one", specs)
	}
	spec := specs[0]
	if spec.Name != "GIT_TOKEN" || spec.Value != "secret-truth" {
		t.Fatalf("spec = %#v, want the declared env name and the credential truth", spec)
	}
	if len(spec.AllowHosts) != 1 || spec.AllowHosts[0] != "git.example.com" {
		t.Fatalf("AllowHosts = %#v, want only the scoped endpoint", spec.AllowHosts)
	}
	if !spec.RequireTLS {
		t.Fatal("RequireTLS must be true: substitution without a verified peer leaks the credential")
	}
	if !strings.HasPrefix(spec.Placeholder, credentials.PlaceholderPrefix) || !strings.HasSuffix(spec.Placeholder, handle.TokenFingerprint) {
		t.Fatalf("Placeholder = %q, want the fingerprint-derived placeholder", spec.Placeholder)
	}
	if spec.Placeholder == spec.Value {
		t.Fatal("the placeholder must never equal the credential truth")
	}
}

// TestPlanInjectionIsAllOrNothing pins the "verify every authorization before
// writing any header or environment variable" constraint: one denied request
// yields no specs at all, so a caller cannot half-apply a set of credentials.
func TestPlanInjectionIsAllOrNothing(t *testing.T) {
	handle := baseHandle()
	specs, err := credentials.PlanInjection(testSnapshot(handle, "secret-truth"), []credentials.InjectionRequest{
		{HandleID: handle.ID, Request: credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}}},
		{HandleID: handle.ID, Request: credentials.Request{Endpoint: "registry.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}}},
	}, testNow)
	if !errors.Is(err, credentials.ErrEndpointOutOfScope) {
		t.Fatalf("PlanInjection() error = %v, want ErrEndpointOutOfScope", err)
	}
	if specs != nil {
		t.Fatalf("PlanInjection() specs = %#v, want none after a denial", specs)
	}
}

func TestPlanInjectionDeniesHandleMissingFromSnapshot(t *testing.T) {
	handle := baseHandle()
	specs, err := credentials.PlanInjection(testSnapshot(handle, "secret-truth"), []credentials.InjectionRequest{
		{HandleID: "cred_absent", Request: credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}}},
	}, testNow)
	if !errors.Is(err, credentials.ErrHandleNotInSnapshot) {
		t.Fatalf("PlanInjection() error = %v, want ErrHandleNotInSnapshot", err)
	}
	if specs != nil {
		t.Fatalf("PlanInjection() specs = %#v, want none", specs)
	}
}

func TestPlanInjectionObservesRevocationFromTheSnapshot(t *testing.T) {
	handle := baseHandle()
	handle.RevokedAt = testNow.Add(-1)
	if _, err := credentials.PlanInjection(testSnapshot(handle, "secret-truth"), []credentials.InjectionRequest{
		{HandleID: handle.ID, Request: credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}}},
	}, testNow); !errors.Is(err, credentials.ErrRevoked) {
		t.Fatalf("PlanInjection() revoked handle error = %v, want ErrRevoked", err)
	}
}

func TestPlanInjectionBatchesSharedHandleByOwnerIntersection(t *testing.T) {
	handle := baseHandle()
	requests := []credentials.InjectionRequest{
		{HandleID: handle.ID, Request: credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}, {Kind: "run", ID: "run-1"}}}},
		{HandleID: handle.ID, Request: credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "run", ID: "run-1"}}}},
	}
	if _, err := credentials.PlanInjection(testSnapshot(handle, "secret-truth"), requests, testNow); err != nil {
		t.Fatalf("PlanInjection() shared handle error = %v, want the intersected owner to authorize it", err)
	}

	disjoint := []credentials.InjectionRequest{
		{HandleID: handle.ID, Request: credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}}},
		{HandleID: handle.ID, Request: credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "run", ID: "run-1"}}}},
	}
	specs, err := credentials.PlanInjection(testSnapshot(handle, "secret-truth"), disjoint, testNow)
	if !errors.Is(err, credentials.ErrUnattributable) {
		t.Fatalf("PlanInjection() disjoint batch error = %v, want ErrUnattributable", err)
	}
	if specs != nil {
		t.Fatalf("PlanInjection() disjoint batch specs = %#v, want none", specs)
	}
}

// TestSnapshotIsFixedAfterConstruction pins the "read credentials only from a
// fixed snapshot" constraint: mutating the caller's slice or the material after
// construction cannot change what the injection path sees.
func TestSnapshotIsFixedAfterConstruction(t *testing.T) {
	handle := baseHandle()
	materials := []credentials.Material{{Handle: handle, Value: "secret-truth"}}
	snapshot := credentials.NewSnapshot(materials)

	materials[0].Value = "tampered"
	materials[0].Handle.Scope.Endpoint = "evil.example.com"
	materials[0].Handle.Scope.Owners[0].ID = "tampered-owner"
	materials[0].Handle.Scope.Owners = nil
	materials = append(materials, credentials.Material{Handle: credentials.Handle{ID: "cred_injected"}, Value: "injected"})
	if len(materials) != 2 {
		t.Fatalf("source slice length = %d, want 2 after the append", len(materials))
	}

	stored, ok := snapshot.Lookup(handle.ID)
	if !ok {
		t.Fatal("snapshot must still hold the handle it was built with")
	}
	if stored.Value != "secret-truth" {
		t.Fatalf("snapshot value = %q, want the value captured at construction", stored.Value)
	}
	if stored.Handle.Scope.Endpoint != "git.example.com" {
		t.Fatalf("snapshot endpoint = %q, want the endpoint captured at construction", stored.Handle.Scope.Endpoint)
	}
	if _, ok := snapshot.Lookup("cred_injected"); ok {
		t.Fatal("appending to the source slice must not add a handle to the snapshot")
	}
	if snapshot.Len() != 1 {
		t.Fatalf("snapshot length = %d, want 1", snapshot.Len())
	}
}

// TestPlanInjectionRefusesTwoHandlesClaimingOneGuestVariable pins the planning
// boundary: the guest holds one value per environment variable, so a second
// handle for the same name would silently shadow the first. The plan must fail
// before it reports success rather than leaving the collision to a driver.
func TestPlanInjectionRefusesTwoHandlesClaimingOneGuestVariable(t *testing.T) {
	first := baseHandle()
	second := baseHandle()
	second.ID = "cred_other"
	second.TokenHash = "hash-other"
	second.TokenFingerprint = "fingerprint-other"
	snapshot := credentials.NewSnapshot([]credentials.Material{
		{Handle: first, Value: "first-truth"},
		{Handle: second, Value: "second-truth"},
	})
	request := func(handleID string) credentials.InjectionRequest {
		return credentials.InjectionRequest{
			HandleID: handleID,
			Request:  credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}},
		}
	}
	specs, err := credentials.PlanInjection(snapshot, []credentials.InjectionRequest{request(first.ID), request(second.ID)}, testNow)
	if !errors.Is(err, credentials.ErrInvalidSecretSpec) {
		t.Fatalf("PlanInjection() duplicate variable error = %v, want ErrInvalidSecretSpec", err)
	}
	if specs != nil {
		t.Fatalf("PlanInjection() specs = %#v, want none", specs)
	}
}

// TestPlanInjectionRefusesAHandleWithoutFingerprint pins the placeholder
// invariant: the placeholder is derived from the fingerprint, so a handle
// without one would plan the same bare prefix for every such handle.
func TestPlanInjectionRefusesAHandleWithoutFingerprint(t *testing.T) {
	handle := baseHandle()
	handle.TokenFingerprint = ""
	specs, err := credentials.PlanInjection(testSnapshot(handle, "secret-truth"), []credentials.InjectionRequest{{
		HandleID: handle.ID,
		Request:  credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}},
	}}, testNow)
	if !errors.Is(err, credentials.ErrInvalidHandle) {
		t.Fatalf("PlanInjection() missing-fingerprint error = %v, want ErrInvalidHandle", err)
	}
	if specs != nil {
		t.Fatalf("PlanInjection() specs = %#v, want none", specs)
	}
}
