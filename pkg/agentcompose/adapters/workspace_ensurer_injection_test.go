package adapters

import (
	"context"
	"testing"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/workspaces"
)

type constructorWorkspaceEnsurer struct{}

var _ workspaces.WorkspaceEnsurer = (*constructorWorkspaceEnsurer)(nil)

func (*constructorWorkspaceEnsurer) Ensure(context.Context, *domain.Sandbox) error {
	return nil
}

func TestWorkspaceEnsurerConstructorDependencies(t *testing.T) {
	t.Parallel()

	ensurer := &constructorWorkspaceEnsurer{}
	bridge := NewSandboxRPCBridge(nil, nil, nil, ensurer, nil, nil, nil, nil, nil, nil, nil, nil)
	runner := NewLoaderSandboxRunner(nil, nil, nil, ensurer, nil, nil, nil, nil, nil, nil, nil)

	if bridge.workspaceEnsurer != ensurer {
		t.Fatalf("SandboxRPCBridge workspace ensurer = %p, want %p", bridge.workspaceEnsurer, ensurer)
	}
	if got := bridge.sessionLifecycle().WorkspaceEnsurer; got != ensurer {
		t.Fatalf("Lifecycle workspace ensurer = %p, want %p", got, ensurer)
	}
	if runner.workspaceEnsurer != ensurer {
		t.Fatalf("LoaderSandboxRunner workspace ensurer = %p, want %p", runner.workspaceEnsurer, ensurer)
	}
}
