package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func (f *runLogE2E) removeSandboxes(t *testing.T) {
	t.Helper()
	if f.sandboxes == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Each fixture has its own daemon and store. Include scheduler-created
	// sandboxes, whose identities need not appear on the parent run response.
	response, err := f.sandboxes.ListSandboxes(ctx, connect.NewRequest(&agentcomposev2.ListSandboxesRequest{Limit: 500}))
	if err != nil {
		t.Errorf("list fixture sandboxes for cleanup: %v", err)
		return
	}
	for _, sandbox := range response.Msg.GetSandboxes() {
		id := sandbox.GetSandboxId()
		if _, err := f.sandboxes.RemoveSandbox(ctx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: id, Force: true})); err != nil {
			t.Errorf("cleanup sandbox %s: %v", id, err)
		}
	}
}

func (f *runLogE2E) startSandboxHost(t *testing.T) {
	t.Helper()
	dataRoot := filepath.Join(f.root, "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// Own the host separately: it must survive hard daemon restarts, then stop
	// before the fixture removes its data root. Managed hosts intentionally
	// outlive their daemon and would otherwise leak between test scenarios.
	f.env["AGENT_COMPOSE_SANDBOX_HOST_MODE"] = "external"
	socket := f.env["AGENT_COMPOSE_SANDBOX_HOST_SOCKET"]
	host := startE2ESandboxHost(t, f.binary, f.repo, dataRoot, socket, f.env)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("sandbox-host log:\n%s", host.logs.String())
		}
	})
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !e2eSandboxHostSocketReachable(socket) {
		select {
		case <-ctx.Done():
			t.Fatalf("wait for sandbox-host: %v\n%s", ctx.Err(), host.logs.String())
		case <-ticker.C:
		}
	}
}
