package sandboxes

import (
	"testing"
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestApplySessionStartInfoPersistsTheRuntimeProcessModel(t *testing.T) {
	now := time.Now()
	vmState, _ := ApplySessionStartInfo(domain.VMState{}, domain.ProxyState{}, domain.SandboxVMInfo{
		SandboxInitState: driverpkg.SandboxInitStateEnabled,
	}, now)
	if vmState.SandboxInitState != driverpkg.SandboxInitStateEnabled {
		t.Fatalf("SandboxInitState = %q, want enabled", vmState.SandboxInitState)
	}
	vmState, _ = ApplySessionStartInfo(vmState, domain.ProxyState{}, domain.SandboxVMInfo{
		SandboxInitState: driverpkg.SandboxInitStateDisabled,
	}, now)
	if vmState.SandboxInitState != driverpkg.SandboxInitStateDisabled {
		t.Fatalf("SandboxInitState = %q, want the actual runtime state to replace the previous one", vmState.SandboxInitState)
	}
	// A driver that does not report the state must not erase a recorded one.
	vmState.SandboxInitState = driverpkg.SandboxInitStateEnabled
	vmState, _ = ApplySessionStartInfo(vmState, domain.ProxyState{}, domain.SandboxVMInfo{}, now)
	if vmState.SandboxInitState != driverpkg.SandboxInitStateEnabled {
		t.Fatalf("SandboxInitState = %q, want an unreported state to preserve the record", vmState.SandboxInitState)
	}
}
