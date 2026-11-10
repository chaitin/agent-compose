package compose

import (
	"errors"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
)

func TestNormalizeSandboxNetworkSpecL7Constraint(t *testing.T) {
	normalized, err := NormalizeSandboxNetworkSpec("policy.network", &SandboxNetworkSpec{
		Default: SandboxNetworkDefaultDenyAll,
		Allow: []SandboxNetworkAllowSpec{{
			Host:        "api.example.com",
			Port:        443,
			Methods:     []string{"post", "GET"},
			Paths:       []string{"/repos/*", "/health"},
			Enforcement: "audit",
		}},
	})
	if err != nil {
		t.Fatalf("NormalizeSandboxNetworkSpec() error = %v", err)
	}
	if len(normalized.Allow) != 1 {
		t.Fatalf("allow = %+v, want one entry", normalized.Allow)
	}
	entry := normalized.Allow[0]
	if strings.Join(entry.Methods, ",") != "GET,POST" {
		t.Fatalf("methods = %v, want canonical sorted uppercase methods", entry.Methods)
	}
	if strings.Join(entry.Paths, ",") != "/health,/repos/*" {
		t.Fatalf("paths = %v, want canonical sorted paths", entry.Paths)
	}
	if entry.Enforcement != "audit" {
		t.Fatalf("enforcement = %q, want audit", entry.Enforcement)
	}

	declaration, err := normalized.EgressDeclaration()
	if err != nil {
		t.Fatalf("EgressDeclaration() error = %v", err)
	}
	if len(declaration.Allow) != 1 {
		t.Fatalf("declaration allow = %+v, want one entry", declaration.Allow)
	}
	compiled := declaration.Allow[0]
	if strings.Join(compiled.L7.Methods, ",") != "GET,POST" || strings.Join(compiled.L7.Paths, ",") != "/health,/repos/*" {
		t.Fatalf("compiled L7 = %+v, want the normalized constraint", compiled.L7)
	}
	if compiled.Enforcement != egress.EnforcementAudit {
		t.Fatalf("compiled enforcement = %q, want audit", compiled.Enforcement)
	}
}

func TestNormalizeSandboxNetworkSpecOmitsDefaultL7Fields(t *testing.T) {
	normalized, err := NormalizeSandboxNetworkSpec("policy.network", &SandboxNetworkSpec{
		Default: SandboxNetworkDefaultDenyAll,
		Allow: []SandboxNetworkAllowSpec{{
			Host: "api.example.com",
			Port: 443,
			// enforcement: enforce is the default and is not persisted, so an
			// existing declaration keeps its canonical JSON and hash.
			Enforcement: "enforce",
		}},
	})
	if err != nil {
		t.Fatalf("NormalizeSandboxNetworkSpec() error = %v", err)
	}
	entry := normalized.Allow[0]
	if len(entry.Methods) != 0 || len(entry.Paths) != 0 || entry.Enforcement != "" {
		t.Fatalf("entry = %+v, want the L4 zero value", entry)
	}
	spec := &NormalizedProjectSpec{Name: "demo", Agents: []NormalizedAgentSpec{{
		Name:    "worker",
		Enabled: true,
		Policy:  &NormalizedSandboxSpec{Network: normalized},
	}}}
	data, err := spec.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON() error = %v", err)
	}
	for _, field := range []string{"methods", "paths", "enforcement"} {
		if strings.Contains(string(data), `"`+field+`"`) {
			t.Fatalf("canonical JSON contains %q for an entry that does not use L7: %s", field, data)
		}
	}
}

