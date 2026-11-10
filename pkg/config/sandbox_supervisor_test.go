package config

import (
	"strings"
	"testing"
)

func TestLoadSandboxSupervisorConfig(t *testing.T) {
	tests := map[string]struct {
		initEnabled bool
		egress      string
		supervisor  string
		binary      string
		want        bool
		wantErr     string
	}{
		"default off":                   {want: false},
		"enforce enables the mode":      {initEnabled: true, egress: "enforce", supervisor: "enforce", want: true},
		"case and whitespace trimmed":   {initEnabled: true, egress: "enforce", supervisor: "  Enforce ", want: true},
		"false alias":                   {initEnabled: true, egress: "enforce", supervisor: "false", want: false},
		"requires init":                 {egress: "enforce", supervisor: "enforce", wantErr: "SANDBOX_INIT_ENABLED"},
		"requires enforce egress":       {initEnabled: true, egress: "audit", supervisor: "enforce", wantErr: "SANDBOX_INIT_EGRESS"},
		"rejects an unknown mode":       {initEnabled: true, egress: "enforce", supervisor: "sometimes", wantErr: "SANDBOX_SUPERVISOR"},
		"binary without the mode":       {binary: "/app/ac-supervisor", wantErr: "SANDBOX_SUPERVISOR_BINARY"},
		"binary must be absolute":       {initEnabled: true, egress: "enforce", supervisor: "enforce", binary: "ac-supervisor", wantErr: "absolute"},
		"absolute binary with the mode": {initEnabled: true, egress: "enforce", supervisor: "enforce", binary: "/app/ac-supervisor", want: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SANDBOX_INIT_ENABLED", boolString(test.initEnabled))
			t.Setenv("SANDBOX_INIT_SOCKET_DIR", "/run/ac")
			t.Setenv("SANDBOX_INIT_EGRESS", test.egress)
			t.Setenv("SANDBOX_SUPERVISOR", test.supervisor)
			t.Setenv("SANDBOX_SUPERVISOR_BINARY", test.binary)
			config, err := loadSandboxInitConfig()
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("loadSandboxInitConfig() error = %v, want one containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadSandboxInitConfig() error = %v", err)
			}
			if config.Supervisor != test.want {
				t.Fatalf("Supervisor = %v, want %v", config.Supervisor, test.want)
			}
			if test.binary != "" && config.SupervisorBinary != test.binary {
				t.Fatalf("SupervisorBinary = %q, want %q", config.SupervisorBinary, test.binary)
			}
		})
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestSandboxSupervisorIdentityReadsAndValidates(t *testing.T) {
	t.Setenv("SANDBOX_INIT_ENABLED", "true")
	t.Setenv("SANDBOX_INIT_SOCKET_DIR", "/run/ac")
	t.Setenv("SANDBOX_INIT_EGRESS", "enforce")
	t.Setenv("SANDBOX_SUPERVISOR", "enforce")
	t.Setenv("SANDBOX_SUPERVISOR_UID", "1000")
	t.Setenv("SANDBOX_SUPERVISOR_GID", "1000")
	config, err := loadSandboxInitConfig()
	if err != nil {
		t.Fatalf("loadSandboxInitConfig() error = %v", err)
	}
	if config.SupervisorUID != 1000 || config.SupervisorGID != 1000 {
		t.Fatalf("supervisor identity = %d/%d, want 1000/1000", config.SupervisorUID, config.SupervisorGID)
	}
	t.Setenv("SANDBOX_SUPERVISOR_GID", "")
	if _, err := loadSandboxInitConfig(); err == nil {
		t.Fatal("loadSandboxInitConfig() accepted uid without gid")
	}
}
