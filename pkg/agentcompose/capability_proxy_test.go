package agentcompose

import (
	"context"
	"testing"

	"agent-compose/pkg/capabilities"
	driverpkg "agent-compose/pkg/driver"
	modelpkg "agent-compose/pkg/model"
)

func TestResolveCapabilitySession(t *testing.T) {
	ctx := context.Background()
	_, _, store := newTestSessionRPCBridgeWithStore(t)
	// The capset set lives in session tags; only the token lives in env.
	session, err := store.CreateSession(ctx, "cap", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", modelpkg.SessionTypeManual, nil,
		[]modelpkg.SessionEnvVar{{Name: capabilities.CapabilitySessionTokenEnvName, Value: "session-token", Secret: true}},
		[]modelpkg.SessionTag{{Name: capabilities.CapabilityCapsetTagName, Value: "dev"}, {Name: capabilities.CapabilityCapsetTagName, Value: "data"}})
	if err != nil {
		t.Fatal(err)
	}
	session.Summary.VMStatus = modelpkg.VMStatusRunning
	if err := store.UpdateSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	binding, err := store.ResolveCapabilitySession(ctx, "session-token")
	if err != nil {
		t.Fatal(err)
	}
	if binding.SessionID != session.Summary.ID {
		t.Fatalf("unexpected session id %q", binding.SessionID)
	}
	if len(binding.CapsetIDs) != 2 || binding.CapsetIDs[0] != "dev" || binding.CapsetIDs[1] != "data" {
		t.Fatalf("unexpected capset set %+v", binding.CapsetIDs)
	}

	session.Summary.VMStatus = modelpkg.VMStatusStopped
	if err := store.UpdateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveCapabilitySession(ctx, "session-token"); err == nil {
		t.Fatal("expected stopped session capability token to be rejected")
	}
}
