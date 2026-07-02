package agentcompose

import (
	driverpkg "agent-compose/internal/driver"
	"context"
	"testing"
)

func TestResolveCapabilitySession(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSessionRPCBridge(t)
	// The capset set lives in session tags; only the token lives in env.
	session, err := bridge.store.CreateSession(ctx, "cap", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", SessionTypeManual, nil,
		[]SessionEnvVar{{Name: capabilitySessionTokenEnvName, Value: "session-token", Secret: true}},
		[]SessionTag{{Name: capabilityCapsetTagName, Value: "dev"}, {Name: capabilityCapsetTagName, Value: "data"}})
	if err != nil {
		t.Fatal(err)
	}
	session.Summary.VMStatus = VMStatusRunning
	if err := bridge.store.UpdateSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	binding, err := bridge.store.ResolveCapabilitySession(ctx, "session-token")
	if err != nil {
		t.Fatal(err)
	}
	if binding.SessionID != session.Summary.ID {
		t.Fatalf("unexpected session id %q", binding.SessionID)
	}
	if len(binding.CapsetIDs) != 2 || binding.CapsetIDs[0] != "dev" || binding.CapsetIDs[1] != "data" {
		t.Fatalf("unexpected capset set %+v", binding.CapsetIDs)
	}

	session.Summary.VMStatus = VMStatusStopped
	if err := bridge.store.UpdateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.store.ResolveCapabilitySession(ctx, "session-token"); err == nil {
		t.Fatal("expected stopped session capability token to be rejected")
	}
}
