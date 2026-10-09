package driver

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// planAllows reports whether a rendered plan has an allow rule for the given
// destination on the given port/protocol. It is a test helper, not a production
// decision engine: enforcement belongs to the Microsandbox runtime, and the
// engine's own allow/deny decision model lives in pkg/egress.
func planAllows(plan microsandboxNetworkPlan, destination, port, protocol string) bool {
	if plan.DefaultEgress != microsandboxPlanDefaultEgressDeny {
		return plan.DefaultEgress == microsandboxPlanDefaultEgressAllow
	}
	for _, rule := range plan.Rules {
		if rule.Action == "allow" && rule.Destination == destination && rule.Port == port && rule.Protocol == protocol {
			return true
		}
	}
	return false
}

func TestPlanMicrosandboxNetwork(t *testing.T) {
	engineEndpoint := mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS)
	declaredAllow := mustEndpoint(t, "api.example.com", 443, egress.ProtocolHTTPS)

	tests := []struct {
		name    string
		policy  *SandboxNetworkPolicy
		want    microsandboxNetworkPlan
		wantErr bool
	}{
		{
			name:   "undeclared policy keeps egress open",
			policy: nil,
			want: microsandboxNetworkPlan{
				DefaultEgress:    microsandboxPlanDefaultEgressAllow,
				RebindProtection: true,
			},
		},
		{
			name: "declared allow renders no enforceful rules",
			policy: &SandboxNetworkPolicy{
				Default: egress.Allow,
				Allow:   []egress.Endpoint{declaredAllow},
			},
			want: microsandboxNetworkPlan{
				DefaultEgress:    microsandboxPlanDefaultEgressAllow,
				RebindProtection: true,
			},
		},
		{
			name: "deny renders engine endpoints first then declared entries",
			policy: &SandboxNetworkPolicy{
				Default:         egress.Deny,
				Allow:           []egress.Endpoint{declaredAllow},
				EngineEndpoints: []egress.EngineEndpoint{engineEndpoint},
				DenyDomains:     []string{"ads.example.com", "evil.example.com"},
			},
			want: microsandboxNetworkPlan{
				DefaultEgress: microsandboxPlanDefaultEgressDeny,
				Rules: []microsandboxNetworkRule{
					{Action: "allow", Destination: "facade.internal", Port: "7410", Protocol: "tcp"},
					{Action: "allow", Destination: "api.example.com", Port: "443", Protocol: "tcp"},
				},
				DenyDomains:      []string{"ads.example.com", "evil.example.com"},
				RebindProtection: true,
			},
		},
		{
			name: "single leading wildcard becomes the SDK domain suffix",
			policy: &SandboxNetworkPolicy{
				Default: egress.Deny,
				Allow:   []egress.Endpoint{mustEndpoint(t, "*.example.com", 443, egress.ProtocolHTTPS)},
			},
			want: microsandboxNetworkPlan{
				DefaultEgress: microsandboxPlanDefaultEgressDeny,
				Rules: []microsandboxNetworkRule{
					{Action: "allow", Destination: ".example.com", Port: "443", Protocol: "tcp"},
				},
				RebindProtection: true,
			},
		},
		{
			name: "udp and any protocols map to the SDK L4 values",
			policy: &SandboxNetworkPolicy{
				Default: egress.Deny,
				Allow: []egress.Endpoint{
					mustEndpoint(t, "dns.example.com", 53, egress.ProtocolUDP),
					mustEndpoint(t, "raw.example.com", 9000, egress.ProtocolAny),
				},
			},
			want: microsandboxNetworkPlan{
				DefaultEgress: microsandboxPlanDefaultEgressDeny,
				Rules: []microsandboxNetworkRule{
					{Action: "allow", Destination: "dns.example.com", Port: "53", Protocol: "udp"},
					{Action: "allow", Destination: "raw.example.com", Port: "9000", Protocol: ""},
				},
				RebindProtection: true,
			},
		},
		{
			name: "an inexpressible host pattern is rejected",
			policy: &SandboxNetworkPolicy{
				Default: egress.Deny,
				Allow:   []egress.Endpoint{{Host: "*.*.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planMicrosandboxNetwork(test.policy)
			if test.wantErr {
				if err == nil {
					t.Fatalf("planMicrosandboxNetwork() error = nil, want an error; plan = %+v", plan)
				}
				return
			}
			if err != nil {
				t.Fatalf("planMicrosandboxNetwork() error = %v", err)
			}
			if !reflect.DeepEqual(plan, test.want) {
				t.Fatalf("planMicrosandboxNetwork() = %+v, want %+v", plan, test.want)
			}
			if !plan.RebindProtection {
				t.Fatal("plan.RebindProtection = false, want true (DNS rebind protection must stay enabled)")
			}
		})
	}
}

// TestPlanMicrosandboxDenyPermitsEngineEndpointsAndBlocksOthers pins the
// reachability contract of default-deny: the engine-owned endpoints supplied by
// the caller stay allowed, declared allowances stay allowed, and an endpoint
// nobody declared falls through to the deny default.
func TestPlanMicrosandboxDenyPermitsEngineEndpointsAndBlocksOthers(t *testing.T) {
	policy, err := SandboxNetworkPolicyFromDeclaration(
		&egress.NetworkDeclaration{
			Default: egress.Deny,
			Allow:   []egress.AllowEntry{{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
		},
		[]egress.EngineEndpoint{
			mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS),
			mustEngineEndpoint(t, egress.PurposeTelemetry, "telemetry.internal", 4318, egress.ProtocolHTTP),
		},
		nil,
	)
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
	}

	plan, err := planMicrosandboxNetwork(&policy)
	if err != nil {
		t.Fatalf("planMicrosandboxNetwork() error = %v", err)
	}
	if plan.DefaultEgress != microsandboxPlanDefaultEgressDeny {
		t.Fatalf("DefaultEgress = %q, want %q", plan.DefaultEgress, microsandboxPlanDefaultEgressDeny)
	}
	for _, engineEndpoint := range policy.EngineEndpoints {
		if !planAllows(plan, engineEndpoint.Endpoint.Host, strconv.Itoa(engineEndpoint.Endpoint.Port), "tcp") {
			t.Fatalf("engine endpoint %+v is not permitted by plan %+v", engineEndpoint, plan)
		}
	}
	if !planAllows(plan, "api.example.com", "443", "tcp") {
		t.Fatalf("declared allowance is not permitted by plan %+v", plan)
	}
	if planAllows(plan, "exfil.example.com", "443", "tcp") {
		t.Fatalf("undeclared host is permitted by plan %+v", plan)
	}
	if planAllows(plan, "api.example.com", "8080", "tcp") {
		t.Fatalf("undeclared port is permitted by plan %+v", plan)
	}
}
