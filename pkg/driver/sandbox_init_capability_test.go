package driver

import (
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

func TestSandboxInitCapabilityForEveryDriver(t *testing.T) {
	for _, test := range []struct {
		driver    string
		supported bool
		mode      SandboxInitMode
		control   SandboxInitControl
	}{
		{driver: RuntimeDriverDocker, supported: true, mode: SandboxInitModePID1, control: SandboxInitControlUnixSocket},
		{driver: RuntimeDriverK8s, supported: true, mode: SandboxInitModePID1, control: SandboxInitControlGuestExec},
		{driver: RuntimeDriverMicrosandbox, supported: true, mode: SandboxInitModePID1, control: SandboxInitControlGuestExec},
		{driver: RuntimeDriverBoxlite, supported: true, mode: SandboxInitModePID1, control: SandboxInitControlUnixSocket},
		{driver: "unknown", supported: false, mode: SandboxInitModeUnsupported, control: SandboxInitControlNone},
	} {
		t.Run(test.driver, func(t *testing.T) {
			capability := SandboxInitCapabilityFor(test.driver)
			if capability.Supported != test.supported || capability.Mode != test.mode || capability.Control != test.control {
				t.Fatalf("capability = %#v, want supported=%v mode=%s control=%s", capability, test.supported, test.mode, test.control)
			}
			if !capability.Supported && len(capability.Limitations) == 0 {
				t.Fatalf("unsupported capability %#v does not explain why", capability)
			}
		})
	}
}

func TestSandboxInitStateForPrefersThePersistedRuntimeState(t *testing.T) {
	config := &appconfig.Config{SandboxInitEnabled: true}
	if got := SandboxInitStateFor(config, VMState{SandboxInitState: SandboxInitStateDisabled}); got != SandboxInitStateDisabled {
		t.Fatalf("state = %q, want the persisted disabled state to win over the flag", got)
	}
	config.SandboxInitEnabled = false
	if got := SandboxInitStateFor(config, VMState{SandboxInitState: SandboxInitStateEnabled}); got != SandboxInitStateEnabled {
		t.Fatalf("state = %q, want the persisted enabled state to win over the flag", got)
	}
	if got := SandboxInitStateFor(&appconfig.Config{SandboxInitEnabled: true}, VMState{}); got != SandboxInitStateEnabled {
		t.Fatalf("state = %q, want the flag fallback for an unrecorded runtime", got)
	}
	if got := SandboxInitStateFor(&appconfig.Config{}, VMState{}); got != SandboxInitStateDisabled {
		t.Fatalf("state = %q, want disabled when the flag is off", got)
	}
}
