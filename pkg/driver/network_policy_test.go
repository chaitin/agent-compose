package driver

import (
	"reflect"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
)

func mustEndpoint(t *testing.T, host string, port int, protocol egress.Protocol) egress.Endpoint {
	t.Helper()
	endpoint, err := egress.NewEndpoint(host, port, protocol)
	if err != nil {
		t.Fatalf("egress.NewEndpoint(%q, %d, %q) error = %v", host, port, protocol, err)
	}
	return endpoint
}

// mustAllow is mustEndpoint's counterpart for an allowance: the two are
// different types on purpose, because an allowance carries the addresses its
// name may resolve to and an endpoint does not.
func mustAllow(t *testing.T, host string, port int, protocol egress.Protocol) egress.AllowEntry {
	t.Helper()
	endpoint := mustEndpoint(t, host, port, protocol)
	return egress.AllowEntry{Host: endpoint.Host, Port: endpoint.Port, Protocol: endpoint.Protocol}
}

func mustEngineEndpoint(t *testing.T, purpose egress.EndpointPurpose, host string, port int, protocol egress.Protocol) egress.EngineEndpoint {
	t.Helper()
	endpoint := egress.EngineEndpoint{
		Purpose:  purpose,
		Endpoint: mustEndpoint(t, host, port, protocol),
		Source:   egress.SourceRuntimeBaseURL,
	}
	if err := endpoint.Validate(); err != nil {
		t.Fatalf("EngineEndpoint.Validate() error = %v", err)
	}
	return endpoint
}

