//go:build linux

package driver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// boxliteShimStandInSource is the program the shim stand-in is copied from. Any
// long-lived executable works; only its name matters to the reaper.
const boxliteShimStandInSource = "/bin/sleep"

// startStandInCommand starts a prepared stand-in command and guarantees the test
// does not leave it behind: the reaper kills it in the positive test, and
// cleanup kills whatever the test asserts must stay alive.
//
// It also waits for the child's own program to be published, because Start
// returns as soon as the child's exec closes its error pipe, which the kernel
// does before it installs the new arguments (see waitForStandInProgram).
func startStandInCommand(t *testing.T, cmd *exec.Cmd) *exec.Cmd {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start %s: %v", cmd.Path, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	waitForStandInProgram(t, cmd)
	return cmd
}

// startStandInProcess starts a long-lived process from path.
func startStandInProcess(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	return startStandInCommand(t, exec.Command(path, "60"))
}

// waitForStandInProgram waits until the kernel reports the child's own argv[0],
// so a test never inspects a process that is still being started.
//
// os/exec reports a child as started once its exec closes the error pipe, and
// the kernel closes that pipe before it installs the new program's arguments. A
// /proc read in between sees an empty argv[0], or the executable the parent
// still runs, so a reaper asked to identify the pid there correctly refuses to
// signal it. Waiting for the argv[0] the test started removes that race instead
// of leaving the outcome to machine load.
func waitForStandInProgram(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	pid := cmd.Process.Pid
	want := cmd.Args[0]
	deadline := time.Now().Add(2 * time.Second)
	for {
		argv0, err := readStandInArgv0(pid)
		if err != nil {
			t.Fatalf("read the stand-in process %d command line: %v", pid, err)
		}
		if argv0 == want {
			return
		}
		if time.Now().After(deadline) {
			exe, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
			t.Fatalf("stand-in process %d never ran %q: alive=%v argv0=%q exe=%q", pid, want, boxliteProcessAlive(pid), argv0, exe)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func readStandInArgv0(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", err
	}
	argv0, _, _ := strings.Cut(string(raw), "\x00")
	return argv0, nil
}

// copyBoxliteShimStandIn writes a real binary under the shim's name and returns
// its path.
func copyBoxliteShimStandIn(t *testing.T) string {
	t.Helper()
	shimPath := filepath.Join(t.TempDir(), boxliteShimExecutableName)
	raw, err := os.ReadFile(boxliteShimStandInSource)
	if err != nil {
		t.Skipf("cannot read %s for a boxlite shim stand-in: %v", boxliteShimStandInSource, err)
	}
	if err := os.WriteFile(shimPath, raw, 0o755); err != nil {
		t.Skipf("cannot create a boxlite shim stand-in: %v", err)
	}
	return shimPath
}

// startBoxliteShimStandIn starts a long-lived process that runs the shim binary
// under the shim's own name, which is what the reaper identifies before it
// signals anything. The test copies the binary instead of linking to it so both
// programs the reaper compares - the argv[0] the process was started with and
// the executable it runs - name the shim: a symlink would leave the executable
// pointing at the real binary, the shape the reaper must refuse.
func startBoxliteShimStandIn(t *testing.T) *exec.Cmd {
	t.Helper()
	return startStandInProcess(t, copyBoxliteShimStandIn(t))
}

// startBoxliteWrapperStandIn starts the shim binary under an argv[0] that does
// not name it, the shape a shim started through a wrapper keeps. Its executable
// is then the only signal left, and the reaper must still release it.
func startBoxliteWrapperStandIn(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(copyBoxliteShimStandIn(t), "60")
	cmd.Args[0] = filepath.Join(t.TempDir(), "boxlite-wrapper")
	return startStandInCommand(t, cmd)
}

// startBoxliteMentioningStandIn starts a long-lived process whose *arguments*
// name boxlite while the program it runs does not. That is the look-alike the
// reaper must refuse: a pid recorded on disk can be reused by any process, and
// only the program it runs may decide whether it is the shim.
func startBoxliteMentioningStandIn(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(boxliteShimStandInSource, "60")
	cmd.Args[0] = filepath.Join(t.TempDir(), "boxlite-notes")
	return startStandInCommand(t, cmd)
}

func writeBoxliteShimPID(t *testing.T, runtimeHome, boxID string, pid int) {
	t.Helper()
	boxDir := filepath.Join(runtimeHome, "boxes", boxID)
	if err := os.MkdirAll(boxDir, 0o755); err != nil {
		t.Fatalf("create box directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(boxDir, boxliteShimPIDFileName), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatalf("write shim pid file: %v", err)
	}
}

// TestReapBoxliteShimProcessesReleasesTheShimOfAFailedStart pins the fix for the
// leak the test round measured: a failed boxlite start leaves its detached shim
// running with the VM's /dev/kvm handle open, and repeated failures accumulate
// handles until the host's kvm module cannot be unloaded.
func TestReapBoxliteShimProcessesReleasesTheShimOfAFailedStart(t *testing.T) {
	runtimeHome := t.TempDir()
	cmd := startBoxliteShimStandIn(t)
	writeBoxliteShimPID(t, runtimeHome, "box-of-failed-start", cmd.Process.Pid)

	reaped, err := ReapBoxliteShimProcesses(runtimeHome, "box-of-failed-start")
	if err != nil {
		t.Fatalf("ReapBoxliteShimProcesses returned error: %v", err)
	}
	if !reaped {
		t.Fatal("ReapBoxliteShimProcesses reported nothing to reap for a failed start's shim")
	}
	if boxliteProcessAlive(cmd.Process.Pid) {
		t.Fatalf("boxlite shim process %d is still alive after reaping", cmd.Process.Pid)
	}
}

// TestReapBoxliteShimProcessesReleasesAShimItKnowsOnlyByItsExecutable pins the
// other half of the identification. A shim started through a wrapper keeps its
// pid and its executable but not its argv[0], and the kernel publishes a new
// program's arguments a moment after os/exec reports it started, so a recorded
// pid can name a process whose arguments do not mention the shim at all. Both
// look like an unrelated process, and refusing them leaves the KVM handle the
// reaper exists to release.
func TestReapBoxliteShimProcessesReleasesAShimItKnowsOnlyByItsExecutable(t *testing.T) {
	runtimeHome := t.TempDir()
	cmd := startBoxliteWrapperStandIn(t)
	writeBoxliteShimPID(t, runtimeHome, "box-of-wrapped-shim", cmd.Process.Pid)

	reaped, err := ReapBoxliteShimProcesses(runtimeHome, "box-of-wrapped-shim")
	if err != nil {
		t.Fatalf("ReapBoxliteShimProcesses returned error: %v", err)
	}
	if !reaped {
		t.Fatal("ReapBoxliteShimProcesses reported nothing to reap for a wrapped shim")
	}
	if boxliteProcessAlive(cmd.Process.Pid) {
		t.Fatalf("boxlite shim process %d is still alive after reaping", cmd.Process.Pid)
	}
}

// TestReapBoxliteShimProcessesRefusesAProcessItDoesNotOwn pins the safety rule
// the reaper needs because pids are reused: a stale pid file must not turn into
// a signal to an unrelated process.
func TestReapBoxliteShimProcessesRefusesAProcessItDoesNotOwn(t *testing.T) {
	runtimeHome := t.TempDir()
	cmd := startStandInProcess(t, "/bin/sleep")
	writeBoxliteShimPID(t, runtimeHome, "box-with-stale-pid", cmd.Process.Pid)

	reaped, err := ReapBoxliteShimProcesses(runtimeHome, "box-with-stale-pid")
	if err == nil {
		t.Fatal("ReapBoxliteShimProcesses accepted a pid that is not a boxlite process")
	}
	if reaped {
		t.Fatal("ReapBoxliteShimProcesses reported reaping a process it must not signal")
	}
	if !boxliteProcessAlive(cmd.Process.Pid) {
		t.Fatalf("unrelated process %d was signalled", cmd.Process.Pid)
	}
}

// TestReapBoxliteShimProcessesRefusesAProcessThatOnlyMentionsBoxlite pins the
// tightened identification: matching the whole command line accepted any
// process with "boxlite" anywhere in its arguments, which a reused pid only has
// to mention once to be killed.
func TestReapBoxliteShimProcessesRefusesAProcessThatOnlyMentionsBoxlite(t *testing.T) {
	runtimeHome := t.TempDir()
	cmd := startBoxliteMentioningStandIn(t)
	writeBoxliteShimPID(t, runtimeHome, "box-with-a-lookalike-pid", cmd.Process.Pid)

	reaped, err := ReapBoxliteShimProcesses(runtimeHome, "box-with-a-lookalike-pid")
	if err == nil {
		t.Fatal("ReapBoxliteShimProcesses accepted a pid whose arguments merely mention boxlite")
	}
	if reaped {
		t.Fatal("ReapBoxliteShimProcesses reported reaping a process it must not signal")
	}
	if !boxliteProcessAlive(cmd.Process.Pid) {
		t.Fatalf("unrelated process %d was signalled", cmd.Process.Pid)
	}
}

func TestReapBoxliteShimProcessesSkipsABoxWithoutAShimPID(t *testing.T) {
	runtimeHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runtimeHome, "boxes", "box-never-started"), 0o755); err != nil {
		t.Fatalf("create box directory: %v", err)
	}

	reaped, err := reapBoxliteShimProcesses(runtimeHome, "box-never-started", boxliteReapTiming{pidAttempts: 1, exitAttempts: 1})
	if err != nil {
		t.Fatalf("reapBoxliteShimProcesses returned error: %v", err)
	}
	if reaped {
		t.Fatal("reapBoxliteShimProcesses reported reaping a shim that was never recorded")
	}
}

func TestReapBoxliteShimProcessesRejectsMalformedInput(t *testing.T) {
	runtimeHome := t.TempDir()
	boxDir := filepath.Join(runtimeHome, "boxes", "box-with-bad-pid")
	if err := os.MkdirAll(boxDir, 0o755); err != nil {
		t.Fatalf("create box directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(boxDir, boxliteShimPIDFileName), []byte("not-a-pid"), 0o600); err != nil {
		t.Fatalf("write malformed shim pid file: %v", err)
	}

	if _, err := reapBoxliteShimProcesses(runtimeHome, "box-with-bad-pid", boxliteReapTiming{pidAttempts: 1, exitAttempts: 1}); err == nil {
		t.Fatal("reapBoxliteShimProcesses accepted a malformed shim pid file")
	}
	if _, err := ReapBoxliteShimProcesses(runtimeHome, "../outside-the-home"); err == nil {
		t.Fatal("ReapBoxliteShimProcesses accepted a box id that is not a single path element")
	}
	if reaped, err := ReapBoxliteShimProcesses("", "box-with-bad-pid"); err != nil || reaped {
		t.Fatalf("ReapBoxliteShimProcesses without a runtime home = (%v, %v), want (false, nil)", reaped, err)
	}
}

func TestWaitForBoxliteShimPIDWaitsForALatePIDFile(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), boxliteShimPIDFileName)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(20 * time.Millisecond)
		_ = os.WriteFile(pidPath, []byte("1234\n"), 0o600)
	}()

	pid, found, err := waitForBoxliteShimPID(pidPath, 40, 5*time.Millisecond)
	<-done
	if err != nil {
		t.Fatalf("waitForBoxliteShimPID returned error: %v", err)
	}
	if !found || pid != 1234 {
		t.Fatalf("waitForBoxliteShimPID = (%d, %v), want (1234, true)", pid, found)
	}
}

