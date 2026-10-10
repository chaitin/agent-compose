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
}
