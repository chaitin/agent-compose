package driver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestMarkedExecLeaderTerminationScriptKillsOnlyLeaderGroup exercises the
// embedded leader-termination script against a fabricated /proc so its
// root-detection and process-group selection can be asserted without a real
// container. The scenario mirrors docker exec non-TTY: the leader's process
// group has its leader outside the container namespace, so its pgrp reads 0,
// while the detached supervisor/task start their own groups and must survive.
func TestMarkedExecLeaderTerminationScriptKillsOnlyLeaderGroup(t *testing.T) {
	procRoot := t.TempDir()
	mkProc := func(pid, ppid, pgrp int, marker string) {
		dir := filepath.Join(procRoot, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		environ := ""
		if marker != "" {
			environ = dockerExecMarkerEnv + "=" + marker + "\x00"
		}
		if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(environ), 0o644); err != nil {
			t.Fatal(err)
		}
		// pid (comm) state ppid pgrp session ...
		stat := fmt.Sprintf("%d (sh) S %d %d 1 0 0 0 0 0 0\n", pid, ppid, pgrp)
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Container init carries no marker.
	mkProc(1, 0, 1, "")
	// The exec leader: parent (pid 1) is unmarked, so it is the root. Its
	// process group leader is outside the namespace, so pgrp reads 0.
	mkProc(100, 1, 0, "exec-1")
	// The leader's non-detached child shares pgrp 0.
	mkProc(200, 100, 0, "exec-1")
	// Detached supervisor and task start their own groups and must survive.
	mkProc(300, 200, 300, "exec-1")
	mkProc(400, 300, 400, "exec-1")

	script := strings.ReplaceAll(markedExecLeaderTerminationScript, "/proc", procRoot)
	logPath := filepath.Join(t.TempDir(), "killed")
	// Stub the kill builtin with a function that records the target pid, so the
	// script's selection is observable without signalling real processes.
	stub := fmt.Sprintf("kill() { printf '%%s\\n' \"$2\" >> %q; }; %s", logPath, script)
	cmd := exec.Command("sh", "-c", stub, "agent-compose-exec-leader-cleanup", "exec-1", dockerExecMarkerEnv)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("kill stub recorded nothing: %v", err)
	}
	killed := strings.Fields(string(raw))
	sort.Strings(killed)
	want := []string{"100", "200"}
	if !reflect.DeepEqual(killed, want) {
		t.Fatalf("killed = %v, want %v", killed, want)
	}
}
