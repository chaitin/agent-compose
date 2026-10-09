package driver

import (
	"reflect"
	"testing"
)

func TestNewSandboxNetworkPolicyNormalizesAndValidates(t *testing.T) {
	tests := []struct {
		name        string
		defaultName string
		allow       []SandboxNetworkEntry
		engine      []SandboxNetworkEntry
		denyDomains []string
		want        SandboxNetworkPolicy
		wantErr     bool
	}{
		{
			name:        "omitted default normalizes to allow-all",
			defaultName: "",
			want:        SandboxNetworkPolicy{Default: SandboxNetworkDefaultAllowAll},
		},
		{
			name:        "deny with ordered entries normalizes case and protocol",
			defaultName: "DENY",
			allow: []SandboxNetworkEntry{
				{Host: "API.Example.COM", Port: 443, Protocol: ""},
				{Host: "dns.example.com", Port: 53, Protocol: "UDP"},
			},
			engine: []SandboxNetworkEntry{{Host: "facade.internal", Port: 7410, Protocol: "HTTP"}},
			want: SandboxNetworkPolicy{
				Default: SandboxNetworkDefaultDeny,
				Allow: []SandboxNetworkEntry{
					{Host: "api.example.com", Port: 443, Protocol: SandboxNetworkProtocolAny},
					{Host: "dns.example.com", Port: 53, Protocol: SandboxNetworkProtocolUDP},
				},
				EngineEndpoints: []SandboxNetworkEntry{
					{Host: "facade.internal", Port: 7410, Protocol: SandboxNetworkProtocolHTTP},
				},
			},
		},
		{
			name:        "deny domains are lowercased deduped and sorted",
			defaultName: "deny",
			denyDomains: []string{"Evil.example.com", "ads.example.com", "evil.example.com"},
			want: SandboxNetworkPolicy{
				Default:     SandboxNetworkDefaultDeny,
				DenyDomains: []string{"ads.example.com", "evil.example.com"},
			},
		},
		{
			name:        "unknown default is rejected",
			defaultName: "maybe",
			wantErr:     true,
		},
		{
			name:        "empty host is rejected",
			defaultName: "deny",
			allow:       []SandboxNetworkEntry{{Host: "  ", Port: 443}},
			wantErr:     true,
		},
		{
			name:        "port out of range is rejected",
			defaultName: "deny",
			allow:       []SandboxNetworkEntry{{Host: "api.example.com", Port: 0}},
			wantErr:     true,
		},
		{
			name:        "wildcard host is accepted for allow entries",
			defaultName: "deny",
			allow:       []SandboxNetworkEntry{{Host: "*.example.com", Port: 443, Protocol: "https"}},
			want: SandboxNetworkPolicy{
				Default: SandboxNetworkDefaultDeny,
				Allow:   []SandboxNetworkEntry{{Host: "*.example.com", Port: 443, Protocol: SandboxNetworkProtocolHTTPS}},
			},
		},
		{
			name:        "wildcard is rejected for deny domains",
			defaultName: "deny",
			denyDomains: []string{"*.example.com"},
			wantErr:     true,
		},
		{
			name:        "duplicate allow entries are rejected",
			defaultName: "deny",
			allow: []SandboxNetworkEntry{
				{Host: "api.example.com", Port: 443, Protocol: "tcp"},
				{Host: "API.example.com", Port: 443, Protocol: "TCP"},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, err := NewSandboxNetworkPolicy(test.defaultName, test.allow, test.engine, test.denyDomains)
			if test.wantErr {
				if err == nil {
					t.Fatalf("NewSandboxNetworkPolicy() error = nil, want an error; policy = %+v", policy)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewSandboxNetworkPolicy() error = %v", err)
			}
			if !reflect.DeepEqual(policy, test.want) {
				t.Fatalf("NewSandboxNetworkPolicy() = %+v, want %+v", policy, test.want)
			}
			if err := policy.Validate(); err != nil {
				t.Fatalf("Validate() after construction error = %v", err)
			}
		})
	}
}

func TestSandboxNetworkPolicyDenyByDefault(t *testing.T) {
	tests := []struct {
		name   string
		policy SandboxNetworkPolicy
		want   bool
	}{
		{name: "deny", policy: SandboxNetworkPolicy{Default: SandboxNetworkDefaultDeny}, want: true},
		{name: "allow-all", policy: SandboxNetworkPolicy{Default: SandboxNetworkDefaultAllowAll}, want: false},
		{name: "zero value is not deny", policy: SandboxNetworkPolicy{}, want: false},
		{name: "unknown default is denied", policy: SandboxNetworkPolicy{Default: "sideways"}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.policy.DenyByDefault(); got != test.want {
				t.Fatalf("DenyByDefault() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSandboxNetworkPolicyPermittingEntries(t *testing.T) {
	engine := SandboxNetworkEntry{Host: "facade.internal", Port: 7410, Protocol: SandboxNetworkProtocolHTTPS}
	allow := SandboxNetworkEntry{Host: "api.example.com", Port: 443, Protocol: SandboxNetworkProtocolHTTPS}
	duplicate := SandboxNetworkEntry{Host: "api.example.com", Port: 443, Protocol: SandboxNetworkProtocolHTTPS}

	policy := SandboxNetworkPolicy{
		Default:         SandboxNetworkDefaultDeny,
		Allow:           []SandboxNetworkEntry{allow, duplicate},
		EngineEndpoints: []SandboxNetworkEntry{engine},
	}
	got := policy.permittingEntries()
	want := []SandboxNetworkEntry{engine, allow}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permittingEntries() = %+v, want %+v", got, want)
	}

	permissive := SandboxNetworkPolicy{
		Default:         SandboxNetworkDefaultAllowAll,
		Allow:           []SandboxNetworkEntry{allow},
		EngineEndpoints: []SandboxNetworkEntry{engine},
	}
	if got := permissive.permittingEntries(); got != nil {
		t.Fatalf("permittingEntries() for allow-all = %+v, want nil", got)
	}
}

func TestSandboxNetworkEntryName(t *testing.T) {
	tests := []struct {
		name  string
		entry SandboxNetworkEntry
		want  string
	}{
		{name: "omitted protocol is any", entry: SandboxNetworkEntry{Host: "API.Example.com", Port: 443}, want: "api.example.com:443/any"},
		{name: "explicit tcp", entry: SandboxNetworkEntry{Host: "api.example.com", Port: 22, Protocol: " TCP "}, want: "api.example.com:22/tcp"},
		{name: "unknown protocol normalizes to any", entry: SandboxNetworkEntry{Host: "api.example.com", Port: 1, Protocol: "sctp"}, want: "api.example.com:1/any"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.entry.Name(); got != test.want {
				t.Fatalf("Name() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestSandboxNetworkPolicyDenyFailsClosedOnMalformedHost pins that a host the
// engine cannot interpret is rejected rather than passed through, so a typo
// never widens access.
func TestSandboxNetworkPolicyDenyFailsClosedOnMalformedHost(t *testing.T) {
	_, err := NewSandboxNetworkPolicy("deny", []SandboxNetworkEntry{
		{Host: "bad host.example.com", Port: 443, Protocol: "https"},
	}, nil, nil)
	if err == nil {
		t.Fatal("NewSandboxNetworkPolicy() error = nil, want a validation error")
	}
}
