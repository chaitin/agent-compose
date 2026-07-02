package agentcompose

import (
	"context"
	"errors"
	"testing"

	"agent-compose/pkg/agentcompose/capabilities"
	"agent-compose/pkg/agentcompose/domain"
	driverpkg "agent-compose/pkg/driver"
)

func TestResolveCapabilitySession(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSessionRPCBridge(t)
	// The capset set lives in session tags; only the token lives in env.
	session, err := bridge.store.CreateSession(ctx, "cap", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SessionTypeManual, nil,
		[]SessionEnvVar{{Name: capabilities.SessionTokenEnvName, Value: "session-token", Secret: true}},
		[]SessionTag{{Name: capabilities.CapsetTagName, Value: "dev"}, {Name: capabilities.CapsetTagName, Value: "data"}})
	if err != nil {
		t.Fatal(err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
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

	session.Summary.VMStatus = domain.VMStatusStopped
	if err := bridge.store.UpdateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.store.ResolveCapabilitySession(ctx, "session-token"); err == nil {
		t.Fatal("expected stopped session capability token to be rejected")
	}

	if _, err := bridge.store.ResolveCapabilitySession(ctx, "missing-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing capability token error = %v, want ErrNotFound", err)
	} else if err.Error() != "capability session token not found" {
		t.Fatalf("missing capability token message = %q", err.Error())
	}
}
