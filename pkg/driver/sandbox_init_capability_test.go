package driver

import "testing"

func TestSandboxInitCapabilityForEveryDriver(t *testing.T) {
	for _, test := range []struct {
		driver    string
		supported bool
		mode      SandboxInitMode
		control   SandboxInitControl
	}{
		{driver: RuntimeDriverDocker, supported: true, mode: SandboxInitModePID1, control: SandboxInitControlUnixSocket},
		{driver: RuntimeDriverK8s, supported: true, mode: SandboxInitModePID1, control: SandboxInitControlGuestExec},
		{driver: RuntimeDriverMicrosandbox, supported: true, mode: SandboxInitModePID1, control: SandboxInitControlUnixSocket},
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
