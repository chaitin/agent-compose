package credentials

// Material pairs a handle's metadata with the credential truth it stands for.
//
// The truth exists only inside a Snapshot. Nothing else in this package accepts
// a raw credential value, which keeps the value out of the model, the store,
// and every error message.
type Material struct {
	Handle Handle
	// Value is the credential truth. It must be handed straight to the sandbox
	// secret mechanism and never logged, persisted, or returned to a caller.
	Value string
}

// Snapshot is a fixed, already-resolved set of credential materials that an
// injection path may read.
//
// The injection path never re-resolves policy: it can only name a handle the
// snapshot already holds, and a snapshot exposes no mutation after
// construction. A snapshot is scoped to a single injection, not to a sandbox
// lifetime, so a revocation that happens between injections is observed when
// the next snapshot is built rather than being cached away.
type Snapshot struct {
	items map[string]Material
}

// NewSnapshot copies materials into an immutable lookup keyed by handle id.
// Later mutations of the input slice or the handles it contains do not affect
// the snapshot.
func NewSnapshot(materials []Material) Snapshot {
	snapshot := Snapshot{items: make(map[string]Material, len(materials))}
	for _, material := range materials {
		handle := material.Handle.Normalized()
		if handle.ID == "" {
			continue
		}
		snapshot.items[handle.ID] = Material{Handle: handle, Value: material.Value}
	}
	return snapshot
}

// Lookup returns the material for a handle id. The boolean is false when the
// snapshot does not hold the handle, which the injection path treats as a
// denial rather than as a reason to resolve policy itself.
func (s Snapshot) Lookup(handleID string) (Material, bool) {
	material, ok := s.items[handleID]
	return material, ok
}

// Len reports how many materials the snapshot holds.
func (s Snapshot) Len() int { return len(s.items) }
