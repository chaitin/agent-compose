package config

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/samber/do/v2"
)

func newDockerSandboxNetworkTestConfig(t *testing.T) (*Config, error) {
	t.Helper()
	t.Setenv("DATA_ROOT", t.TempDir())
	di := do.New()
	do.ProvideValue(di, slog.Default())
	return NewConfig(di)
}

func TestNewConfigLoadsDockerSandboxNetworkStrict(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset keeps the profile off", value: "", want: false},
		{name: "explicit true", value: "true", want: true},
		{name: "explicit false", value: "false", want: false},
		{name: "case and surrounding space are tolerated", value: " TRUE ", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(DockerSandboxNetworkStrictEnvName, test.value)
			config, err := newDockerSandboxNetworkTestConfig(t)
			if err != nil {
				t.Fatalf("NewConfig returned error: %v", err)
			}
			if config.DockerSandboxNetworkStrict != test.want {
				t.Fatalf("DockerSandboxNetworkStrict = %v, want %v", config.DockerSandboxNetworkStrict, test.want)
			}
		})
	}
}

func TestNewConfigRejectsInvalidDockerSandboxNetworkStrict(t *testing.T) {
	t.Setenv(DockerSandboxNetworkStrictEnvName, "yes")
	if _, err := newDockerSandboxNetworkTestConfig(t); err == nil || !strings.Contains(err.Error(), DockerSandboxNetworkStrictEnvName) {
		t.Fatalf("NewConfig error = %v, want a %s parse error", err, DockerSandboxNetworkStrictEnvName)
	}
}
