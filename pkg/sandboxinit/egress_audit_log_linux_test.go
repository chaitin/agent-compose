//go:build linux

package sandboxinit

import "testing"

// TestEgressSupervisorStampsTheLaunchGeneration pins the wiring between the
// daemon-injected generation and the audit event a sink receives: a caller that
// does not attribute an event itself still gets the generation the runtime was
// created under.
func TestEgressSupervisorStampsTheLaunchGeneration(t *testing.T) {
	var observed EgressAuditEvent
	supervisor, err := newEgressSupervisor(EgressModeAudit, "sandbox-1", func(event EgressAuditEvent) {
		observed = event
	}, 17)
	if err != nil {
		t.Fatalf("newEgressSupervisor() error = %v", err)
	}
	supervisor.record(EgressAuditEvent{SandboxID: "sandbox-1", Host: "example.com", Port: 443})
	if observed.Generation != 17 {
		t.Fatalf("audit event generation = %d, want the launch generation 17", observed.Generation)
	}
	if observed.Host != "example.com" || observed.Port != 443 {
		t.Fatalf("audit event = %+v, want the observed destination", observed)
	}
}
