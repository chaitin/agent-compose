package capproxy

import (
	"context"
	"strings"

	"github.com/chaitin/agent-compose/pkg/egress"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// resolveCallCapset picks the capset for this call: the guest-supplied
// x-octobus-capset if it is in the allowed set, or the sole allowed capset when
// the guest omits it. Otherwise it is an error (the guest must disambiguate).
//
// The reachability decision is evaluated by the shared egress entry point
// against capsetEgressPolicy, so the capability gateway and the LLM facade
// answer "may this sandbox reach this upstream" through one model.
func resolveCallCapset(ctx context.Context, allowed []string) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	requested := firstMetadata(md, "x-octobus-capset")
	if requested == "" && len(allowed) != 1 {
		return "", status.Error(codes.FailedPrecondition, "x-octobus-capset is required: sandbox allows multiple capsets")
	}
	// A guest that names no capset selects the sandbox's sole grant. Only the
	// request name is normalized here; the policy is still what decides.
	name := requested
	if name == "" {
		name = allowed[0]
	}
	binding := SandboxBinding{CapsetIDs: allowed}
	policy := capsetEgressPolicy(binding)
	request := capsetEgressRequest(binding, name)
	record := egress.NewRecord(request, egress.Decide(policy, request))
	// Re-validate the policy generation before use: a decision made against an
	// older policy must not be honored. The binding is request-scoped today, so
	// the generations match; this guard is the landing point for the phase-3
	// policy store (SEC-5).
	if record.IsStale(policy) || !record.Allowed() {
		return "", status.Errorf(codes.PermissionDenied, "capset %q is not allowed for this sandbox", requested)
	}
	return record.Result.Target, nil
}

// capsetEgressPolicy returns the policy a sandbox binding grants over the
// capability gateway: one allow rule per capset the sandbox may reach. Every
// other capset falls through to the deny default.
func capsetEgressPolicy(binding SandboxBinding) egress.Policy {
	rules := make([]egress.Rule, 0, len(binding.CapsetIDs))
	for _, id := range binding.CapsetIDs {
		rules = append(rules, egress.Rule{ID: "capability.capset", Names: []string{id}, Action: egress.Allow})
	}
	return egress.NewPolicy(egress.Deny, rules...)
}

// capsetEgressRequest describes one gateway call as an egress request.
func capsetEgressRequest(binding SandboxBinding, requested string) egress.Request {
	return egress.Request{
		Consumer: binding.SandboxID,
		Kind:     egress.KindCapabilityCapset,
		Name:     strings.TrimSpace(requested),
	}
}
