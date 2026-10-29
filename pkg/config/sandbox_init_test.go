package config

import "testing"

func TestSandboxInitConfigDefaultsOff(t *testing.T) {
	t.Setenv("SANDBOX_INIT_ENABLED", "")
	t.Setenv("SANDBOX_INIT_BINARY", "")
	config, err := loadSandboxInitConfig()
	if err != nil {
		t.Fatalf("loadSandboxInitConfig() error = %v", err)
	}
	if config.Enabled {
		t.Fatal("sandbox init is enabled by default, want opt-in")
	}
	if config.Binary != DefaultSandboxInitBinary {
		t.Fatalf("binary = %q, want %q", config.Binary, DefaultSandboxInitBinary)
	}
}

func TestSandboxInitConfigReadsEnvironment(t *testing.T) {
	t.Setenv("SANDBOX_INIT_ENABLED", "true")
	t.Setenv("SANDBOX_INIT_BINARY", "/opt/agent-compose-init")
	config, err := loadSandboxInitConfig()
	if err != nil {
		t.Fatalf("loadSandboxInitConfig() error = %v", err)
	}
	if !config.Enabled || config.Binary != "/opt/agent-compose-init" {
		t.Fatalf("config = %#v, want enabled with the configured binary", config)
	}
}

func TestSandboxInitConfigRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct {
		name      string
		enabled   string
		binary    string
		wantError bool
	}{
		{name: "invalid bool", enabled: "sometimes", wantError: true},
		{name: "relative binary", enabled: "true", binary: "bin/init", wantError: true},
		{name: "relative binary while disabled", enabled: "false", binary: "bin/init"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SANDBOX_INIT_ENABLED", test.enabled)
			t.Setenv("SANDBOX_INIT_BINARY", test.binary)
			_, err := loadSandboxInitConfig()
			if (err != nil) != test.wantError {
				t.Fatalf("loadSandboxInitConfig() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}
