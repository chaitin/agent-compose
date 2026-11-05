package compose

import (
	"strings"
	"testing"
)

func runComposeNetworkPolicyIntegration(t *testing.T) {
	t.Helper()
	normalized, err := NormalizeSandboxNetworkSpec("policy.network", &SandboxNetworkSpec{
		Default: SandboxNetworkDefaultDenyAll,
		Allow: []SandboxNetworkAllowSpec{{
			Host: "api.example.com", Port: 443, Methods: []string{"GET"}, Paths: []string{"/v1/*"},
			InjectHeaders: map[string]string{"Authorization": "Bearer ${COMPOSE_NETWORK_TOKEN}"},
		}},
		Deny: []SandboxNetworkDenySpec{{Host: "blocked.example.com", Port: 22}},
	})
	if err != nil {
		t.Fatalf("NormalizeSandboxNetworkSpec() error = %v", err)
	}
	declaration, err := normalized.EgressDeclaration()
	if err != nil {
		t.Fatalf("EgressDeclaration() error = %v", err)
	}
	if len(declaration.Allow) != 1 || declaration.Allow[0].InjectHeaders["Authorization"] == "" {
		t.Fatalf("declaration = %+v, want injected allowance", declaration)
	}
	spec := &NormalizedProjectSpec{Name: "integration", Agents: []NormalizedAgentSpec{{
		Name: "worker", Enabled: true, Policy: &NormalizedSandboxSpec{Network: normalized},
	}}}
	data, err := spec.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON() error = %v", err)
	}
	if !strings.Contains(string(data), "inject_headers") {
		t.Fatalf("canonical project does not contain inject_headers: %s", data)
	}
	clone := cloneNormalizedNetworkAllow(normalized.Allow)
	clone[0].InjectHeaders["Authorization"] = "mutated"
	if normalized.Allow[0].InjectHeaders["Authorization"] == "mutated" {
		t.Fatal("cloneNormalizedNetworkAllow mutated the original map")
	}
}

func TestComposeNetworkPolicyIntegration(t *testing.T) { runComposeNetworkPolicyIntegration(t) }
func TestComposeNetworkPolicyE2E(t *testing.T)         { runComposeNetworkPolicyIntegration(t) }
