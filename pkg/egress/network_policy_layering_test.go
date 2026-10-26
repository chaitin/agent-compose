package egress

import (
	"reflect"
	"testing"
)

func TestLayerNetworkDeclarations(t *testing.T) {
	globalAllowlist := &NetworkDeclaration{
		Default: Deny,
		Allow: []AllowEntry{
			{Host: "*.example.com", Port: 443, Protocol: ProtocolAny},
			{Host: "10.0.0.0/8", Port: 80, Protocol: ProtocolAny},
		},
	}
	agentAllowlist := &NetworkDeclaration{
		Default: Deny,
		Allow: []AllowEntry{
			{Host: "api.example.com", Port: 443, Protocol: ProtocolAny},
			{Host: "10.2.16.21", Port: 80, Protocol: ProtocolAny},
		},
	}
	tests := []struct {
		name         string
		outer, inner *NetworkDeclaration
		want         *NetworkDeclaration
		wantErr      bool
	}{
		{
			name:  "no daemon policy leaves the agent declaration unchanged",
			inner: agentAllowlist,
			want:  agentAllowlist,
		},
		{
			name:  "no agent policy leaves the daemon declaration unchanged",
			outer: globalAllowlist,
			want:  globalAllowlist,
		},
		{
			name:  "daemon allowlist contains agent allowlist",
			outer: globalAllowlist,
			inner: agentAllowlist,
			want:  agentAllowlist,
		},
		{
			name:    "agent allow entry outside daemon allowlist is rejected",
			outer:   globalAllowlist,
			inner:   &NetworkDeclaration{Default: Deny, Allow: []AllowEntry{{Host: "other.example.net", Port: 443, Protocol: ProtocolAny}}},
			wantErr: true,
		},
		{
			name:  "daemon allowlist and agent denylist",
			outer: globalAllowlist,
			inner: &NetworkDeclaration{Default: Allow, Deny: []DenyEntry{{Host: "bad.example.com", Port: 443, Protocol: ProtocolAny}}},
			want: &NetworkDeclaration{
				Default: Deny,
				Allow:   globalAllowlist.Allow,
				Deny:    []DenyEntry{{Host: "bad.example.com", Port: 443, Protocol: ProtocolAny}},
			},
		},
		{
			name:  "daemon denylist and agent allowlist",
			outer: &NetworkDeclaration{Default: Allow, Deny: []DenyEntry{{Host: "blocked.example.com", Port: 443, Protocol: ProtocolAny}}},
			inner: agentAllowlist,
			want: &NetworkDeclaration{
				Default: Deny,
				Allow:   agentAllowlist.Allow,
				Deny:    []DenyEntry{{Host: "blocked.example.com", Port: 443, Protocol: ProtocolAny}},
			},
		},
		{
			name:  "two denylists union their denials",
			outer: &NetworkDeclaration{Default: Allow, Deny: []DenyEntry{{Host: "outer.example", Port: 443, Protocol: ProtocolAny}}},
			inner: &NetworkDeclaration{Default: Allow, Deny: []DenyEntry{{Host: "inner.example", Port: 443, Protocol: ProtocolAny}}},
			want: &NetworkDeclaration{
				Default: Allow,
				Deny: []DenyEntry{
					{Host: "outer.example", Port: 443, Protocol: ProtocolAny},
					{Host: "inner.example", Port: 443, Protocol: ProtocolAny},
				},
			},
		},
		{
			name:  "agent allow-all cannot widen a daemon allowlist",
			outer: globalAllowlist,
			inner: &NetworkDeclaration{Default: Allow, Allow: []AllowEntry{{Host: "other.example.net", Port: 443, Protocol: ProtocolAny}}},
			want:  &NetworkDeclaration{Default: Deny, Allow: globalAllowlist.Allow},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LayerNetworkDeclarations(tt.outer, tt.inner)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("LayerNetworkDeclarations() = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("LayerNetworkDeclarations() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("LayerNetworkDeclarations() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLayerNetworkDeclarationsDeduplicatesSharedDenials(t *testing.T) {
	// A daemon and an agent can both deny the same endpoint. The effective
	// policy is unambiguous, and the driver boundary rejects duplicate denials,
	// so layering must collapse the shared entry instead of failing the sandbox.
	deny := DenyEntry{Host: "metadata.example.com", Port: 80, Protocol: ProtocolAny}
	outer := &NetworkDeclaration{Default: Allow, Deny: []DenyEntry{deny}}
	inner := &NetworkDeclaration{
		Default: Deny,
		Allow:   []AllowEntry{{Host: "api.example.com", Port: 443, Protocol: ProtocolAny}},
		Deny:    []DenyEntry{deny},
	}
	layered, err := LayerNetworkDeclarations(outer, inner)
	if err != nil {
		t.Fatalf("LayerNetworkDeclarations() error = %v", err)
	}
	if len(layered.Deny) != 1 || !reflect.DeepEqual(layered.Deny[0], deny) {
		t.Fatalf("layered deny = %+v, want the shared denial once", layered.Deny)
	}
}

func TestLayerNetworkDeclarationsCarriesOuterAllowedIPs(t *testing.T) {
	outer := &NetworkDeclaration{Default: Deny, Allow: []AllowEntry{{
		Host: "api.example.com", Port: 443, Protocol: ProtocolAny, AllowedIPs: []string{"203.0.113.0/24"},
	}}}
	inner := &NetworkDeclaration{Default: Deny, Allow: []AllowEntry{{
		Host: "api.example.com", Port: 443, Protocol: ProtocolAny,
	}}}
	got, err := LayerNetworkDeclarations(outer, inner)
	if err != nil {
		t.Fatalf("LayerNetworkDeclarations() error = %v", err)
	}
	if !reflect.DeepEqual(got.Allow[0].AllowedIPs, []string{"203.0.113.0/24"}) {
		t.Fatalf("effective allowed_ips = %v, want the daemon bound", got.Allow[0].AllowedIPs)
	}

	inner.Allow[0].AllowedIPs = []string{"198.51.100.0/24"}
	if _, err := LayerNetworkDeclarations(outer, inner); err == nil {
		t.Fatal("an agent allowed_ips outside the daemon range was accepted")
	}
}

func TestHostCoveredBy(t *testing.T) {
	tests := []struct {
		inner, outer string
		want         bool
	}{
		{"api.example.com", "*.example.com", true},
		{"*.example.com", "*.example.com", true},
		{"*.example.com", "api.example.com", false},
		{"a.b.example.com", "*.example.com", false},
		{"10.2.16.21", "10.0.0.0/8", true},
		{"10.0.0.0/16", "10.0.0.0/8", true},
		{"10.0.0.0/8", "10.0.0.0/16", false},
		{"2001:db8::1", "2001:db8::/32", true},
		{"2001:db8::/48", "2001:db8::/32", true},
	}
	for _, tt := range tests {
		if got := hostCoveredBy(tt.inner, tt.outer); got != tt.want {
			t.Fatalf("hostCoveredBy(%q, %q) = %v, want %v", tt.inner, tt.outer, got, tt.want)
		}
	}
}
