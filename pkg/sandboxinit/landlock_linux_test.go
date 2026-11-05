//go:build linux

package sandboxinit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// landlockTestPolicy is the smallest ruleset that can run a shell: the system
// baseline plus one writable workspace. The denied fixture deliberately lives
// outside every rule.
func landlockTestPolicy(workspace string) LandlockPolicy {
	rules := append([]LandlockRule(nil), defaultLandlockSystemRules...)
	for _, path := range landlockReadOnlyFiles {
		rules = append(rules, LandlockRule{Path: path, Scope: LandlockScopeRead})
	}
	rules = append(rules, LandlockRule{Path: workspace, Scope: LandlockScopeReadWrite})
	if coverageDir := strings.TrimSpace(os.Getenv("GOCOVERDIR")); filepath.IsAbs(coverageDir) {
		// The Go coverage runtime writes its counters from the confined helper
		// process too. Allow only that exact build directory; the test's denied
		// fixture lives elsewhere under TMPDIR and stays denied.
		rules = append(rules, LandlockRule{Path: filepath.Clean(coverageDir), Scope: LandlockScopeReadWrite})
	}
	return LandlockPolicy{Rules: rules}
}

func requireLandlockHost(t *testing.T) {
	t.Helper()
	abi, err := landlockABI()
	if err != nil {
		t.Skipf("host Landlock is unavailable: %v", err)
	}
	if abi < 1 {
		t.Skipf("host Landlock reported ABI %d", abi)
	}
}

func TestResolveLandlockSetup(t *testing.T) {
	probeErr := errors.New("function not implemented")
	for _, test := range []struct {
		name        string
		mode        LandlockMode
		fallback    LandlockFallback
		abi         int
		probeErr    error
		wantActive  bool
		wantWarning bool
		wantErr     bool
	}{
		{name: "off never probes", mode: LandlockModeOff, abi: 0},
		{name: "enforce supported", mode: LandlockModeEnforce, fallback: LandlockFallbackFail, abi: 4, wantActive: true},
		{name: "enforce unsupported fails closed", mode: LandlockModeEnforce, fallback: LandlockFallbackFail, probeErr: probeErr, wantErr: true},
		{name: "enforce unsupported with warn degrades", mode: LandlockModeEnforce, fallback: LandlockFallbackWarn, probeErr: probeErr, wantWarning: true},
		{name: "abi zero fails closed", mode: LandlockModeEnforce, fallback: LandlockFallbackFail, abi: 0, wantErr: true},
		{name: "abi zero with warn degrades", mode: LandlockModeEnforce, fallback: LandlockFallbackWarn, abi: 0, wantWarning: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			active, warning, err := resolveLandlockSetup(test.mode, test.fallback, test.abi, test.probeErr)
			if (err != nil) != test.wantErr {
				t.Fatalf("resolveLandlockSetup() error = %v, wantErr %v", err, test.wantErr)
			}
			if active != test.wantActive {
				t.Fatalf("resolveLandlockSetup() active = %v, want %v", active, test.wantActive)
			}
			if (warning != "") != test.wantWarning {
				t.Fatalf("resolveLandlockSetup() warning = %q, wantWarning %v", warning, test.wantWarning)
			}
		})
	}
}

func TestLandlockHandledAccessGatesRightsByABI(t *testing.T) {
	abi1 := landlockHandledFS(1)
	if abi1&unix.LANDLOCK_ACCESS_FS_REFER != 0 || abi1&unix.LANDLOCK_ACCESS_FS_TRUNCATE != 0 {
		t.Fatalf("ABI 1 handled access has REFER/TRUNCATE: %#x", abi1)
	}
	abi2 := landlockHandledFS(2)
	if abi2&unix.LANDLOCK_ACCESS_FS_REFER == 0 || abi2&unix.LANDLOCK_ACCESS_FS_TRUNCATE != 0 {
		t.Fatalf("ABI 2 handled access = %#x, want REFER without TRUNCATE", abi2)
	}
	abi3 := landlockHandledFS(3)
	if abi3&unix.LANDLOCK_ACCESS_FS_REFER == 0 || abi3&unix.LANDLOCK_ACCESS_FS_TRUNCATE == 0 {
		t.Fatalf("ABI 3 handled access = %#x, want REFER and TRUNCATE", abi3)
	}
	if abi3&unix.LANDLOCK_ACCESS_FS_WRITE_FILE == 0 {
		t.Fatal("handled access is missing WRITE_FILE")
	}
}

