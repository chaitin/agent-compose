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
	for _, rule := range plan.Rules {
		if rule.Destination == destination && rule.Port == port && rule.Protocol == protocol {
			return rule.Action == "allow"
		}
	}
	return plan.DefaultEgress == microsandboxPlanDefaultEgressAllow
}

func TestPlanMicrosandboxNetwork(t *testing.T) {
	engineEndpoint := mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS)
	declaredAllow := mustAllow(t, "api.example.com", 443, egress.ProtocolHTTPS)

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
				Allow:   []egress.AllowEntry{declaredAllow},
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
				Allow:           []egress.AllowEntry{declaredAllow},
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
				Allow:   []egress.AllowEntry{mustAllow(t, "*.example.com", 443, egress.ProtocolHTTPS)},
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
				Allow: []egress.AllowEntry{
					mustAllow(t, "dns.example.com", 53, egress.ProtocolUDP),
					mustAllow(t, "raw.example.com", 9000, egress.ProtocolAny),
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
			name: "allow default with a denial renders the denial ahead of the open default",
			policy: &SandboxNetworkPolicy{
				Default: egress.Allow,
				Deny:    []egress.DenyEntry{{Host: "metadata.example.com", Port: 80, Protocol: egress.ProtocolTCP}},
			},
			want: microsandboxNetworkPlan{
				DefaultEgress: microsandboxPlanDefaultEgressAllow,
				Rules: []microsandboxNetworkRule{
					{Action: "deny", Destination: "metadata.example.com", Port: "80", Protocol: "tcp"},
				},
				RebindProtection: true,
			},
		},
		{
			name: "allowlist with a denial keeps engine endpoints first and denials before allowances",
			policy: &SandboxNetworkPolicy{
				Default:         egress.Deny,
				Allow:           []egress.AllowEntry{declaredAllow},
				Deny:            []egress.DenyEntry{{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
				EngineEndpoints: []egress.EngineEndpoint{engineEndpoint},
			},
			want: microsandboxNetworkPlan{
				DefaultEgress: microsandboxPlanDefaultEgressDeny,
				Rules: []microsandboxNetworkRule{
					{Action: "allow", Destination: "facade.internal", Port: "7410", Protocol: "tcp"},
					{Action: "deny", Destination: "api.example.com", Port: "443", Protocol: "tcp"},
					{Action: "allow", Destination: "api.example.com", Port: "443", Protocol: "tcp"},
				},
				RebindProtection: true,
			},
		},
		{
			name: "an inexpressible host pattern is rejected",
			policy: &SandboxNetworkPolicy{
				Default: egress.Deny,
				Allow:   []egress.AllowEntry{{Host: "*.*.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planMicrosandboxNetwork(test.policy, true)
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

	plan, err := planMicrosandboxNetwork(&policy, true)
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

func TestPlanMicrosandboxDenyListBlocksOnlyDeclaredDestinations(t *testing.T) {
	policy, err := SandboxNetworkPolicyFromDeclaration(
		&egress.NetworkDeclaration{
			Default: egress.Allow,
			Deny:    []egress.DenyEntry{{Host: "metadata.example.com", Port: 80, Protocol: egress.ProtocolTCP}},
		},
		[]egress.EngineEndpoint{mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS)},
		nil,
	)
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
	}
	plan, err := planMicrosandboxNetwork(&policy, true)
	if err != nil {
		t.Fatalf("planMicrosandboxNetwork() error = %v", err)
	}
	if plan.DefaultEgress != microsandboxPlanDefaultEgressAllow {
		t.Fatalf("DefaultEgress = %q, want %q", plan.DefaultEgress, microsandboxPlanDefaultEgressAllow)
	}
	if planAllows(plan, "metadata.example.com", "80", "tcp") {
		t.Fatalf("declared denial is permitted by plan %+v", plan)
	}
	if !planAllows(plan, "public.example.com", "443", "tcp") {
		t.Fatalf("undeclared destination is denied by plan %+v", plan)
	}
	if !planAllows(plan, "facade.internal", "7410", "tcp") {
		t.Fatalf("engine endpoint is not permitted by plan %+v", plan)
	}
}

// TestPlanMicrosandboxNetworkRebindProtectionFollowsTheSetting pins that the
// daemon-level switch reaches the rendered plan instead of being hard-coded on.
// The default itself lives in pkg/config; this function must pass through
// whatever the caller resolved.
func TestPlanMicrosandboxNetworkRebindProtectionFollowsTheSetting(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		plan, err := planMicrosandboxNetwork(nil, enabled)
		if err != nil {
			t.Fatalf("planMicrosandboxNetwork(nil, %v) error = %v", enabled, err)
		}
		if plan.RebindProtection != enabled {
			t.Fatalf("plan.RebindProtection = %v, want %v", plan.RebindProtection, enabled)
		}
	}
}

// TestPlanMicrosandboxNetworkKeepsCIDRDestinations pins that a CIDR entry reaches
// the SDK as a CIDR destination. The SDK accepts a range in Destination, and the
// plan must not drop or rewrite it: dropping it would silently widen or narrow the
// declaration while the admission report claimed the allowlist was installed.
func TestPlanMicrosandboxNetworkKeepsCIDRDestinations(t *testing.T) {
	policy, err := SandboxNetworkPolicyFromDeclaration(&egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow:   []egress.AllowEntry{mustAllow(t, "10.0.0.0/8", 443, egress.ProtocolAny)},
		Deny:    []egress.DenyEntry{{Host: "192.168.0.0/16", Port: 22, Protocol: egress.ProtocolTCP}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
	}
	plan, err := planMicrosandboxNetwork(&policy, true)
	if err != nil {
		t.Fatalf("planMicrosandboxNetwork() error = %v", err)
	}
	want := []microsandboxNetworkRule{
		{Action: "deny", Destination: "192.168.0.0/16", Port: "22", Protocol: "tcp"},
		{Action: "allow", Destination: "10.0.0.0/8", Port: "443", Protocol: ""},
	}
	if !reflect.DeepEqual(plan.Rules, want) {
		t.Fatalf("rules = %+v, want the CIDR denial before the CIDR allowance", plan.Rules)
	}
}

func TestMicrosandboxDestinationKeepsCIDR(t *testing.T) {
	for _, host := range []string{"10.0.0.0/8", "2001:db8::/32"} {
		got, err := microsandboxDestination(host)
		if err != nil {
			t.Fatalf("microsandboxDestination(%q) error = %v", host, err)
		}
		if got != host {
			t.Fatalf("microsandboxDestination(%q) = %q, want the range unchanged", host, got)
		}
	}
}
