package config

import "testing"

func TestSandboxInitConfigDefaultsOff(t *testing.T) {
	t.Setenv("SANDBOX_INIT_ENABLED", "")
	t.Setenv("SANDBOX_INIT_BINARY", "")
	t.Setenv("SANDBOX_INIT_SOCKET_DIR", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
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
	if config.SocketDir != "/run/agent-compose" {
		t.Fatalf("socket dir = %q, want /run/agent-compose", config.SocketDir)
	}
}

func TestSandboxInitConfigDefaultsToTheUserRuntimeDirectory(t *testing.T) {
	t.Setenv("SANDBOX_INIT_ENABLED", "")
	t.Setenv("SANDBOX_INIT_BINARY", "")
	t.Setenv("SANDBOX_INIT_SOCKET_DIR", "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	config, err := loadSandboxInitConfig()
	if err != nil {
		t.Fatalf("loadSandboxInitConfig() error = %v", err)
	}
	if config.SocketDir != "/run/user/1000/agent-compose" {
		t.Fatalf("socket dir = %q, want the XDG runtime directory", config.SocketDir)
	}
}

func TestSandboxInitConfigReadsEnvironment(t *testing.T) {
	t.Setenv("SANDBOX_INIT_ENABLED", "true")
	t.Setenv("SANDBOX_INIT_BINARY", "/opt/ac-init")
	t.Setenv("SANDBOX_INIT_SOCKET_DIR", "/run/ac")
	config, err := loadSandboxInitConfig()
	if err != nil {
		t.Fatalf("loadSandboxInitConfig() error = %v", err)
	}
	if !config.Enabled || config.Binary != "/opt/ac-init" || config.SocketDir != "/run/ac" {
		t.Fatalf("config = %#v, want enabled with the configured binary and socket dir", config)
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
			t.Setenv("SANDBOX_INIT_SOCKET_DIR", "/run/test")
			_, err := loadSandboxInitConfig()
			if (err != nil) != test.wantError {
				t.Fatalf("loadSandboxInitConfig() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}
