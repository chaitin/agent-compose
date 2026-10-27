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
		{name: "id fallback when runtime ref empty", sandbox: &Sandbox{Summary: SandboxSummary{ID: "sandbox-1"}}, vmState: VMState{}, want: "agent-compose-sandbox-1"},
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

func TestBoxliteSandboxName(t *testing.T) {
	sandbox := &Sandbox{Summary: SandboxSummary{ID: "sandbox-1", RuntimeRef: "agent-compose-sandbox-1"}}
	tests := []struct {
		name    string
		sandbox *Sandbox
		vmState VMState
		want    string
	}{
		{name: "box name wins", sandbox: sandbox, vmState: VMState{BoxName: "box-name"}, want: "box-name"},
		{name: "runtime ref when name empty", sandbox: sandbox, vmState: VMState{}, want: "agent-compose-sandbox-1"},
		{name: "id fallback when ref empty", sandbox: &Sandbox{Summary: SandboxSummary{ID: "sandbox-1"}}, vmState: VMState{}, want: "agent-compose-sandbox-1"},
		{name: "stale box id is ignored for the name", sandbox: sandbox, vmState: VMState{BoxID: "stale-box-id", BoxName: "box-name"}, want: "box-name"},
		{name: "whitespace name is skipped", sandbox: sandbox, vmState: VMState{BoxName: "  "}, want: "agent-compose-sandbox-1"},
		{name: "empty without sandbox", sandbox: nil, vmState: VMState{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := boxliteSandboxName(tt.sandbox, tt.vmState); got != tt.want {
				t.Fatalf("boxliteSandboxName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBoxliteCreateNameMatchesLookupKeyBeforeBoxID guards the in-progress-create
// reuse invariant: before the base disk build finishes the BoxID is not yet
// persisted, so the only identifier a restarted daemon can probe is the name
// assigned at create. If the create name and the lookup key ever diverge here,
// NeedsCreate/getOrCreateBox would miss the in-progress box and issue a second,
// orphaned create. Once the BoxID is persisted it becomes the authoritative
// lookup key (the box remains addressable by name too), so the invariant only
// holds while the BoxID is empty.
func TestBoxliteCreateNameMatchesLookupKeyBeforeBoxID(t *testing.T) {
	sandbox := &Sandbox{Summary: SandboxSummary{ID: "sandbox-1", RuntimeRef: "agent-compose-sandbox-1"}}
	cases := []VMState{
		{},                                   // fresh create, RuntimeRef fallback
		{BoxName: "agent-compose-sandbox-1"}, // BoxName persisted at create
		{BoxName: "  "},                      // whitespace name falls through to RuntimeRef
		{BoxID: "   "},                       // whitespace BoxID is skipped like an empty one
	}
	noRuntimeRef := &Sandbox{Summary: SandboxSummary{ID: "sandbox-1"}}
	for _, vmState := range cases {
		for _, sb := range []*Sandbox{sandbox, noRuntimeRef} {
			lookup := boxliteLookupKey(sb, vmState)
			name := boxliteSandboxName(sb, vmState)
			if lookup == "" || name == "" {
				t.Fatalf("sandbox %+v vmState %+v: lookup key %q / create name %q must both be non-empty", sb.Summary, vmState, lookup, name)
			}
			if lookup != name {
				t.Fatalf("sandbox %+v vmState %+v: lookup key %q != create name %q; a restarted daemon would miss the in-progress box", sb.Summary, vmState, lookup, name)
			}
		}
	}
}
