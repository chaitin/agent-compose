package compose

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// The golden hashes were captured from the code before SandboxSpec.network
// existed. An undeclared network policy must not change the canonical JSON, and
// therefore must not change the spec hash.
//
// stoppedRuntimeGoldenHash was re-baselined when the agent policy block was
// renamed from `sandbox` to `policy`: the canonical JSON of a spec that declares
// the block necessarily changed key, so one reconciliation is expected per agent
// that declares a policy. A spec that declares no policy block is unaffected,
// which undeclaredNetworkGoldenHash still pins.
const (
	undeclaredNetworkGoldenHash = "sha256:f08cbeba0de6afd597bc83977f5ada85d4a10ccdc7d0afe070aa4c9a84899534"
	stoppedRuntimeGoldenHash    = "sha256:020d1dce179d7a8f38ae1341bc1a03267018bc4588947c56473c65ed1802248a"
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
        default: deny-all
`, nil)
	withAllow := mustNormalizeCompose(t, `
name: network-hash
agents:
  worker:
    provider: codex
    sandbox:
      network:
        default: deny-all
        allow:
          - host: api.github.com
            port: 443
`, nil)
	reorderedAllow := mustNormalizeCompose(t, `
name: network-hash
agents:
  worker:
    provider: codex
    sandbox:
      network:
        default: deny-all
        allow:
          - host: registry.npmjs.org
            port: 443
          - host: api.github.com
            port: 443
`, nil)
	reorderedAllowAgain := mustNormalizeCompose(t, `
name: network-hash
agents:
  worker:
    provider: codex
    sandbox:
      network:
        default: deny-all
        allow:
          - host: api.github.com
            port: 443
          - host: registry.npmjs.org
            port: 443
`, nil)
	withPorts := mustNormalizeCompose(t, `
name: network-hash
agents:
  worker:
    provider: codex
    sandbox:
      network:
        default: deny-all
        allow:
          - host: api.github.com
            ports: [443, 80]
`, nil)
	withSplitPorts := mustNormalizeCompose(t, `
name: network-hash
agents:
  worker:
    provider: codex
    sandbox:
      network:
        default: deny-all
        allow:
          - host: api.github.com
            port: 80
          - host: api.github.com
            port: 443
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
	if mustHash(t, withPorts) != mustHash(t, withSplitPorts) {
		t.Fatal("ports expansion changed the spec hash; it must normalize to the same entries")
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
        default: deny-all
        allow:
          - host: "*.example.com"
            port: 8443`,
			want: &NormalizedSandboxNetworkSpec{
				Default: SandboxNetworkDefaultDenyAll,
				Allow: []NormalizedSandboxNetworkAllowSpec{
					{Host: "*.example.com", Port: 8443},
				},
			},
		},
		{
			name: "a plain host-and-port entry has no protocol label",
			network: `network:
        allow:
          - host: api.github.com
            port: 443`,
			want: &NormalizedSandboxNetworkSpec{
				Default: SandboxNetworkDefaultAllowAll,
				Allow: []NormalizedSandboxNetworkAllowSpec{
					{Host: "api.github.com", Port: 443},
				},
			},
		},
		{
			name: "allow entries are sorted and hosts lowercased",
			network: `network:
        default: deny-all
        allow:
          - host: Registry.NPMJS.org
            port: 443
          - host: API.github.com
            port: 443`,
			want: &NormalizedSandboxNetworkSpec{
				Default: SandboxNetworkDefaultDenyAll,
				Allow: []NormalizedSandboxNetworkAllowSpec{
					{Host: "api.github.com", Port: 443},
					{Host: "registry.npmjs.org", Port: 443},
				},
			},
		},
		{
			name: "deny-all is the canonical spelling",
			network: `network:
        default: deny-all`,
			want: &NormalizedSandboxNetworkSpec{Default: SandboxNetworkDefaultDenyAll},
		},
		{
			name: "allow ports expand into canonical entries",
			network: `network:
        default: deny-all
        allow:
          - host: API.github.com
            ports: [443, 80]`,
			want: &NormalizedSandboxNetworkSpec{
				Default: SandboxNetworkDefaultDenyAll,
				Allow: []NormalizedSandboxNetworkAllowSpec{
					{Host: "api.github.com", Port: 80},
					{Host: "api.github.com", Port: 443},
				},
			},
		},
		{
			name: "deny ports expand into canonical entries",
			network: `network:
        deny:
          - host: blocked.example.com
            ports: [8443, 443]`,
			want: &NormalizedSandboxNetworkSpec{
				Default: SandboxNetworkDefaultAllowAll,
				Deny: []NormalizedSandboxNetworkDenySpec{
					{Host: "blocked.example.com", Port: 443},
					{Host: "blocked.example.com", Port: 8443},
				},
			},
		},
		{
			name: "IPv6 literals and CIDR ranges are canonical destinations",
			network: `network:
        default: deny-all
        allow:
          - host: 2001:0DB8::1
            port: 443
          - host: 10.2.16.21/8
            ports: [80, 443]
          - host: 2001:db8::/32
            port: 8443`,
			want: &NormalizedSandboxNetworkSpec{
				Default: SandboxNetworkDefaultDenyAll,
				Allow: []NormalizedSandboxNetworkAllowSpec{
					{Host: "10.0.0.0/8", Port: 80},
					{Host: "10.0.0.0/8", Port: 443},
					{Host: "2001:db8::/32", Port: 8443},
					{Host: "2001:db8::1", Port: 443},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized := mustNormalizeCompose(t, "name: network\nagents:\n  worker:\n    provider: codex\n    sandbox:\n      "+tc.network+"\n", nil)
			got := normalized.Agents[0].PolicyBlock().Network
			if got == nil {
				t.Fatal("network declaration was dropped during normalization")
				return
			}
			if got.Default != tc.want.Default {
				t.Fatalf("default = %q, want %q", got.Default, tc.want.Default)
			}
			if len(got.Allow) != len(tc.want.Allow) {
				t.Fatalf("allow = %+v, want %+v", got.Allow, tc.want.Allow)
			}
			for i := range got.Allow {
				if !reflect.DeepEqual(got.Allow[i], tc.want.Allow[i]) {
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
			name:    "historical deny spelling is rejected with the replacement",
			body:    "sandbox:\n      network:\n        default: deny",
			wantErr: `network default "deny" is no longer supported; use "deny-all"`,
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
			name:    "invalid CIDR host",
			body:    "sandbox:\n      network:\n        allow:\n          - host: 10.0.0.0/33\n            port: 443",
			wantErr: "must be a host name, IP address, or CIDR range",
		},
		{
			name:    "protocol is not a declaration field",
			body:    "sandbox:\n      network:\n        allow:\n          - host: api.github.com\n            port: 443\n            protocol: https",
			wantErr: "protocol was removed from network entries",
		},
		{
			name:    "duplicate allow entry",
			body:    "sandbox:\n      network:\n        allow:\n          - host: api.github.com\n            port: 443\n\n          - host: api.github.com\n            port: 443",
			wantErr: "duplicate allow entry",
		},
		{
			name:    "allow is not a sequence",
			body:    "sandbox:\n      network:\n        allow:\n          host: api.github.com",
			wantErr: "expected sequence",
		},
		{
			name:    "port and ports together",
			body:    "sandbox:\n      network:\n        allow:\n          - host: api.github.com\n            port: 443\n            ports: [80]",
			wantErr: "specify port or ports, not both",
		},
		{
			name:    "ports is not a sequence",
			body:    "sandbox:\n      network:\n        allow:\n          - host: api.github.com\n            ports: 443",
			wantErr: "expected sequence",
		},
		{
			name:    "ports entry out of range",
			body:    "sandbox:\n      network:\n        allow:\n          - host: api.github.com\n            ports: [443, 70000]",
			wantErr: "port must be between 1 and 65535",
		},
		{
			name:    "duplicate port in ports",
			body:    "sandbox:\n      network:\n        allow:\n          - host: api.github.com\n            ports: [443, 443]",
			wantErr: "duplicate port",
		},
		{
			name:    "empty ports",
			body:    "sandbox:\n      network:\n        deny:\n          - host: blocked.example.com\n            ports: []",
			wantErr: "port must be between 1 and 65535",
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
	tests := []struct {
		name        string
		network     string
		wantDefault egress.Action
		wantAllow   []egress.AllowEntry
	}{
		{
			name: "declared deny with an allowance",
			network: `network:
        default: deny-all
        allow:
          - host: api.github.com
            port: 443`,
			wantDefault: egress.Deny,
			wantAllow:   []egress.AllowEntry{{Host: "api.github.com", Port: 443, Protocol: egress.ProtocolAny}},
		},
		{
			// The schema's "allow-all" is not an egress.Action; the seam has to
			// translate it. An omitted default normalizes to "allow-all", so a
			// declaration that only lists allowances lands here too.
			name: "omitted default translates allow-all to allow",
			network: `network:
        allow:
          - host: api.github.com
            port: 443`,
			wantDefault: egress.Allow,
			wantAllow:   []egress.AllowEntry{{Host: "api.github.com", Port: 443, Protocol: egress.ProtocolAny}},
		},
		{
			name:        "explicit allow-all translates to allow",
			network:     "network:\n        default: allow-all",
			wantDefault: egress.Allow,
		},
		{
			name:        "empty declared block is allow-all",
			network:     "network: {}",
			wantDefault: egress.Allow,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := mustNormalizeCompose(t, "name: network-egress\nagents:\n  worker:\n    provider: codex\n    sandbox:\n      "+tt.network+"\n", nil)
			declaration, err := spec.Agents[0].PolicyBlock().Network.EgressDeclaration()
			if err != nil {
				t.Fatalf("EgressDeclaration returned error: %v", err)
			}
			if declaration.Default != tt.wantDefault {
				t.Fatalf("default = %q, want %q", declaration.Default, tt.wantDefault)
			}
			if len(declaration.Allow) != len(tt.wantAllow) {
				t.Fatalf("allow = %+v, want %+v", declaration.Allow, tt.wantAllow)
			}
			for i, want := range tt.wantAllow {
				if !reflect.DeepEqual(declaration.Allow[i], want) {
					t.Fatalf("allow[%d] = %+v, want %+v", i, declaration.Allow[i], want)
				}
			}
			// The whole point of the translation is that the decision model
			// accepts what this seam produces.
			if err := declaration.Validate(); err != nil {
				t.Fatalf("declaration did not validate: %v", err)
			}
		})
	}
}

// TestSandboxNetworkEgressDeclarationRejectsUnknownDefault pins the fail-closed
// reading of a normalized spec decoded from persisted JSON: the value was not
// re-validated against the schema, so an unknown default must be reported
// rather than guessed into an action.
func TestSandboxNetworkEgressDeclarationRejectsUnknownDefault(t *testing.T) {
	spec := &NormalizedSandboxNetworkSpec{Default: "permit"}
	if _, err := spec.EgressDeclaration(); err == nil {
		t.Fatal("EgressDeclaration accepted an unknown default")
	}
	var nilSpec *NormalizedSandboxNetworkSpec
	declaration, err := nilSpec.EgressDeclaration()
	if err != nil {
		t.Fatalf("EgressDeclaration(nil) returned error: %v", err)
	}
	if declaration.Default != "" || len(declaration.Allow) != 0 {
		t.Fatalf("EgressDeclaration(nil) = %+v, want the zero declaration", declaration)
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
			declaration, err := spec.Agents[0].PolicyBlock().Network.EgressDeclaration()
			if err != nil {
				t.Fatalf("EgressDeclaration() error = %v", err)
			}
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
// declared allow-all value becomes allow: a value the schema does not accept is
// an error rather than a guessed action, so it cannot silently widen access.
func TestEgressActionForSandboxNetworkDefaultFailsClosed(t *testing.T) {
	got, err := egressActionForSandboxNetworkDefault(SandboxNetworkDefaultAllowAll)
	if err != nil {
		t.Fatalf("egressActionForSandboxNetworkDefault(allow-all) error = %v", err)
	}
	if got != egress.Allow {
		t.Fatalf("allow-all mapped to %q, want %q", got, egress.Allow)
	}
	denied, err := egressActionForSandboxNetworkDefault(SandboxNetworkDefaultDenyAll)
	if err != nil {
		t.Fatalf("egressActionForSandboxNetworkDefault(deny) error = %v", err)
	}
	if denied != egress.Deny {
		t.Fatalf("deny-all mapped to %q, want %q", denied, egress.Deny)
	}
	// An omitted default is not an unknown value: normalization maps it to
	// allow-all, so it keeps the declared default instead of being rejected.
	omitted, err := egressActionForSandboxNetworkDefault("")
	if err != nil {
		t.Fatalf("egressActionForSandboxNetworkDefault(\"\") error = %v", err)
	}
	if omitted != egress.Allow {
		t.Fatalf("omitted default mapped to %q, want %q", omitted, egress.Allow)
	}
	for _, value := range []string{"permit", "sideways"} {
		if _, err := egressActionForSandboxNetworkDefault(value); err == nil {
			t.Fatalf("egressActionForSandboxNetworkDefault(%q) returned no error, want a rejection", value)
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
        default: deny-all
        allow:
          - host: api.github.com
            port: 443
            allowed_ips:
              - 203.0.113.0/24
`, nil)
	before := mustHash(t, spec)

	cloned := spec.Redacted()
	cloned.Agents[0].PolicyBlock().Network.Default = SandboxNetworkDefaultAllowAll
	cloned.Agents[0].PolicyBlock().Network.Allow[0].Host = "attacker.example.com"
	// The address list is a slice inside the copied entry, so mutating it in
	// place only stays local if the clone copied it too.
	cloned.Agents[0].PolicyBlock().Network.Allow[0].AllowedIPs[0] = "0.0.0.0/0"
	cloned.Agents[0].PolicyBlock().Network.Allow = append(cloned.Agents[0].PolicyBlock().Network.Allow, NormalizedSandboxNetworkAllowSpec{Host: "extra.example.com", Port: 1})

	if after := mustHash(t, spec); after != before {
		t.Fatalf("hash changed after mutating a returned view: %s != %s", after, before)
	}
}

