package config

import (
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

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

func TestSandboxInitConfigRejectsAnOverlongRuntimeRoot(t *testing.T) {
	t.Setenv("SANDBOX_INIT_ENABLED", "true")
	t.Setenv("SANDBOX_INIT_SOCKET_DIR", "/"+strings.Repeat("x", MaxSandboxInitSocketPathLength))
	if _, err := loadSandboxInitConfig(); err == nil {
		t.Fatal("loadSandboxInitConfig accepted a runtime root whose worst-case socket exceeds the limit")
	}
	t.Setenv("SANDBOX_INIT_ENABLED", "false")
	if _, err := loadSandboxInitConfig(); err != nil {
		t.Fatalf("loadSandboxInitConfig rejected the path while init is disabled: %v", err)
	}
}

func TestSandboxInitEgressDefaultsOff(t *testing.T) {
	t.Setenv("SANDBOX_INIT_EGRESS", "")
	config, err := loadSandboxInitConfig()
	if err != nil {
		t.Fatalf("loadSandboxInitConfig() error = %v", err)
	}
	if config.Egress != sandboxinit.EgressModeOff {
		t.Fatalf("egress = %q, want off", config.Egress)
	}
}

func TestSandboxInitEgressReadsAndValidatesTheMode(t *testing.T) {
	for _, test := range []struct {
		raw       string
		want      sandboxinit.EgressMode
		wantError bool
	}{
		{raw: "audit", want: sandboxinit.EgressModeAudit},
		{raw: " AUDIT ", want: sandboxinit.EgressModeAudit},
		{raw: "off", want: sandboxinit.EgressModeOff},
		{raw: "enforce", want: sandboxinit.EgressModeEnforce},
		{raw: "observe", wantError: true},
	} {
		t.Run(test.raw, func(t *testing.T) {
			t.Setenv("SANDBOX_INIT_EGRESS", test.raw)
			config, err := loadSandboxInitConfig()
			if test.wantError {
				if err == nil {
					t.Fatal("loadSandboxInitConfig() accepted an invalid egress mode")
				}
				return
			}
			if err != nil {
				t.Fatalf("loadSandboxInitConfig() error = %v", err)
			}
			if config.Egress != test.want {
				t.Fatalf("egress = %q, want %q", config.Egress, test.want)
			}
		})
	}
}

func TestSandboxInitLandlockDefaultsOffAndFailsClosed(t *testing.T) {
	t.Setenv("SANDBOX_INIT_LANDLOCK", "")
	t.Setenv("SANDBOX_INIT_LANDLOCK_FALLBACK", "")
	config, err := loadSandboxInitConfig()
	if err != nil {
		t.Fatalf("loadSandboxInitConfig() error = %v", err)
	}
	if config.Landlock != sandboxinit.LandlockModeOff {
		t.Fatalf("landlock = %q, want off", config.Landlock)
	}
	if config.LandlockFallback != sandboxinit.LandlockFallbackFail {
		t.Fatalf("landlock fallback = %q, want fail", config.LandlockFallback)
	}
}

func TestSandboxInitLandlockReadsAndValidatesTheMode(t *testing.T) {
	for _, test := range []struct {
		mode         string
		fallback     string
		want         sandboxinit.LandlockMode
		wantFallback sandboxinit.LandlockFallback
		wantError    bool
	}{
		{mode: "off", fallback: "", want: sandboxinit.LandlockModeOff, wantFallback: sandboxinit.LandlockFallbackFail},
		{mode: "enforce", fallback: "fail", want: sandboxinit.LandlockModeEnforce, wantFallback: sandboxinit.LandlockFallbackFail},
		{mode: " ENFORCE ", fallback: " WARN ", want: sandboxinit.LandlockModeEnforce, wantFallback: sandboxinit.LandlockFallbackWarn},
		{mode: "audit", fallback: "fail", wantError: true},
		{mode: "enforce", fallback: "ignore", wantError: true},
	} {
		t.Run(test.mode+"/"+test.fallback, func(t *testing.T) {
			t.Setenv("SANDBOX_INIT_LANDLOCK", test.mode)
			t.Setenv("SANDBOX_INIT_LANDLOCK_FALLBACK", test.fallback)
			config, err := loadSandboxInitConfig()
			if test.wantError {
				if err == nil {
					t.Fatal("loadSandboxInitConfig() accepted an invalid Landlock configuration")
				}
				return
			}
			if err != nil {
				t.Fatalf("loadSandboxInitConfig() error = %v", err)
			}
			if config.Landlock != test.want || config.LandlockFallback != test.wantFallback {
				t.Fatalf("landlock = %q/%q, want %q/%q", config.Landlock, config.LandlockFallback, test.want, test.wantFallback)
			}
		})
	}
}

func TestSandboxInitEgressAuditLogIsOptInAndRequiresObservation(t *testing.T) {
	t.Setenv("SANDBOX_INIT_EGRESS_AUDIT_LOG", "")
	t.Setenv("SANDBOX_INIT_EGRESS", "audit")
	config, err := loadSandboxInitConfig()
	if err != nil {
		t.Fatalf("loadSandboxInitConfig() error = %v", err)
	}
	if config.EgressAuditLog {
		t.Fatal("init audit log is enabled by default, want opt-in")
	}

	t.Setenv("SANDBOX_INIT_EGRESS_AUDIT_LOG", "true")
	config, err = loadSandboxInitConfig()
	if err != nil {
		t.Fatalf("loadSandboxInitConfig() with the audit log enabled error = %v", err)
	}
	if !config.EgressAuditLog {
		t.Fatal("init audit log was not enabled from the environment")
	}

	t.Setenv("SANDBOX_INIT_EGRESS", "off")
	if _, err := loadSandboxInitConfig(); err == nil {
		t.Fatal("loadSandboxInitConfig() accepted the audit log with egress off")
	}
	t.Setenv("SANDBOX_INIT_EGRESS_AUDIT_LOG", "sometimes")
	t.Setenv("SANDBOX_INIT_EGRESS", "audit")
	if _, err := loadSandboxInitConfig(); err == nil {
		t.Fatal("loadSandboxInitConfig() accepted a non-boolean audit log switch")
	}
}

func TestSandboxInitWorkloadIdentityReadsAndValidates(t *testing.T) {
	t.Setenv("SANDBOX_INIT_WORKLOAD_UID", "1000")
	t.Setenv("SANDBOX_INIT_WORKLOAD_GID", "1000")
	config, err := loadSandboxInitConfig()
	if err != nil {
		t.Fatalf("loadSandboxInitConfig() error = %v", err)
	}
	if config.WorkloadUID != 1000 || config.WorkloadGID != 1000 {
		t.Fatalf("workload identity = %d/%d, want 1000/1000", config.WorkloadUID, config.WorkloadGID)
	}

	t.Setenv("SANDBOX_INIT_WORKLOAD_GID", "")
	if _, err := loadSandboxInitConfig(); err == nil {
		t.Fatal("loadSandboxInitConfig() accepted uid without gid")
	}
	t.Setenv("SANDBOX_INIT_WORKLOAD_GID", "0")
	if _, err := loadSandboxInitConfig(); err == nil {
		t.Fatal("loadSandboxInitConfig() accepted gid 0 with a non-zero uid")
	}
	t.Setenv("SANDBOX_INIT_WORKLOAD_UID", "70000")
	t.Setenv("SANDBOX_INIT_WORKLOAD_GID", "1000")
	if _, err := loadSandboxInitConfig(); err == nil {
		t.Fatal("loadSandboxInitConfig() accepted an out-of-range uid")
	}
}

func TestSandboxEgressStrictProfileUnifiesInitEgressAndSupervisor(t *testing.T) {
	t.Setenv("SANDBOX_EGRESS_PROFILE", "strict")
	t.Setenv("SANDBOX_INIT_SOCKET_DIR", "/run/ac")
	t.Setenv("SANDBOX_INIT_ENABLED", "")
	t.Setenv("SANDBOX_INIT_EGRESS", "")
	t.Setenv("SANDBOX_SUPERVISOR", "")
	config, err := loadSandboxInitConfig()
	if err != nil {
		t.Fatalf("loadSandboxInitConfig() error = %v", err)
	}
	if !config.Enabled || config.Egress != sandboxinit.EgressModeEnforce || !config.Supervisor {
		t.Fatalf("strict profile = enabled=%v egress=%q supervisor=%v, want true/enforce/true", config.Enabled, config.Egress, config.Supervisor)
	}

	t.Setenv("SANDBOX_INIT_ENABLED", "false")
	if _, err := loadSandboxInitConfig(); err == nil {
		t.Fatal("strict profile accepted SANDBOX_INIT_ENABLED=false")
	}
	t.Setenv("SANDBOX_INIT_ENABLED", "")
	t.Setenv("SANDBOX_SUPERVISOR", "off")
	if _, err := loadSandboxInitConfig(); err == nil {
		t.Fatal("strict profile accepted SANDBOX_SUPERVISOR=off")
	}
}
