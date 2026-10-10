package compose

import (
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// The golden hashes were captured from the code before SandboxSpec.network
// existed. An undeclared network policy must not change the canonical JSON, and
// therefore must not change the spec hash.
const (
	undeclaredNetworkGoldenHash = "sha256:f08cbeba0de6afd597bc83977f5ada85d4a10ccdc7d0afe070aa4c9a84899534"
	stoppedRuntimeGoldenHash    = "sha256:78fec1f44000f7ae853b23f8021164adf53746ad6a8583fee43a7eae5f2c6957"
)

func TestUndeclaredSandboxNetworkKeepsCanonicalHash(t *testing.T) {
	spec := mustNormalizeCompose(t, `
name: network-golden
agents:
  worker:
    provider: codex
`, nil)
	canonical, err := spec.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	if strings.Contains(string(canonical), `"network"`) {
		t.Fatalf("undeclared network policy leaked into canonical JSON: %s", canonical)
	}
	if got := mustHash(t, spec); got != undeclaredNetworkGoldenHash {
		t.Fatalf("hash = %s, want the pre-network golden %s", got, undeclaredNetworkGoldenHash)
	}
}

func TestStoppedRuntimeOnlySandboxKeepsCanonicalHash(t *testing.T) {
	spec := mustNormalizeCompose(t, `
name: network-golden
agents:
  worker:
    provider: codex
    sandbox:
      stopped_runtime_policy: remove
`, nil)
	canonical, err := spec.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	if strings.Contains(string(canonical), `"network"`) {
		t.Fatalf("undeclared network policy leaked into canonical JSON: %s", canonical)
	}
	if got := mustHash(t, spec); got != stoppedRuntimeGoldenHash {
		t.Fatalf("hash = %s, want the pre-network golden %s", got, stoppedRuntimeGoldenHash)
	}
}

func TestSpecHashIncludesDeclaredSandboxNetwork(t *testing.T) {
	undeclared := mustNormalizeCompose(t, `
name: network-hash
agents:
  worker:
    provider: codex
`, nil)
	declaredAllowAll := mustNormalizeCompose(t, `
name: network-hash
agents:
  worker:
    provider: codex
    sandbox:
      network: {}
`, nil)
	denied := mustNormalizeCompose(t, `
name: network-hash
agents:
  worker:
    provider: codex
    sandbox:
      network:
        default: deny
`, nil)
	withAllow := mustNormalizeCompose(t, `
name: network-hash
agents:
  worker:
    provider: codex
    sandbox:
      network:
        default: deny
        allow:
          - host: api.github.com
            port: 443
            protocol: https
`, nil)
	reorderedAllow := mustNormalizeCompose(t, `
name: network-hash
agents:
  worker:
    provider: codex
    sandbox:
      network:
        default: deny
        allow:
          - host: registry.npmjs.org
            port: 443
            protocol: https
          - host: api.github.com
            port: 443
            protocol: https
`, nil)
	reorderedAllowAgain := mustNormalizeCompose(t, `
name: network-hash
agents:
  worker:
    provider: codex
    sandbox:
      network:
        default: deny
        allow:
          - host: api.github.com
            port: 443
            protocol: https
          - host: registry.npmjs.org
            port: 443
            protocol: https
`, nil)

	base := mustHash(t, undeclared)
	for name, spec := range map[string]*NormalizedProjectSpec{
		"empty declared block": declaredAllowAll,
		"deny default":         denied,
		"allow entry":          withAllow,
	} {
		if got := mustHash(t, spec); got == base {
			t.Fatalf("%s did not change the spec hash", name)
		}
	}
	if mustHash(t, denied) == mustHash(t, withAllow) {
		t.Fatal("an allow entry did not change the spec hash")
	}
	if mustHash(t, reorderedAllow) != mustHash(t, reorderedAllowAgain) {
		t.Fatal("allow entry order changed the spec hash; normalization must sort the list")
	}
}

func TestNormalizeSandboxNetwork(t *testing.T) {
	tests := []struct {
		name    string
		network string
		want    *NormalizedSandboxNetworkSpec
	}{
		{
			name:    "declared block with no fields is allow-all",
			network: "network: {}",
			want:    &NormalizedSandboxNetworkSpec{Default: SandboxNetworkDefaultAllowAll},
		},
		{
			name: "deny with a patterned allow entry",
			network: `network:
        default: deny
        allow:
          - host: "*.example.com"
            port: 8443
            protocol: tcp`,
			want: &NormalizedSandboxNetworkSpec{
				Default: SandboxNetworkDefaultDeny,
				Allow: []NormalizedSandboxNetworkAllowSpec{
					{Host: "*.example.com", Port: 8443, Protocol: "tcp"},
				},
			},
		},
		{
			name: "omitted protocol normalizes to any",
			network: `network:
        allow:
          - host: api.github.com
            port: 443`,
			want: &NormalizedSandboxNetworkSpec{
				Default: SandboxNetworkDefaultAllowAll,
				Allow: []NormalizedSandboxNetworkAllowSpec{
					{Host: "api.github.com", Port: 443, Protocol: "any"},
				},
			},
		},
		{
			name: "allow entries are sorted and hosts lowercased",
			network: `network:
        default: deny
        allow:
          - host: Registry.NPMJS.org
            port: 443
            protocol: https
          - host: API.github.com
            port: 443
            protocol: https`,
			want: &NormalizedSandboxNetworkSpec{
				Default: SandboxNetworkDefaultDeny,
				Allow: []NormalizedSandboxNetworkAllowSpec{
					{Host: "api.github.com", Port: 443, Protocol: "https"},
					{Host: "registry.npmjs.org", Port: 443, Protocol: "https"},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized := mustNormalizeCompose(t, "name: network\nagents:\n  worker:\n    provider: codex\n    sandbox:\n      "+tc.network+"\n", nil)
			got := normalized.Agents[0].Sandbox.Network
			if got == nil {
				t.Fatal("network declaration was dropped during normalization")
			}
			if got.Default != tc.want.Default {
				t.Fatalf("default = %q, want %q", got.Default, tc.want.Default)
			}
			if len(got.Allow) != len(tc.want.Allow) {
				t.Fatalf("allow = %+v, want %+v", got.Allow, tc.want.Allow)
			}
			for i := range got.Allow {
				if got.Allow[i] != tc.want.Allow[i] {
					t.Fatalf("allow[%d] = %+v, want %+v", i, got.Allow[i], tc.want.Allow[i])
				}
			}
		})
	}
}

func TestSandboxNetworkRejectsInvalidDeclarations(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "unknown default",
			body:    "sandbox:\n      network:\n        default: allow",
			wantErr: "network default must be",
		},
		{
			name:    "unknown field under network",
			body:    "sandbox:\n      network:\n        mode: deny",
			wantErr: "unknown field",
		},
		{
			name:    "unknown field under allow entry",
			body:    "sandbox:\n      network:\n        allow:\n          - host: api.github.com\n            port: 443\n            path: /v1",
			wantErr: "unknown field",
		},
		{
			name:    "missing host",
			body:    "sandbox:\n      network:\n        allow:\n          - port: 443",
			wantErr: "host must not be empty",
		},
		{
			name:    "invalid host label separator",
			body:    "sandbox:\n      network:\n        allow:\n          - host: \"foo*bar.example.com\"\n            port: 443",
			wantErr: "may contain only letters",
		},
		{
			name:    "wildcard inside a label",
			body:    "sandbox:\n      network:\n        allow:\n          - host: \"ap*i.example.com\"\n            port: 443",
			wantErr: "may contain only letters",
		},
		{
			name:    "port out of range",
			body:    "sandbox:\n      network:\n        allow:\n          - host: api.github.com\n            port: 70000",
			wantErr: "port must be between 1 and 65535",
		},
		{
			name:    "missing port",
			body:    "sandbox:\n      network:\n        allow:\n          - host: api.github.com",
			wantErr: "port must be between 1 and 65535",
		},
		{
			name:    "unknown protocol",
			body:    "sandbox:\n      network:\n        allow:\n          - host: api.github.com\n            port: 443\n            protocol: icmp",
			wantErr: "protocol must be one of",
		},
		{
			name:    "duplicate allow entry",
			body:    "sandbox:\n      network:\n        allow:\n          - host: api.github.com\n            port: 443\n            protocol: https\n          - host: api.github.com\n            port: 443\n            protocol: https",
			wantErr: "duplicate allow entry",
		},
		{
			name:    "allow is not a sequence",
			body:    "sandbox:\n      network:\n        allow:\n          host: api.github.com",
			wantErr: "expected sequence",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := "name: network\nagents:\n  worker:\n    provider: codex\n    " + tc.body + "\n"
			// Strict parsing rejects an unknown field before normalization, so
			// both stages count as rejecting the declaration.
			spec, err := Parse([]byte(raw))
			if err == nil {
				_, err = Normalize(spec, NormalizeOptions{})
			}
			if err == nil {
				t.Fatalf("declaration was accepted, want error %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestSandboxNetworkEgressDeclaration(t *testing.T) {
	spec := mustNormalizeCompose(t, `
name: network-egress
agents:
  worker:
    provider: codex
    sandbox:
      network:
        default: deny
        allow:
          - host: api.github.com
            port: 443
            protocol: https
`, nil)
	declaration := spec.Agents[0].Sandbox.Network.EgressDeclaration()
	if declaration.Default != egress.Deny {
		t.Fatalf("default = %q, want %q", declaration.Default, egress.Deny)
	}
	if len(declaration.Allow) != 1 {
		t.Fatalf("allow = %+v, want one entry", declaration.Allow)
	}
	entry := declaration.Allow[0]
	if entry.Host != "api.github.com" || entry.Port != 443 || entry.Protocol != egress.ProtocolHTTPS {
		t.Fatalf("allow entry = %+v, want api.github.com:443/https", entry)
	}
}

// TestSandboxNetworkEgressDeclarationAllowAll pins the default-allow path. The
// compose vocabulary ("allow-all") and the decision model's ("allow") differ, so
// passing the compose value through produced a declaration the decision model
// rejects and made every declared allow-all policy unusable.
func TestSandboxNetworkEgressDeclarationAllowAll(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "omitted default normalizes to allow-all", body: "network: {}"},
		{name: "explicit allow-all", body: "network:\n        default: allow-all"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := mustNormalizeCompose(t, `
name: network-egress-allow
agents:
  worker:
    provider: codex
    sandbox:
      `+test.body+`
`, nil)
			declaration := spec.Agents[0].Sandbox.Network.EgressDeclaration()
			if declaration.Default != egress.Allow {
				t.Fatalf("default = %q, want %q", declaration.Default, egress.Allow)
			}
			if err := declaration.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			policy, err := egress.EffectiveNetworkPolicy(&declaration, nil)
			if err != nil {
				t.Fatalf("EffectiveNetworkPolicy() error = %v", err)
			}
			result := egress.Decide(policy, egress.Request{Kind: egress.KindNetworkEndpoint, Name: "anywhere.example.com:443/https"})
			if result.Action != egress.Allow {
				t.Fatalf("action = %q, want %q", result.Action, egress.Allow)
			}
		})
	}
}

// TestEgressActionForSandboxNetworkDefaultFailsClosed pins that only the
// declared allow-all value becomes allow: any unexpected default maps to deny
// rather than silently widening access.
func TestEgressActionForSandboxNetworkDefaultFailsClosed(t *testing.T) {
	if got := egressActionForSandboxNetworkDefault(SandboxNetworkDefaultAllowAll); got != egress.Allow {
		t.Fatalf("allow-all mapped to %q, want %q", got, egress.Allow)
	}
	for _, value := range []string{SandboxNetworkDefaultDeny, "", "sideways"} {
		if got := egressActionForSandboxNetworkDefault(value); got != egress.Deny {
			t.Fatalf("%q mapped to %q, want %q", value, got, egress.Deny)
		}
	}
}

// TestSandboxNetworkReturnsDeepClone pins the clone the canonical output relies
// on: mutating a returned view must not change the spec it came from.
func TestSandboxNetworkReturnsDeepClone(t *testing.T) {
	spec := mustNormalizeCompose(t, `
name: network-clone
agents:
  worker:
    provider: codex
    sandbox:
      network:
        default: deny
        allow:
          - host: api.github.com
            port: 443
            protocol: https
`, nil)
	before := mustHash(t, spec)

	cloned := spec.Redacted()
	cloned.Agents[0].Sandbox.Network.Default = SandboxNetworkDefaultAllowAll
	cloned.Agents[0].Sandbox.Network.Allow[0].Host = "attacker.example.com"
	cloned.Agents[0].Sandbox.Network.Allow = append(cloned.Agents[0].Sandbox.Network.Allow, NormalizedSandboxNetworkAllowSpec{Host: "extra.example.com", Port: 1, Protocol: "tcp"})

	if after := mustHash(t, spec); after != before {
		t.Fatalf("hash changed after mutating a returned view: %s != %s", after, before)
	}
}
