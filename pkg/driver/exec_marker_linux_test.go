//go:build linux

package driver

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The leader starts one child in its own session, which is what a process
// group kill misses and what a leader-only kill misses entirely. The script
// must still end the whole marked tree and leave unmarked processes alone.
func TestMarkedExecTerminationScriptEndsWholeMarkedTree(t *testing.T) {
	marker := newExecMarker()
	tree := exec.Command("sh", "-c", "setsid sleep 300 </dev/null >/dev/null 2>&1 & sleep 300")
	tree.Env = append(os.Environ(), guestExecMarkerEnv+"="+marker)
	if err := tree.Start(); err != nil {
		t.Fatalf("start marked tree: %v", err)
	}
	t.Cleanup(func() { _ = tree.Process.Kill() })
	bystander := exec.Command("sleep", "300")
	if err := bystander.Start(); err != nil {
		t.Fatalf("start unmarked process: %v", err)
	}
	t.Cleanup(func() { _ = bystander.Process.Kill(); _ = bystander.Wait() })

	waitForMarkedProcesses(t, marker, 3)

	command := markedExecTerminationCommand(guestExecMarkerEnv, marker)
	if output, err := exec.Command(command[0], command[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("termination script failed: %v: %s", err, output)
	}
	_ = tree.Wait()
	if pids := markedProcesses(t, marker); len(pids) != 0 {
		t.Fatalf("marked processes survived termination: %v", pids)
	}
	if !processRunning(bystander.Process.Pid) {
		t.Fatal("unmarked process was terminated")
	}
}

// processRunning reports whether pid exists and has not exited; an exited but
// unreaped child is a zombie and does not count.
func processRunning(pid int) bool {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	end := bytes.LastIndexByte(stat, ')')
	return end >= 0 && end+2 < len(stat) && stat[end+2] != 'Z'
}

func waitForMarkedProcesses(t *testing.T, marker string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := markedProcesses(t, marker); len(got) >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("marked processes = %v, want at least %d", markedProcesses(t, marker), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func markedProcesses(t *testing.T, marker string) []int {
	t.Helper()
	expected := []byte(guestExecMarkerEnv + "=" + marker)
	paths, err := filepath.Glob("/proc/[0-9]*/environ")
	if err != nil {
		t.Fatalf("list processes: %v", err)
	}
	var pids []int
	for _, path := range paths {
		environ, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, entry := range bytes.Split(environ, []byte{0}) {
			if bytes.Equal(entry, expected) {
				pid, _ := strconv.Atoi(filepath.Base(filepath.Dir(path)))
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}
