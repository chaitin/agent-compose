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

func TestSandboxInitLandlockEnv(t *testing.T) {
	for _, test := range []struct {
		name   string
		config *appconfig.Config
		want   map[string]string
		ok     bool
	}{
		{name: "nil config", config: nil},
		{name: "off", config: &appconfig.Config{SandboxInitLandlock: "off"}},
		{name: "empty", config: &appconfig.Config{}},
		{
			name:   "enforce with fallback",
			config: &appconfig.Config{SandboxInitLandlock: "enforce", SandboxInitLandlockFallback: "warn"},
			want: map[string]string{
				sandboxinit.LandlockModeEnvVar:     "enforce",
				sandboxinit.LandlockFallbackEnvVar: "warn",
			},
			ok: true,
		},
		{
			name:   "padded enforce",
			config: &appconfig.Config{SandboxInitLandlock: " ENFORCE "},
			want:   map[string]string{sandboxinit.LandlockModeEnvVar: "ENFORCE"},
			ok:     true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := sandboxInitLandlockEnv(test.config)
			if ok != test.ok {
				t.Fatalf("sandboxInitLandlockEnv() ok = %v, want %v", ok, test.ok)
			}
			if len(got) != len(test.want) {
				t.Fatalf("sandboxInitLandlockEnv() = %#v, want %#v", got, test.want)
			}
			for key, want := range test.want {
				if got[key] != want {
					t.Fatalf("sandboxInitLandlockEnv()[%q] = %q, want %q", key, got[key], want)
				}
			}
		})
	}
}

func TestSandboxInitIsolationEnvCombinesEgressAndLandlock(t *testing.T) {
	config := &appconfig.Config{
		SandboxInitEgress:           "audit",
		SandboxInitLandlock:         "enforce",
		SandboxInitLandlockFallback: "fail",
	}
	got := sandboxInitIsolationEnv(config)
	for key, want := range map[string]string{
		sandboxinit.EgressModeEnvVar:       "audit",
		sandboxinit.LandlockModeEnvVar:     "enforce",
		sandboxinit.LandlockFallbackEnvVar: "fail",
	} {
		if got[key] != want {
			t.Fatalf("isolation env[%q] = %q, want %q", key, got[key], want)
		}
	}
	if env := sandboxInitIsolationEnv(nil); len(env) != 0 {
		t.Fatalf("isolation env for nil config = %#v, want empty", env)
	}
}

func TestRequireSandboxInitLandlockCapability(t *testing.T) {
	enforce := &appconfig.Config{SandboxInitLandlock: "enforce", SandboxInitLandlockFallback: "fail"}
	warn := &appconfig.Config{SandboxInitLandlock: "enforce", SandboxInitLandlockFallback: "warn"}
	for _, test := range []struct {
		name    string
		info    sandboxinit.ServerInfo
		config  *appconfig.Config
		wantErr bool
	}{
		{name: "nil config", config: nil},
		{name: "off", info: sandboxinit.ServerInfo{}, config: &appconfig.Config{SandboxInitLandlock: "off"}},
		{name: "enforce absent fails closed", info: sandboxinit.ServerInfo{}, config: enforce, wantErr: true},
		{name: "enforce present", info: sandboxinit.ServerInfo{Capabilities: []string{sandboxinit.CapabilityLandlockEnforce}}, config: enforce},
		{name: "warn absent degrades", info: sandboxinit.ServerInfo{}, config: warn},
		{name: "warn present", info: sandboxinit.ServerInfo{Capabilities: []string{sandboxinit.CapabilityLandlockEnforce}}, config: warn},
		{name: "invalid mode", config: &appconfig.Config{SandboxInitLandlock: "audit"}, wantErr: true},
		{name: "invalid fallback", info: sandboxinit.ServerInfo{}, config: &appconfig.Config{SandboxInitLandlock: "enforce", SandboxInitLandlockFallback: "ignore"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := requireSandboxInitLandlockCapability(test.info, test.config)
			if (err != nil) != test.wantErr {
				t.Fatalf("requireSandboxInitLandlockCapability() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
