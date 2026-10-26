package driver

import "strings"

// boxliteLookupKey returns the identifier the BoxLite runtime probes when
// resolving an existing box: BoxID first, then BoxName, then the sandbox's
// runtime ref. The three values name the same box at different lifecycle
// stages — BoxID is only persisted after the base disk build completes — so
// every probe must use this single precedence. Otherwise a daemon restarted
// mid-build would fail to find the in-progress box (NeedsCreate sees it by
// name, getOrCreateBox misses it by the still-empty BoxID) and issue a second
// create, producing a duplicate or orphan box. NeedsCreate and getOrCreateBox
// must stay on this same key.
func boxliteLookupKey(sandbox *Sandbox, vmState VMState) string {
	lookup := firstNonEmpty(strings.TrimSpace(vmState.BoxID), strings.TrimSpace(vmState.BoxName))
	if sandbox != nil {
		lookup = firstNonEmpty(lookup, strings.TrimSpace(sandbox.Summary.RuntimeRef))
	}
	return lookup
}
