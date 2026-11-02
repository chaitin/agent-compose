package driver

import (
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
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
