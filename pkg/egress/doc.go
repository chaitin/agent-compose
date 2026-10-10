// Package egress owns the single decision entry point every daemon-mediated
// egress path uses: given a policy snapshot and a request, Decide reports
// whether the consumer may reach the upstream resource and which canonical
// resource the request resolves to.
//
// The package is deliberately pure. Decide reads no clock, performs no I/O, and
// holds no package state, so the same model can serve the runtime decision
// (the capability gateway and the LLM facade today, a Rego-evaluated policy
// later) and the config-time analysis of that policy (a Z3 prover comparing two
// policy snapshots). Keeping one model is what stops the two consumers from
// drifting apart.
//
// Every decision records the Generation of the policy it was evaluated
// against. A consumer that re-reads a newer policy before acting can recognize
// an old decision as stale instead of silently honoring it.
package egress