// TestLandlockEnforcementHelper is not a standalone test: the parent runs the
// test binary as a child so the ruleset is installed after fork, exactly like
// the exec trampoline.
func TestLandlockEnforcementHelper(t *testing.T) {
	if os.Getenv("AGENT_COMPOSE_LANDLOCK_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	runtime.LockOSThread()
	policy, err := decodeLandlockPolicy(os.Getenv(landlockPolicyEnvVar))
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper policy:", err)
		os.Exit(2)
	}
	if err := applyLandlockPolicy(policy); err != nil {
		fmt.Fprintln(os.Stderr, "helper apply:", err)
		os.Exit(2)
	}
	workspace := os.Getenv("AGENT_COMPOSE_LANDLOCK_WORKSPACE")
	denied := os.Getenv("AGENT_COMPOSE_LANDLOCK_DENIED")
	results := map[string]bool{}
	results["workspace_write"] = os.WriteFile(filepath.Join(workspace, "written"), []byte("ok"), 0o600) == nil
	if _, readErr := os.ReadFile(filepath.Join(denied, "secret")); readErr != nil {
		results["denied_read"] = errors.Is(readErr, fs.ErrPermission)
	}
	if writeErr := os.WriteFile(filepath.Join(denied, "new"), []byte("x"), 0o600); writeErr != nil {
		results["denied_write"] = errors.Is(writeErr, fs.ErrPermission)
	}
	_, hostsErr := os.ReadFile("/etc/hosts")
	results["system_read"] = hostsErr == nil
	results["system_exec"] = exec.Command("/bin/sh", "-c", "exit 0").Run() == nil
	conn, dialErr := net.DialTimeout("tcp", os.Getenv("AGENT_COMPOSE_LANDLOCK_TCP"), 2*time.Second)
	results["tcp_connect"] = dialErr == nil
	if conn != nil {
		_ = conn.Close()
	}
	if err := json.NewEncoder(os.Stdout).Encode(results); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestLandlockDeniesUnlistedPathAndKeepsWorkspaceWritable(t *testing.T) {
	requireLandlockHost(t)
	workspace := t.TempDir()
	denied := t.TempDir()
	if err := os.WriteFile(filepath.Join(denied, "secret"), []byte("classified"), 0o600); err != nil {
		t.Fatalf("write denied fixture: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	policy := landlockTestPolicy(workspace)
	payload, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLandlockEnforcementHelper$")
	cmd.Env = append(os.Environ(),
		"AGENT_COMPOSE_LANDLOCK_HELPER=1",
		landlockPolicyEnvVar+"="+string(payload),
		"AGENT_COMPOSE_LANDLOCK_WORKSPACE="+workspace,
		"AGENT_COMPOSE_LANDLOCK_DENIED="+denied,
		"AGENT_COMPOSE_LANDLOCK_TCP="+listener.Addr().String(),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v\n%s", err, output)
	}
	select {
	case conn := <-accepted:
		_ = conn.Close()
	default:
	}
	results := map[string]bool{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &results); err != nil {
		t.Fatalf("decode helper results %q: %v", output, err)
	}
	for _, check := range []string{"workspace_write", "denied_read", "denied_write", "system_read", "system_exec", "tcp_connect"} {
		if !results[check] {
			t.Fatalf("helper result %s = false, want true (results: %v)", check, results)
		}
	}
	data, err := os.ReadFile(filepath.Join(workspace, "written"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("workspace write = %q, %v; want %q", data, err, "ok")
	}
}

func TestValidateLandlockPolicyPathsRejectsMissingWritablePath(t *testing.T) {
	policy := LandlockPolicy{Rules: []LandlockRule{
		{Path: t.TempDir(), Scope: LandlockScopeReadWrite},
		{Path: filepath.Join(t.TempDir(), "missing"), Scope: LandlockScopeReadWrite},
	}}
	err := validateLandlockPolicyPaths(policy)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("validateLandlockPolicyPaths() error = %v, want a missing writable path error", err)
	}
}

func TestValidateLandlockPolicyPathsIgnoresMissingSystemPaths(t *testing.T) {
	policy := LandlockPolicy{Rules: []LandlockRule{
		{Path: filepath.Join(t.TempDir(), "missing"), Scope: LandlockScopeRead},
		{Path: t.TempDir(), Scope: LandlockScopeReadWrite},
	}}
	if err := validateLandlockPolicyPaths(policy); err != nil {
		t.Fatalf("validateLandlockPolicyPaths() error = %v, want missing read-only paths to be optional", err)
	}
}

// initExecResult runs one non-interactive command through a live init server.
func initExecResult(t *testing.T, client *Client, request Request) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := client.Exec(ctx, request)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	defer func() { _ = session.Close() }()
	var stdout, stderr strings.Builder
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		switch frame.Type {
		case FrameStdout:
			stdout.Write(frame.Data)
		case FrameStderr:
			stderr.Write(frame.Data)
		case FrameExit:
			return stdout.String(), stderr.String(), frame.Code
		}
	}
}

func TestInitLandlockConfinesWorkloadFilesystem(t *testing.T) {
	requireLandlockHost(t)
	workspace := t.TempDir()
	denied := t.TempDir()
	if err := os.WriteFile(filepath.Join(denied, "secret"), []byte("classified"), 0o600); err != nil {
		t.Fatalf("write denied fixture: %v", err)
	}
	policy := landlockTestPolicy(workspace)
	_, socket := startTestInitWithOptions(t, Options{
		MainShell: "sleep 120",
		Landlock: LandlockOptions{
			Mode:     LandlockModeEnforce,
			Fallback: LandlockFallbackFail,
			Policy:   &policy,
		},
	})
	client := NewClient(socket)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	info, err := client.VerifySandbox(ctx, "")
	if err != nil {
		t.Fatalf("VerifySandbox() error = %v", err)
	}
	if !info.Supports(CapabilityLandlockEnforce) {
		t.Fatalf("capabilities = %v, want %q", info.Capabilities, CapabilityLandlockEnforce)
	}

	stdout, stderr, code := initExecResult(t, client, Request{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf ok > " + workspace + "/written && cat " + workspace + "/written"},
	})
	if code != 0 || strings.TrimSpace(stdout) != "ok" {
		t.Fatalf("workspace write: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	_, stderr, code = initExecResult(t, client, Request{
		Command: "/bin/sh",
		Args:    []string{"-c", "cat " + denied + "/secret"},
	})
	if code == 0 || !strings.Contains(strings.ToLower(stderr), "permission denied") {
		t.Fatalf("denied read: code=%d stderr=%q, want a permission failure", code, stderr)
	}

	_, stderr, code = initExecResult(t, client, Request{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf x > " + denied + "/new"},
	})
	if code == 0 || !strings.Contains(strings.ToLower(stderr), "permission denied") {
		t.Fatalf("denied write: code=%d stderr=%q, want a permission failure", code, stderr)
	}
}

func TestInitLandlockRejectsAMissingWritablePath(t *testing.T) {
	requireLandlockHost(t)
	workspace := t.TempDir()
	policy := LandlockPolicy{Rules: []LandlockRule{
		{Path: workspace, Scope: LandlockScopeReadWrite},
		{Path: filepath.Join(workspace, "missing"), Scope: LandlockScopeReadWrite},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := Run(ctx, Options{
		ControlSocket: filepath.Join(t.TempDir(), "init.sock"),
		Landlock: LandlockOptions{
			Mode:     LandlockModeEnforce,
			Fallback: LandlockFallbackFail,
			Policy:   &policy,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("Run() error = %v, want a fail-closed missing path error", err)
	}
}