func TestNormalizeSandboxNetworkSpecRejectsInvalidL7(t *testing.T) {
	tests := []struct {
		name        string
		entry       SandboxNetworkAllowSpec
		wantPath    string
		wantMessage string
	}{
		{
			name:        "invalid method",
			entry:       SandboxNetworkAllowSpec{Host: "api.example.com", Port: 443, Methods: []string{"GE T"}},
			wantPath:    "policy.network.allow[0]",
			wantMessage: "not a valid HTTP method",
		},
		{
			name:        "relative path",
			entry:       SandboxNetworkAllowSpec{Host: "api.example.com", Port: 443, Paths: []string{"v1/items"}},
			wantPath:    "policy.network.allow[0]",
			wantMessage: "must start with",
		},
		{
			name:        "unknown enforcement",
			entry:       SandboxNetworkAllowSpec{Host: "api.example.com", Port: 443, Enforcement: "monitor"},
			wantPath:    "policy.network.allow[0].enforcement",
			wantMessage: "enforcement must be",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeSandboxNetworkSpec("policy.network", &SandboxNetworkSpec{
				Default: SandboxNetworkDefaultDenyAll,
				Allow:   []SandboxNetworkAllowSpec{test.entry},
			})
			if err == nil {
				t.Fatal("NormalizeSandboxNetworkSpec() error = nil, want a validation error")
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want a *ValidationError", err)
			}
			if validation.Path != test.wantPath {
				t.Fatalf("path = %q, want %q", validation.Path, test.wantPath)
			}
			if !strings.Contains(validation.Message, test.wantMessage) {
				t.Fatalf("message = %q, want containing %q", validation.Message, test.wantMessage)
			}
		})
	}
}

func TestCloneNormalizedNetworkAllowCopiesL7Slices(t *testing.T) {
	values := []NormalizedSandboxNetworkAllowSpec{{
		Host:    "api.example.com",
		Port:    443,
		Methods: []string{"GET"},
		Paths:   []string{"/v1/*"},
	}}
	cloned := cloneNormalizedNetworkAllow(values)
	cloned[0].Methods[0] = "POST"
	cloned[0].Paths[0] = "/v2/*"
	if values[0].Methods[0] != "GET" || values[0].Paths[0] != "/v1/*" {
		t.Fatalf("clone shares the L7 backing arrays with the source: %+v", values[0])
	}
}

func TestNormalizeSandboxNetworkSpecInjectedHeadersRequireL7AndCarryTemplates(t *testing.T) {
	normalized, err := NormalizeSandboxNetworkSpec("policy.network", &SandboxNetworkSpec{
		Default: SandboxNetworkDefaultDenyAll,
		Allow: []SandboxNetworkAllowSpec{{
			Host: "api.example.com", Port: 443,
			Methods: []string{"GET"},
			InjectHeaders: map[string]string{
				"Authorization": "Bearer ${AGENT_COMPOSE_TEST_TOKEN}",
			},
		}},
	})
	if err != nil {
		t.Fatalf("NormalizeSandboxNetworkSpec() error = %v", err)
	}
	entry := normalized.Allow[0]
	if entry.InjectHeaders["Authorization"] != "Bearer ${AGENT_COMPOSE_TEST_TOKEN}" {
		t.Fatalf("inject headers = %+v, want the env template", entry.InjectHeaders)
	}
	declaration, err := normalized.EgressDeclaration()
	if err != nil {
		t.Fatalf("EgressDeclaration() error = %v", err)
	}
	if declaration.Allow[0].InjectHeaders["Authorization"] != "Bearer ${AGENT_COMPOSE_TEST_TOKEN}" {
		t.Fatalf("compiled inject headers = %+v, want the env template", declaration.Allow[0].InjectHeaders)
	}
	if _, err := NormalizeSandboxNetworkSpec("policy.network", &SandboxNetworkSpec{
		Default: SandboxNetworkDefaultDenyAll,
		Allow: []SandboxNetworkAllowSpec{{
			Host:          "api.example.com",
			Port:          443,
			InjectHeaders: map[string]string{"Authorization": "Bearer ${TOKEN}"},
		}},
	}); err == nil {
		t.Fatal("NormalizeSandboxNetworkSpec() accepted injection without an L7 constraint")
	}
	if _, err := NormalizeSandboxNetworkSpec("policy.network", &SandboxNetworkSpec{
		Default: SandboxNetworkDefaultDenyAll,
		Allow: []SandboxNetworkAllowSpec{{
			Host: "api.example.com", Port: 443, Methods: []string{"GET"},
			InjectHeaders: map[string]string{"Authorization": "literal-secret"},
		}},
	}); err == nil {
		t.Fatal("NormalizeSandboxNetworkSpec() accepted a literal secret")
	}
}
