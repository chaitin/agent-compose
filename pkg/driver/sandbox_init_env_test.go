package driver

import (
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/egress"
	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

func TestSandboxInitEgressEnv(t *testing.T) {
	for _, test := range []struct {
		name   string
		config *appconfig.Config
		want   string
		ok     bool
	}{
		{name: "nil config", config: nil},
		{name: "off", config: &appconfig.Config{SandboxInitEgress: "off"}},
		{name: "empty", config: &appconfig.Config{}},
		{name: "audit", config: &appconfig.Config{SandboxInitEgress: "audit"}, want: string(sandboxinit.EgressModeAudit), ok: true},
		{name: "padded", config: &appconfig.Config{SandboxInitEgress: " audit "}, want: string(sandboxinit.EgressModeAudit), ok: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := sandboxInitEgressEnv(test.config)
			if ok != test.ok || got != test.want {
				t.Fatalf("sandboxInitEgressEnv() = %q, %v; want %q, %v", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestSandboxInitEgressRequiresPtrace(t *testing.T) {
	for _, test := range []struct {
		name   string
		config *appconfig.Config
		want   bool
	}{
		{name: "nil", config: nil},
		{name: "off", config: &appconfig.Config{SandboxInitEgress: "off"}},
		{name: "audit", config: &appconfig.Config{SandboxInitEgress: "audit"}},
		{name: "enforce", config: &appconfig.Config{SandboxInitEgress: "enforce"}, want: true},
		{name: "padded enforce", config: &appconfig.Config{SandboxInitEgress: " enforce "}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sandboxInitEgressRequiresPtrace(test.config); got != test.want {
				t.Fatalf("sandboxInitEgressRequiresPtrace() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRequireSandboxInitEgressCapabilities(t *testing.T) {
	enforce := &appconfig.Config{SandboxInitEgress: "enforce"}
	restricting := &SandboxNetworkPolicy{Default: egress.Deny, MediatedByForwardProxy: true}
	allowAll := &SandboxNetworkPolicy{Default: egress.Allow}
	for _, test := range []struct {
		name    string
		info    sandboxinit.ServerInfo
		config  *appconfig.Config
		policy  *SandboxNetworkPolicy
		wantErr bool
	}{
		{name: "not enforce", info: sandboxinit.ServerInfo{}, config: &appconfig.Config{SandboxInitEgress: "audit"}, policy: restricting},
		{name: "no restricting policy", info: sandboxinit.ServerInfo{}, config: enforce, policy: allowAll},
		{name: "nil policy", info: sandboxinit.ServerInfo{}, config: enforce},
		{name: "unmediated policy", info: sandboxinit.ServerInfo{}, config: enforce, policy: &SandboxNetworkPolicy{Default: egress.Deny}},
		{name: "enforce missing mediate", info: sandboxinit.ServerInfo{}, config: enforce, policy: restricting, wantErr: true},
		{name: "enforce missing hardening", info: sandboxinit.ServerInfo{Capabilities: []string{sandboxinit.CapabilityEgressMediate}}, config: enforce, policy: restricting, wantErr: true},
		{name: "enforce complete", info: sandboxinit.ServerInfo{Capabilities: []string{sandboxinit.CapabilityEgressMediate, sandboxinit.CapabilityEgressHardening}}, config: enforce, policy: restricting},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := requireSandboxInitEgressCapabilities(test.info, test.config, test.policy)
			if (err != nil) != test.wantErr {
				t.Fatalf("requireSandboxInitEgressCapabilities() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
