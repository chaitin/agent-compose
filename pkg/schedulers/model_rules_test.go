package schedulers_test

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

// The single definition of the identity rule every entry point applies: an
// execution carrying trusted ingress headers never reuses a shared sandbox,
// whatever policy it asked for; one without identity keeps its policy.
func TestSandboxPolicyForIdentityOverridesSharedPoliciesForIdentifiedCallers(t *testing.T) {
	identified := domain.NewContextWithTrustedHeaders(context.Background(), []domain.TrustedHeader{{Name: "x-mpi-username", Value: "bob@example.com"}})
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		policy string
		want   string
	}{
		{name: "unattended keeps sticky", ctx: context.Background(), policy: domain.SchedulerSandboxPolicySticky, want: domain.SchedulerSandboxPolicySticky},
		{name: "unattended keeps an unset policy", ctx: context.Background(), policy: "", want: ""},
		{name: "identified caller gets a new sandbox", ctx: identified, policy: domain.SchedulerSandboxPolicySticky, want: domain.SchedulerSandboxPolicyNew},
		{name: "identified caller keeps an explicit new", ctx: identified, policy: domain.SchedulerSandboxPolicyNew, want: domain.SchedulerSandboxPolicyNew},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := schedulers.SandboxPolicyForIdentity(tc.ctx, tc.policy); got != tc.want {
				t.Fatalf("policy = %q, want %q", got, tc.want)
			}
		})
	}
}