// TestNormalizeSandboxNetworkAllowedIPs pins the schema half of allowed_ips:
// the declaration carries canonical ranges into the normalized spec, an entry
// the engine cannot interpret is rejected at the field that caused it, and the
// range participates in the spec hash.
func TestNormalizeSandboxNetworkAllowedIPs(t *testing.T) {
	raw := func(allowedIPs string) string {
		return "name: network-egress\nagents:\n  worker:\n    provider: codex\n    sandbox:\n      network:\n        default: deny-all\n        allow:\n          - host: api.example.com\n            port: 443\n" + allowedIPs
	}

	spec := mustNormalizeCompose(t, raw("            allowed_ips:\n              - 203.0.113.7\n              - 10.0.0.5/8\n              - 10.1.2.3/8\n"), nil)
	normalized := spec.Agents[0].PolicyBlock().Network
	if len(normalized.Allow) != 1 {
		t.Fatalf("allow = %+v, want one entry", normalized.Allow)
	}
	want := []string{"10.0.0.0/8", "203.0.113.7/32"}
	if !reflect.DeepEqual(normalized.Allow[0].AllowedIPs, want) {
		t.Fatalf("allowed_ips = %v, want %v", normalized.Allow[0].AllowedIPs, want)
	}
	declaration, err := normalized.EgressDeclaration()
	if err != nil {
		t.Fatalf("EgressDeclaration returned error: %v", err)
	}
	if len(declaration.Allow) != 1 || !reflect.DeepEqual(declaration.Allow[0].AllowedIPs, want) {
		t.Fatalf("declaration = %+v, want the ranges carried into the decision model", declaration.Allow)
	}
	if err := declaration.Validate(); err != nil {
		t.Fatalf("declaration did not validate: %v", err)
	}

	// The same declaration without the range hashes differently: the constraint
	// is part of what a project declared.
	unconstrained := mustNormalizeCompose(t, raw(""), nil)
	if mustHash(t, spec) == mustHash(t, unconstrained) {
		t.Fatal("a declaration with and without allowed_ips hash the same")
	}
	// Order inside the list carries no meaning.
	reordered := mustNormalizeCompose(t, raw("            allowed_ips:\n              - 10.1.2.3/8\n              - 203.0.113.7\n              - 10.0.0.5/8\n"), nil)
	if mustHash(t, spec) != mustHash(t, reordered) {
		t.Fatal("reordering allowed_ips changed the hash")
	}

	_, err = Normalize(mustParseCompose(t, raw("            allowed_ips: [\"api.example.com\"]\n")), NormalizeOptions{})
	if err == nil {
		t.Fatal("a name in allowed_ips was accepted")
	}
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Path != "agents.worker.sandbox.network.allow[0].allowed_ips" {
		t.Fatalf("error = %v, want a validation error at the allowed_ips field, got %+v", err, validation)
	}
}

