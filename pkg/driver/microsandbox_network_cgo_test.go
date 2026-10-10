//go:build linux && cgo && microsandboxcgo

package driver

import (
	"reflect"
	"testing"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// TestMicrosandboxSDKNetworkConfigBindsThePlan pins the build-tagged adapter
// against the real SDK types, so the untagged plan cannot drift from what the
// runtime hands the hypervisor.
func TestMicrosandboxSDKNetworkConfigBindsThePlan(t *testing.T) {
	policy, err := SandboxNetworkPolicyFromDeclaration(
		&egress.NetworkDeclaration{
			Default: egress.Deny,
			Allow:   []egress.AllowEntry{{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
		},
		[]egress.EngineEndpoint{
			mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS),
		},
		[]string{"ads.example.com"},
	)
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
	}

	config, err := microsandboxSDKNetworkConfig(&policy)
	if err != nil {
		t.Fatalf("microsandboxSDKNetworkConfig() error = %v", err)
	}
	if config.DefaultEgress != microsandbox.PolicyActionDeny {
		t.Fatalf("DefaultEgress = %q, want %q", config.DefaultEgress, microsandbox.PolicyActionDeny)
	}
	if config.DefaultIngress != microsandbox.PolicyActionAllow {
		t.Fatalf("DefaultIngress = %q, want %q", config.DefaultIngress, microsandbox.PolicyActionAllow)
	}
	if config.DNS == nil || config.DNS.RebindProtection == nil || !*config.DNS.RebindProtection {
		t.Fatalf("DNS.RebindProtection = %+v, want enabled", config.DNS)
	}
	if len(config.DenyDomains) != 1 || config.DenyDomains[0] != "ads.example.com" {
		t.Fatalf("DenyDomains = %+v, want [ads.example.com]", config.DenyDomains)
	}
	wantRules := []microsandbox.PolicyRule{
		{Action: microsandbox.PolicyActionAllow, Direction: microsandbox.PolicyDirectionEgress, Destination: "facade.internal", Port: "7410", Protocol: microsandbox.PolicyProtocolTCP},
		{Action: microsandbox.PolicyActionAllow, Direction: microsandbox.PolicyDirectionEgress, Destination: "api.example.com", Port: "443", Protocol: microsandbox.PolicyProtocolTCP},
	}
	if len(config.Rules) != len(wantRules) {
		t.Fatalf("Rules = %+v, want %+v", config.Rules, wantRules)
	}
	for i, want := range wantRules {
		if !reflect.DeepEqual(config.Rules[i], want) {
			t.Fatalf("Rules[%d] = %+v, want %+v", i, config.Rules[i], want)
		}
	}
}

func TestMicrosandboxSDKNetworkConfigKeepsUndeclaredPermissive(t *testing.T) {
	config, err := microsandboxSDKNetworkConfig(nil)
	if err != nil {
		t.Fatalf("microsandboxSDKNetworkConfig(nil) error = %v", err)
	}
	if config.DefaultEgress != microsandbox.PolicyActionAllow {
		t.Fatalf("DefaultEgress = %q, want %q", config.DefaultEgress, microsandbox.PolicyActionAllow)
	}
	if len(config.Rules) != 0 {
		t.Fatalf("Rules = %+v, want none for an undeclared policy", config.Rules)
	}
	if config.DNS == nil || config.DNS.RebindProtection == nil || !*config.DNS.RebindProtection {
		t.Fatalf("DNS.RebindProtection = %+v, want enabled", config.DNS)
	}
}
