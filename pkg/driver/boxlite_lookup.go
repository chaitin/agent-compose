package driver

import "strings"

// boxliteLookupKey returns the identifier the BoxLite runtime probes when
// resolving an existing box: BoxID first, then BoxName, then the sandbox's
// runtime ref, then the deterministic agent-compose-<id> fallback. The values
// name the same box at different lifecycle stages — BoxID is only persisted
// after the base disk build completes — so every probe must use this single
// precedence. The final fallback must match boxliteSandboxName, otherwise a box
// created when RuntimeRef is empty would be named but never addressable by
// lookup. Otherwise a daemon restarted mid-build would fail to find the
// in-progress box (NeedsCreate sees it by name, getOrCreateBox misses it by the
// still-empty BoxID) and issue a second create, producing a duplicate or orphan
// box. NeedsCreate and getOrCreateBox must stay on this same key.
func boxliteLookupKey(sandbox *Sandbox, vmState VMState) string {
	lookup := firstNonEmpty(strings.TrimSpace(vmState.BoxID), strings.TrimSpace(vmState.BoxName))
	if sandbox != nil {
		lookup = firstNonEmpty(lookup, strings.TrimSpace(sandbox.Summary.RuntimeRef), "agent-compose-"+sandbox.Summary.ID)
	}
	return lookup
}

// boxliteSandboxName returns the stable name assigned to a new box via
// boxlite_options_set_name. It is the value boxliteLookupKey resolves to before
// the BoxID is persisted (BoxName is RuntimeRef at create time), so a daemon
// restarted mid-create can find the in-progress box by name once the host
// finishes building it. It deliberately excludes BoxID: that identifier is
// BoxLite-assigned and only exists after the base disk completes, so naming a
// box with it would either be empty or reuse a stale ID after a recreate.
func boxliteSandboxName(sandbox *Sandbox, vmState VMState) string {
	if sandbox == nil {
		return firstNonEmpty(strings.TrimSpace(vmState.BoxName), strings.TrimSpace(vmState.BoxID))
	}
	return firstNonEmpty(strings.TrimSpace(vmState.BoxName), strings.TrimSpace(sandbox.Summary.RuntimeRef), "agent-compose-"+sandbox.Summary.ID)
}