func TestNewSandboxNetworkPolicyNormalizesAndValidates(t *testing.T) {
	api := mustAllow(t, "api.example.com", 443, egress.ProtocolHTTPS)
	dns := mustAllow(t, "dns.example.com", 53, egress.ProtocolUDP)
	engine := mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS)

	tests := []struct {
		name        string
		defaultName egress.Action
		allow       []egress.AllowEntry
		engine      []egress.EngineEndpoint
		denyDomains []string
		want        SandboxNetworkPolicy
		wantErr     bool
	}{
		{
			name:        "omitted default normalizes to allow",
			defaultName: "",
			want:        SandboxNetworkPolicy{Default: egress.Allow},
		},
		{
			name:        "deny keeps ordered entries",
			defaultName: egress.Deny,
			allow:       []egress.AllowEntry{api, dns},
			engine:      []egress.EngineEndpoint{engine},
			want: SandboxNetworkPolicy{
				Default:         egress.Deny,
				Allow:           []egress.AllowEntry{api, dns},
				EngineEndpoints: []egress.EngineEndpoint{engine},
			},
		},
		{
			name:        "deny domains are lowercased deduped and sorted",
			defaultName: egress.Deny,
			denyDomains: []string{"Evil.example.com", "ads.example.com", "evil.example.com"},
			want: SandboxNetworkPolicy{
				Default:     egress.Deny,
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
			defaultName: egress.Deny,
			allow:       []egress.AllowEntry{{Host: "  ", Port: 443}},
			wantErr:     true,
		},
		{
			name:        "port out of range is rejected",
			defaultName: egress.Deny,
			allow:       []egress.AllowEntry{{Host: "api.example.com", Port: 0}},
			wantErr:     true,
		},
		{
			name:        "unknown protocol is rejected",
			defaultName: egress.Deny,
			allow:       []egress.AllowEntry{{Host: "api.example.com", Port: 443, Protocol: "sctp"}},
			wantErr:     true,
		},
		{
			name:        "wildcard host is accepted for allow entries",
			defaultName: egress.Deny,
			allow:       []egress.AllowEntry{{Host: "*.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
			want: SandboxNetworkPolicy{
				Default: egress.Deny,
				Allow:   []egress.AllowEntry{{Host: "*.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
			},
		},
		{
			name:        "wildcard is rejected for deny domains",
			defaultName: egress.Deny,
			denyDomains: []string{"*.example.com"},
			wantErr:     true,
		},
		{
			name:        "duplicate allow entries are rejected",
			defaultName: egress.Deny,
			allow: []egress.AllowEntry{
				{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolTCP},
				{Host: "API.example.com", Port: 443, Protocol: egress.ProtocolTCP},
			},
			wantErr: true,
		},
		{
			name:        "engine endpoint without a source is rejected",
			defaultName: egress.Deny,
			engine: []egress.EngineEndpoint{{
				Purpose:  egress.PurposeLLMFacade,
				Endpoint: egress.Endpoint{Host: "facade.internal", Port: 7410, Protocol: egress.ProtocolHTTPS},
			}},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, err := NewSandboxNetworkPolicy(test.defaultName, test.allow, nil, test.engine, test.denyDomains)
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

// TestSandboxNetworkPolicyFromDeclarationIsTheComposeSeam pins the mapping the
// compose layer uses: a declaration plus engine endpoints becomes the
// driver-boundary policy, and a nil declaration is the D3 pass-through.
func TestSandboxNetworkPolicyFromDeclarationIsTheComposeSeam(t *testing.T) {
	engine := mustEngineEndpoint(t, egress.PurposeTelemetry, "telemetry.internal", 4318, egress.ProtocolHTTP)
	declaration := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow: []egress.AllowEntry{
			{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS},
		},
	}
	policy, err := SandboxNetworkPolicyFromDeclaration(declaration, []egress.EngineEndpoint{engine}, []string{"ads.example.com"})
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
	}
	want := SandboxNetworkPolicy{
		Default:         egress.Deny,
		Allow:           []egress.AllowEntry{mustAllow(t, "api.example.com", 443, egress.ProtocolHTTPS)},
		EngineEndpoints: []egress.EngineEndpoint{engine},
		DenyDomains:     []string{"ads.example.com"},
	}
	if !reflect.DeepEqual(policy, want) {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() = %+v, want %+v", policy, want)
	}

	passThrough, err := SandboxNetworkPolicyFromDeclaration(nil, []egress.EngineEndpoint{engine}, nil)
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration(nil) error = %v", err)
	}
	if passThrough.DenyByDefault() {
		t.Fatalf("nil declaration produced a deny policy: %+v", passThrough)
	}
	if _, err := SandboxNetworkEgressPolicy(passThrough); err != nil {
		t.Fatalf("SandboxNetworkEgressPolicy(pass-through) error = %v", err)
	}
}

func TestSandboxNetworkPolicyDenyByDefault(t *testing.T) {
	tests := []struct {
		name   string
		policy SandboxNetworkPolicy
		want   bool
	}{
		{name: "deny", policy: SandboxNetworkPolicy{Default: egress.Deny}, want: true},
		{name: "allow", policy: SandboxNetworkPolicy{Default: egress.Allow}, want: false},
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

func TestSandboxNetworkPolicyEndpointOrdering(t *testing.T) {
	engine := mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS)
	allow := mustAllow(t, "api.example.com", 443, egress.ProtocolHTTPS)
	deny := egress.DenyEntry{Host: "bad.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}

	policy := SandboxNetworkPolicy{
		Default:         egress.Deny,
		Allow:           []egress.AllowEntry{allow},
		Deny:            []egress.DenyEntry{deny},
		EngineEndpoints: []egress.EngineEndpoint{engine},
	}
	if got, want := policy.engineEndpoints(), []egress.Endpoint{engine.Endpoint}; !reflect.DeepEqual(got, want) {
		t.Fatalf("engineEndpoints() = %+v, want %+v", got, want)
	}
	if got, want := policy.allowingEndpoints(), []egress.Endpoint{{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allowingEndpoints() = %+v, want %+v", got, want)
	}
	if got, want := policy.denyingEndpoints(), []egress.Endpoint{{Host: "bad.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("denyingEndpoints() = %+v, want %+v", got, want)
	}

	denyList := SandboxNetworkPolicy{
		Default:         egress.Allow,
		Allow:           []egress.AllowEntry{allow},
		Deny:            []egress.DenyEntry{deny},
		EngineEndpoints: []egress.EngineEndpoint{engine},
	}
	if got := denyList.allowingEndpoints(); got != nil {
		t.Fatalf("allowingEndpoints() for a denylist = %+v, want nil (an allowance cannot narrow an allow default)", got)
	}
	if got, want := denyList.engineEndpoints(), []egress.Endpoint{engine.Endpoint}; !reflect.DeepEqual(got, want) {
		t.Fatalf("engineEndpoints() for a denylist = %+v, want %+v", got, want)
	}

	permissive := SandboxNetworkPolicy{Default: egress.Allow, EngineEndpoints: []egress.EngineEndpoint{engine}}
	if got := permissive.engineEndpoints(); got != nil {
		t.Fatalf("engineEndpoints() for a permissive policy = %+v, want nil", got)
	}
}

// TestSandboxNetworkPolicyRejectsMalformedHost pins that a host the egress
// model cannot interpret is rejected rather than passed through.
func TestSandboxNetworkPolicyRejectsMalformedHost(t *testing.T) {
	_, err := NewSandboxNetworkPolicy(egress.Deny, []egress.AllowEntry{
		{Host: "bad host.example.com", Port: 443, Protocol: egress.ProtocolHTTPS},
	}, nil, nil, nil)
	if err == nil {
		t.Fatal("NewSandboxNetworkPolicy() error = nil, want a validation error")
	}
}

func TestSandboxNetworkPolicyCarriesAndNormalizesDenials(t *testing.T) {
	policy, err := SandboxNetworkPolicyFromDeclaration(&egress.NetworkDeclaration{
		Default: egress.Allow,
		Deny: []egress.DenyEntry{
			{Host: "Metadata.Example.com", Port: 80, Protocol: egress.ProtocolTCP},
			{Host: "10.2.16.21", Port: 443, Protocol: egress.ProtocolHTTPS},
		},
	}, []egress.EngineEndpoint{
		mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS),
	}, nil)
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
	}
	if policy.DenyByDefault() {
		t.Fatal("denylist policy reported deny-by-default")
	}
	if !policy.Restricts() {
		t.Fatal("denylist policy does not report that it restricts")
	}
	want := []egress.DenyEntry{
		{Host: "metadata.example.com", Port: 80, Protocol: egress.ProtocolTCP},
		{Host: "10.2.16.21", Port: 443, Protocol: egress.ProtocolHTTPS},
	}
	if !reflect.DeepEqual(policy.Deny, want) {
		t.Fatalf("Deny = %+v, want %+v", policy.Deny, want)
	}
	if got := policy.engineEndpoints(); len(got) != 1 || got[0].Host != "facade.internal" {
		t.Fatalf("engineEndpoints() = %+v, want the engine endpoint", got)
	}
}

func TestNewSandboxNetworkPolicyRejectsInvalidDenials(t *testing.T) {
	for _, entry := range []egress.DenyEntry{
		{Host: "", Port: 443},
		{Host: "api.example.com", Port: 0},
		{Host: "api.example.com", Port: 443, Protocol: "sctp"},
	} {
		if _, err := NewSandboxNetworkPolicy(egress.Allow, nil, []egress.DenyEntry{entry}, nil, nil); err == nil {
			t.Fatalf("NewSandboxNetworkPolicy accepted invalid denial %+v", entry)
		}
	}
	if _, err := NewSandboxNetworkPolicy(egress.Allow, nil, []egress.DenyEntry{
		{Host: "API.example.com", Port: 443, Protocol: egress.ProtocolTCP},
		{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolTCP},
	}, nil, nil); err == nil {
		t.Fatal("NewSandboxNetworkPolicy accepted duplicate denials")
	}
}

func TestSandboxNetworkPolicyFromLayeredDeclarationAcceptsSharedDenial(t *testing.T) {
	// The daemon and the agent may both deny the same endpoint. Layering
	// collapses the duplicate, and the driver compiler must accept the result
	// instead of failing sandbox creation with a duplicate-entry error.
	deny := egress.DenyEntry{Host: "metadata.example.com", Port: 80, Protocol: egress.ProtocolAny}
	outer := &egress.NetworkDeclaration{Default: egress.Allow, Deny: []egress.DenyEntry{deny}}
	inner := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow:   []egress.AllowEntry{{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolAny}},
		Deny:    []egress.DenyEntry{deny},
	}
	layered, err := egress.LayerNetworkDeclarations(outer, inner)
	if err != nil {
		t.Fatalf("LayerNetworkDeclarations() error = %v", err)
	}
	policy, err := SandboxNetworkPolicyFromDeclaration(layered, nil, nil)
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
	}
	if len(policy.Deny) != 1 {
		t.Fatalf("deny = %+v, want the shared denial once", policy.Deny)
	}
}
