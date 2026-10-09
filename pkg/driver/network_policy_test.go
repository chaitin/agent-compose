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
	api := mustEndpoint(t, "api.example.com", 443, egress.ProtocolHTTPS)
	dns := mustEndpoint(t, "dns.example.com", 53, egress.ProtocolUDP)
	engine := mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS)

	tests := []struct {
		name        string
		defaultName egress.Action
		allow       []egress.Endpoint
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
			allow:       []egress.Endpoint{api, dns},
			engine:      []egress.EngineEndpoint{engine},
			want: SandboxNetworkPolicy{
				Default:         egress.Deny,
				Allow:           []egress.Endpoint{api, dns},
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
			allow:       []egress.Endpoint{{Host: "  ", Port: 443}},
			wantErr:     true,
		},
		{
			name:        "port out of range is rejected",
			defaultName: egress.Deny,
			allow:       []egress.Endpoint{{Host: "api.example.com", Port: 0}},
			wantErr:     true,
		},
		{
			name:        "unknown protocol is rejected",
			defaultName: egress.Deny,
			allow:       []egress.Endpoint{{Host: "api.example.com", Port: 443, Protocol: "sctp"}},
			wantErr:     true,
		},
		{
			name:        "wildcard host is accepted for allow entries",
			defaultName: egress.Deny,
			allow:       []egress.Endpoint{{Host: "*.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
			want: SandboxNetworkPolicy{
				Default: egress.Deny,
				Allow:   []egress.Endpoint{{Host: "*.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
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
			allow: []egress.Endpoint{
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
		Allow:           []egress.Endpoint{mustEndpoint(t, "api.example.com", 443, egress.ProtocolHTTPS)},
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

func TestSandboxNetworkPolicyPermittingEndpoints(t *testing.T) {
	engine := mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS)
	allow := mustEndpoint(t, "api.example.com", 443, egress.ProtocolHTTPS)
	duplicate := mustEndpoint(t, "API.example.com", 443, egress.ProtocolHTTPS)

	policy := SandboxNetworkPolicy{
		Default:         egress.Deny,
		Allow:           []egress.Endpoint{allow, duplicate},
		EngineEndpoints: []egress.EngineEndpoint{engine},
	}
	got := policy.permittingEndpoints()
	want := []egress.Endpoint{engine.Endpoint, allow}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permittingEndpoints() = %+v, want %+v", got, want)
	}

	permissive := SandboxNetworkPolicy{
		Default:         egress.Allow,
		Allow:           []egress.Endpoint{allow},
		EngineEndpoints: []egress.EngineEndpoint{engine},
	}
	if got := permissive.permittingEndpoints(); got != nil {
		t.Fatalf("permittingEndpoints() for allow = %+v, want nil", got)
	}
}

// TestSandboxNetworkPolicyRejectsMalformedHost pins that a host the egress
// model cannot interpret is rejected rather than passed through.
func TestSandboxNetworkPolicyRejectsMalformedHost(t *testing.T) {
	_, err := NewSandboxNetworkPolicy(egress.Deny, []egress.Endpoint{
		{Host: "bad host.example.com", Port: 443, Protocol: egress.ProtocolHTTPS},
	}, nil, nil)
	if err == nil {
		t.Fatal("NewSandboxNetworkPolicy() error = nil, want a validation error")
	}
}