// TestNormalizeSandboxNetworkAllowBoundsTheList pins the input bound: every
// entry and every allowed address is validated, deduplicated, and compiled
// before a declaration can be refused, so the list is bounded rather than
// unbounded work one request can ask the daemon to do.
func TestNormalizeSandboxNetworkAllowBoundsTheList(t *testing.T) {
	entries := make([]SandboxNetworkAllowSpec, 0, maxSandboxNetworkAllowEntries+1)
	for index := 0; index <= maxSandboxNetworkAllowEntries; index++ {
		entries = append(entries, SandboxNetworkAllowSpec{
			Host: "host-" + strconv.Itoa(index) + ".example.com",
			Port: 443,
		})
	}
	if _, err := normalizeSandboxNetworkAllow("network.allow", entries); err == nil {
		t.Fatal("normalizeSandboxNetworkAllow accepted more entries than the bound")
	}

	addresses := make([]string, 0, maxSandboxNetworkAllowAllowedIPs+1)
	for index := 0; index <= maxSandboxNetworkAllowAllowedIPs; index++ {
		addresses = append(addresses, "10."+strconv.Itoa(index/256%256)+"."+strconv.Itoa(index%256)+".1")
	}
	entry := []SandboxNetworkAllowSpec{{Host: "api.example.com", Port: 443, AllowedIPs: addresses}}
	if _, err := normalizeSandboxNetworkAllow("network.allow", entry); err == nil {
		t.Fatal("normalizeSandboxNetworkAllow accepted more allowed addresses than the bound")
	}
}

