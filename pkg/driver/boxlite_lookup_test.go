package driver

import "testing"

func TestBoxliteLookupKey(t *testing.T) {
	sandbox := &Sandbox{Summary: SandboxSummary{ID: "sandbox-1", RuntimeRef: "agent-compose-sandbox-1"}}
	tests := []struct {
		name    string
		sandbox *Sandbox
		vmState VMState
		want    string
	}{
		{name: "box id wins", sandbox: sandbox, vmState: VMState{BoxID: "box-id", BoxName: "box-name"}, want: "box-id"},
		{name: "box name when id empty", sandbox: sandbox, vmState: VMState{BoxName: "box-name"}, want: "box-name"},
		{name: "runtime ref when id and name empty", sandbox: sandbox, vmState: VMState{}, want: "agent-compose-sandbox-1"},
		{name: "empty when no identifier", sandbox: nil, vmState: VMState{}, want: ""},
		{name: "whitespace id is skipped", sandbox: sandbox, vmState: VMState{BoxID: "  ", BoxName: "box-name"}, want: "box-name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := boxliteLookupKey(tt.sandbox, tt.vmState); got != tt.want {
				t.Fatalf("boxliteLookupKey() = %q, want %q", got, tt.want)
			}
		})
	}
}
