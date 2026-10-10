package capproxy

import (
	"context"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestDecideCallCapsetContract locks the reachability decision the capability
// gateway makes for one call: which capset a sandbox is allowed to reach, which
// capset the call resolves to, the sandbox the decision record names, and the
// exact gRPC code and message every denial carries. It is the pre-refactor
// contract for the SEC-4 egress entry point, so it asserts outcomes only (not
// how the decision is computed) and must keep passing unchanged after the
// decision is routed through pkg/egress.
func TestDecideCallCapsetContract(t *testing.T) {
	const sandboxID = "sandbox-1"
	tests := []struct {
		name       string
		metadata   metadata.MD
		allowed    []string
		wantCapset string
		wantCode   codes.Code
		wantIn     string
	}{
		{
			name:       "requested capset is one of the grants",
			metadata:   metadata.Pairs("x-octobus-capset", "dev"),
			allowed:    []string{"dev", "staging"},
			wantCapset: "dev",
		},
		{
			name:     "requested capset is outside the grants",
			metadata: metadata.Pairs("x-octobus-capset", "other"),
			allowed:  []string{"dev"},
			wantCode: codes.PermissionDenied,
			wantIn:   `capset "other" is not allowed for this sandbox`,
		},
		{
			name:       "requested capset is trimmed before matching",
			metadata:   metadata.Pairs("x-octobus-capset", "  dev  "),
			allowed:    []string{"dev"},
			wantCapset: "dev",
		},
		{
			name:       "first requested value wins",
			metadata:   metadata.MD{"x-octobus-capset": []string{"dev", "staging"}},
			allowed:    []string{"dev", "staging"},
			wantCapset: "dev",
		},
		{
			name:       "omitted capset selects the sole grant",
			metadata:   metadata.MD{},
			allowed:    []string{"only"},
			wantCapset: "only",
		},
		{
			name:     "omitted capset with several grants is ambiguous",
			metadata: metadata.MD{},
			allowed:  []string{"one", "two"},
			wantCode: codes.FailedPrecondition,
			wantIn:   "x-octobus-capset is required",
		},
		{
			name:     "omitted capset with no grants is ambiguous",
			metadata: metadata.MD{},
			allowed:  nil,
			wantCode: codes.FailedPrecondition,
			wantIn:   "x-octobus-capset is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binding := SandboxBinding{SandboxID: sandboxID, CapsetIDs: tc.allowed}
			ctx := metadata.NewIncomingContext(context.Background(), tc.metadata)
			record, err := decideCallCapset(ctx, binding)
			if tc.wantCode != codes.OK {
				if status.Code(err) != tc.wantCode {
					t.Fatalf("code = %s, want %s (err=%v)", status.Code(err), tc.wantCode, err)
				}
				if tc.wantIn != "" && !strings.Contains(status.Convert(err).Message(), tc.wantIn) {
					t.Fatalf("message = %q, want it to contain %q", status.Convert(err).Message(), tc.wantIn)
				}
				if record.Result.Target != "" {
					t.Fatalf("capset = %q, want empty on denial", record.Result.Target)
				}
				// A refusal that reached the decision still records who asked, so
				// the sandbox is named in the audit trail.
				if tc.wantCode == codes.PermissionDenied && record.Request.Consumer != sandboxID {
					t.Fatalf("consumer = %q, want %q", record.Request.Consumer, sandboxID)
				}
				return
			}
			if err != nil {
				t.Fatalf("decideCallCapset returned error: %v", err)
			}
			if record.Result.Target != tc.wantCapset {
				t.Fatalf("capset = %q, want %q", record.Result.Target, tc.wantCapset)
			}
			if record.Request.Consumer != sandboxID {
				t.Fatalf("consumer = %q, want the bound sandbox %q", record.Request.Consumer, sandboxID)
			}
			if record.Request.Kind != egress.KindCapabilityCapset {
				t.Fatalf("kind = %q, want %q", record.Request.Kind, egress.KindCapabilityCapset)
			}
		})
	}
}

// TestCapsetEgressPolicyMirrorsBinding pins how a sandbox binding becomes an
// egress policy: every granted capset is allowed and resolves to itself, an
// ungranted capset falls through to the deny default, and a changed binding
// produces a different generation so an earlier decision is recognized as
// stale.
func TestCapsetEgressPolicyMirrorsBinding(t *testing.T) {
	binding := SandboxBinding{SandboxID: "sandbox-1", CapsetIDs: []string{"dev", "staging"}}
	policy := capsetEgressPolicy(binding)
	if rebuilt := capsetEgressPolicy(binding); rebuilt.Generation() != policy.Generation() {
		t.Fatalf("rebuilding the same binding changed the generation: %d vs %d", rebuilt.Generation(), policy.Generation())
	}
	for _, granted := range binding.CapsetIDs {
		request := capsetEgressRequest(binding, granted)
		result := egress.Decide(policy, request)
		if !result.Allowed() || result.Target != granted {
			t.Fatalf("granted capset %q decided as %+v, want allow resolved to itself", granted, result)
		}
		if request.Kind != egress.KindCapabilityCapset || request.Consumer != binding.SandboxID {
			t.Fatalf("request = %+v, want kind %q and consumer %q", request, egress.KindCapabilityCapset, binding.SandboxID)
		}
	}

	denied := capsetEgressRequest(binding, "other")
	result := egress.Decide(policy, denied)
	if result.Allowed() {
		t.Fatalf("ungranted capset decided as %+v, want deny", result)
	}
	if result.RuleID != "" {
		t.Fatalf("deny result named rule %q, want the policy default", result.RuleID)
	}
	if result.IsStale(policy) {
		t.Fatal("decision is stale against the policy it was evaluated against")
	}

	narrowed := capsetEgressPolicy(SandboxBinding{SandboxID: "sandbox-1", CapsetIDs: []string{"dev"}})
	if !result.IsStale(narrowed) {
		t.Fatal("decision was not recognized as stale after the binding narrowed")
	}
}