func TestNormalizeSandboxNetworkDenyList(t *testing.T) {
	tests := []struct {
		name    string
		network string
		want    *NormalizedSandboxNetworkSpec
	}{
		{
			name: "allow default with denials is denylist mode",
			network: `network:
        deny:
          - host: Metadata.Example.com
            port: 80
          - host: 10.2.16.21
            port: 443`,
			want: &NormalizedSandboxNetworkSpec{
				Default: SandboxNetworkDefaultAllowAll,
				Deny: []NormalizedSandboxNetworkDenySpec{
					{Host: "10.2.16.21", Port: 443},
					{Host: "metadata.example.com", Port: 80},
				},
			},
		},
		{
			name: "deny default can carry both allowances and denials",
			network: `network:
        default: deny-all
        allow:
          - host: "*.example.com"
            port: 443
        deny:
          - host: bad.example.com
            port: 443`,
			want: &NormalizedSandboxNetworkSpec{
				Default: SandboxNetworkDefaultDenyAll,
				Allow: []NormalizedSandboxNetworkAllowSpec{
					{Host: "*.example.com", Port: 443},
				},
				Deny: []NormalizedSandboxNetworkDenySpec{
					{Host: "bad.example.com", Port: 443},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized := mustNormalizeCompose(t, "name: network\nagents:\n  worker:\n    provider: codex\n    policy:\n      "+tc.network+"\n", nil)
			got := normalized.Agents[0].PolicyBlock().Network
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("network = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestNormalizeSandboxNetworkRejectsInvalidDenials(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "unknown field under deny entry",
			body:    "deny:\n          - host: api.example.com\n            port: 443\n            allowed_ips: [10.0.0.0/8]",
			wantErr: "unknown field",
		},
		{
			name:    "missing host",
			body:    "deny:\n          - port: 443",
			wantErr: "host must not be empty",
		},
		{
			name:    "port out of range",
			body:    "deny:\n          - host: api.example.com\n            port: 0",
			wantErr: "port must be between 1 and 65535",
		},
		{
			name:    "duplicate deny entry",
			body:    "deny:\n          - host: api.example.com\n            port: 443\n          - host: API.example.com\n            port: 443",
			wantErr: "duplicate deny entry",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := "name: network\nagents:\n  worker:\n    provider: codex\n    policy:\n      network:\n        " + tc.body + "\n"
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

func TestSandboxNetworkEgressDeclarationCarriesDenials(t *testing.T) {
	spec := mustNormalizeCompose(t, `
name: network-egress-deny
agents:
  worker:
    provider: codex
    policy:
      network:
        deny:
          - host: metadata.example.com
            port: 80
`, nil)
	declaration, err := spec.Agents[0].PolicyBlock().Network.EgressDeclaration()
	if err != nil {
		t.Fatalf("EgressDeclaration returned error: %v", err)
	}
	if declaration.Default != egress.Allow {
		t.Fatalf("default = %q, want %q", declaration.Default, egress.Allow)
	}
	if !declaration.Restricts() {
		t.Fatal("declaration with a denial does not restrict")
	}
	want := []egress.DenyEntry{{Host: "metadata.example.com", Port: 80, Protocol: egress.ProtocolAny}}
	if !reflect.DeepEqual(declaration.Deny, want) {
		t.Fatalf("deny = %+v, want %+v", declaration.Deny, want)
	}
}

func TestSandboxNetworkDenyChangesSpecHash(t *testing.T) {
	base := mustNormalizeCompose(t, `
name: network-hash-denylist
agents:
  worker:
    provider: codex
    policy:
      network: {}`, nil)
	withDeny := mustNormalizeCompose(t, `
name: network-hash-denylist
agents:
  worker:
    provider: codex
    policy:
      network:
        deny:
          - host: metadata.example.com
            port: 80
`, nil)
	if mustHash(t, base) == mustHash(t, withDeny) {
		t.Fatal("a deny entry did not change the spec hash")
	}
}

func TestCloneNormalizedSandboxSpecCopiesDenials(t *testing.T) {
	original := &NormalizedSandboxSpec{Network: &NormalizedSandboxNetworkSpec{
		Default: SandboxNetworkDefaultAllowAll,
		Deny:    []NormalizedSandboxNetworkDenySpec{{Host: "metadata.example.com", Port: 80}},
	}}
	cloned := cloneNormalizedSandboxSpec(original)
	if cloned.Network == original.Network {
		t.Fatal("clone shared the network pointer")
	}
	cloned.Network.Deny[0].Host = "changed.example.com"
	if original.Network.Deny[0].Host != "metadata.example.com" {
		t.Fatalf("mutating the clone changed the original: %+v", original.Network.Deny)
	}
}
