//go:build linux && cgo && boxlitecgo

package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// TestBoxliteEnsureSandboxRefusesDeclaredDefaultDeny pins that the BoxLite
// driver invokes the fail-closed gate before it creates any box. The driver
// reports no egress strength, so a declared default-deny policy must be refused
// with the machine-consumable reason rather than starting a sandbox with
// unrestricted egress - the exact gap that let a deny declaration silently
// start an open sandbox on BoxLite.
func TestBoxliteEnsureSandboxRefusesDeclaredDefaultDeny(t *testing.T) {
	runtime := &cgoSandboxRuntime{}
	sandbox := &Sandbox{
		Summary:       SandboxSummary{ID: "sandbox-boxlite-deny"},
		NetworkPolicy: &SandboxNetworkPolicy{Default: egress.Deny},
	}

	_, err := runtime.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{})
	if !errors.Is(err, ErrSandboxNetworkEnforcementUnavailable) {
		t.Fatalf("EnsureSandbox() error = %v, want ErrSandboxNetworkEnforcementUnavailable", err)
	}
}