// TestWaitForBoxliteShimPIDWaitsForAnIncompletePIDFile pins the other half of
// that window: boxlite creates the pid file before it writes the value, so a
// read that lands in between sees empty content. Reporting that as a malformed
// file leaves the shim and its /dev/kvm handle behind, which is the leak the
// retry exists to close.
func TestWaitForBoxliteShimPIDWaitsForAnIncompletePIDFile(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), boxliteShimPIDFileName)
	if err := os.WriteFile(pidPath, nil, 0o600); err != nil {
		t.Fatalf("create the empty shim pid file: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(20 * time.Millisecond)
		_ = os.WriteFile(pidPath, []byte("4321\n"), 0o600)
	}()

	pid, found, err := waitForBoxliteShimPID(pidPath, 40, 5*time.Millisecond)
	<-done
	if err != nil {
		t.Fatalf("waitForBoxliteShimPID returned error: %v", err)
	}
	if !found || pid != 4321 {
		t.Fatalf("waitForBoxliteShimPID = (%d, %v), want (4321, true)", pid, found)
	}
}

// TestWaitForBoxliteShimPIDReportsAFileThatNeverHoldsAPID keeps the widened
// retry from turning malformed input into a silent "nothing to reap": a file
// that stays invalid is still an error, while one that never appears is not.
func TestWaitForBoxliteShimPIDReportsAFileThatNeverHoldsAPID(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), boxliteShimPIDFileName)
	if err := os.WriteFile(pidPath, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatalf("write malformed shim pid file: %v", err)
	}

	pid, found, err := waitForBoxliteShimPID(pidPath, 3, time.Millisecond)
	if err == nil {
		t.Fatal("waitForBoxliteShimPID accepted a pid file that never holds a pid")
	}
	if found || pid != 0 {
		t.Fatalf("waitForBoxliteShimPID = (%d, %v), want (0, false)", pid, found)
	}

	if pid, found, err := waitForBoxliteShimPID(filepath.Join(t.TempDir(), boxliteShimPIDFileName), 2, time.Millisecond); err != nil || found || pid != 0 {
		t.Fatalf("waitForBoxliteShimPID for a missing file = (%d, %v, %v), want (0, false, nil)", pid, found, err)
	}
}
