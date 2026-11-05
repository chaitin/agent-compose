package egress

// ResourceKind names a family of upstream resources reachable through one
// daemon-mediated path. A policy is scoped to a single kind: the capability
// gateway decides which capsets a sandbox may reach, and the runtime LLM facade
// decides which upstream model a facade token may reach.
type ResourceKind string

const (
	// KindLLMModel is an upstream LLM model reachable through the runtime LLM
	// facade.
	KindLLMModel ResourceKind = "llm-model"
	// KindCapabilityCapset is an OctoBus capset reachable through the
	// capability gateway.
	KindCapabilityCapset ResourceKind = "capability-capset"
)

// Request describes one attempt to reach one upstream resource.
type Request struct {
	// Consumer identifies who is asking. For this daemon it is the sandbox ID;
	// it is carried into the decision record and does not change the outcome.
	Consumer string
	Kind     ResourceKind
	// Name is the resource the consumer asked for, after the caller has
	// trimmed it. An empty Name means the consumer named no resource, which a
	// policy may still resolve (for example to a sole grant).
	Name string
	// ResolvedIPs are the addresses a network-endpoint name resolved to, in the
	// order the caller will try them. They are read only by a rule that
	// constrains its allowed addresses; a caller that has not resolved the name
	// leaves this empty, which such a rule treats as unsatisfied rather than as
	// unconstrained.
	ResolvedIPs []string
	// SessionID, Marker, and ProcessID identify the sandbox-init exec session
	// that produced a mediated connection. They are advisory attribution data
	// from the init process: the policy outcome never depends on them, and a
	// path that has no such identity leaves them empty.
	SessionID string
	Marker    string
	ProcessID int
	// Binary is the absolute executable path of the process that produced a
	// mediated connection, as reported by the sandbox init process. It scopes a
	// rule that declares binaries; a path that cannot be established leaves it
	// empty, which no binary-scoped allowance can satisfy and every
	// binary-scoped denial refuses.
	Binary string
}
