package egress

import "time"

// PolicyRevision is the persisted account of one applied egress policy for a
// scope (today, one sandbox). Revision is monotonic within that scope: it starts
// at one for the first observed policy and advances only when PolicyHash of the
// effective policy changes. Re-observing an unchanged policy keeps both the
// revision number and AppliedAt, so a reader can tell an idempotent read from a
// real policy change.
//
// A revision is acknowledged when the enforcement point that decides the
// sandbox's traffic has read exactly this revision. For a mediated sandbox that
// is the daemon's forward proxy reading the declaration per request; for a
// driver-applied policy it is the runtime creation that installed that policy.
// An inspect that only reports the active revision does not acknowledge it, so a
// declaration changed after a driver-applied runtime was created stays
// unacknowledged until the runtime is recreated.
type PolicyRevision struct {
	Revision  uint64    `json:"revision"`
	Hash      string    `json:"hash"`
	AppliedAt time.Time `json:"applied_at"`
	AckAt     time.Time `json:"acknowledged_at,omitempty"`
}

// Recorded reports whether a revision store produced a real record. The zero
// value is what a scope that never had an observed policy (or a store that was
// not configured) returns.
func (r PolicyRevision) Recorded() bool {
	return r.Revision > 0 && r.Hash != ""
}

// Acknowledged reports whether an enforcement point has read this revision.
func (r PolicyRevision) Acknowledged() bool {
	return !r.AckAt.IsZero()
}
