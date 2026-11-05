package driver

import (
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
)

func TestSandboxNetworkPolicyFromDeclarationKeepsL7Constraint(t *testing.T) {
	declaration := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow: []egress.AllowEntry{{
			Host:        "api.example.com",
			Port:        443,
			Protocol:    egress.ProtocolHTTPS,
			L7:          egress.L7Constraint{Methods: []string{"GET"}, Paths: []string{"/v1/*"}},
			Enforcement: egress.EnforcementAudit,
		}},
	}
	policy, err := SandboxNetworkPolicyFromDeclaration(declaration, nil, nil)
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
	}
	if len(policy.Allow) != 1 {
		t.Fatalf("allow = %+v, want one entry", policy.Allow)
	}
	entry := policy.Allow[0]
	if strings.Join(entry.L7.Methods, ",") != "GET" || strings.Join(entry.L7.Paths, ",") != "/v1/*" {
		t.Fatalf("L7 = %+v, want the declared constraint", entry.L7)
	}
	if entry.Enforcement != egress.EnforcementAudit {
		t.Fatalf("enforcement = %q, want audit", entry.Enforcement)
	}
}

func TestSandboxNetworkPolicyFromDeclarationDefaultsToDestinationOnly(t *testing.T) {
	declaration := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow: []egress.AllowEntry{{
			Host:     "api.example.com",
			Port:     443,
			Protocol: egress.ProtocolHTTPS,
		}},
	}
	policy, err := SandboxNetworkPolicyFromDeclaration(declaration, nil, nil)
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
	}
	entry := policy.Allow[0]
	if !entry.L7.Empty() || entry.Enforcement != "" {
		t.Fatalf("entry = %+v, want the destination-only zero value", entry)
	}
}

func TestNewSandboxNetworkPolicyRejectsInvalidL7(t *testing.T) {
	_, err := NewSandboxNetworkPolicy(egress.Deny, []egress.AllowEntry{{
		Host:     "api.example.com",
		Port:     443,
		Protocol: egress.ProtocolHTTPS,
		L7:       egress.L7Constraint{Paths: []string{"v1/items"}},
	}}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "must start with") {
		t.Fatalf("NewSandboxNetworkPolicy() error = %v, want an invalid-path rejection", err)
	}
}
