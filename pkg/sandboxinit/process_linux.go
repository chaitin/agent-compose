//go:build linux

package sandboxinit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Hooks is the extension point for future isolation work. Stage 1 installs no
// hook; later stages can use Prepare to apply seccomp/Landlock/cgroup settings
// to the child before exec, without changing the control protocol.
type Hooks struct {
	Prepare    func(*exec.Cmd) error
	AfterStart func(pid int) error
}

const (
	markerEnv = "AGENT_COMPOSE_INTERNAL_INIT_MARKER"
	// maxFrameBuffer bounds how much per-stream output can queue while a
	// client is connected. A disconnected client is drained to io.Discard.
	maxFrameBuffer = 256
	// execOutputDrainGrace bounds how long an exited exec waits for final
	// output before its exit frame is delivered. Descendants can hold the
	// inherited pipes indefinitely.
	execOutputDrainGrace = 250 * time.Millisecond
)

// process is one command owned by init. The manager owns it until wait4
// reports its exit.
type process struct {
	id      string
	pid     int
	pgid    int
	command string
	args    []string
	marker  string
	tty     bool
	main    bool

	stdin  io.WriteCloser
	master *os.File

	frames  chan Frame
	discard chan struct{}
	stopOut chan struct{}
	done    chan struct{}
	once    sync.Once
	outOnce sync.Once
	output  sync.WaitGroup

	startedAt time.Time
	exitCode  atomic.Int32
	waitErr   error
}

func (p *process) info() ProcessInfo {
	return ProcessInfo{
		ID:        p.id,
		PID:       p.pid,
		Command:   p.command,
		Args:      append([]string(nil), p.args...),
		Marker:    p.marker,
		TTY:       p.tty,
		StartedAt: p.startedAt,
		Main:      p.main,
	}
}

func (p *process) detach() {
	p.once.Do(func() { close(p.discard) })
}

func (p *process) stopOutput() {
	p.outOnce.Do(func() { close(p.stopOut) })
}

// finish reports the leader's exit without waiting indefinitely for descendants
// that inherited stdout/stderr. The leader is already reaped, so the exit frame
// is the truth the caller needs; a background process holding the pipe must not
// turn a completed exec into a timeout. Output that is already available gets a
// short drain grace, then the pipes are drained to the discard path while the
// exit frame is delivered.
func (p *process) finish(code int, err error) {
	p.exitCode.Store(int32(code))
	p.waitErr = err
	go func() {
		drained := make(chan struct{})
		go func() {
			p.output.Wait()
			close(drained)
		}()
		timer := time.NewTimer(execOutputDrainGrace)
		select {
		case <-drained:
			timer.Stop()
		case <-timer.C:
		}
		p.stopOutput()
		select {
		case <-p.discard:
		case p.frames <- Frame{Type: FrameExit, SessionID: p.id, Code: code, Message: errString(err)}:
		}
		close(p.done)
	}()
}

func (p *process) exitStatus() int {
	return int(p.exitCode.Load())
}

// closeStdin closes the child's stdin direction. A TTY master cannot be closed
// without hanging up the terminal and killing output, so EOF is delivered as
// the terminal's EOT character instead.
func (p *process) closeStdin() {
	if p.tty {
		if p.master != nil {
			_, _ = p.master.Write([]byte{0x04})
			// EOT is the terminal EOF and lets canonical readers finish while
			// keeping output flowing. A program in raw mode may ignore it, so
			// fall back to hanging up the terminal rather than leaving CloseSend
			// blocked forever.
			go func(master *os.File) {
				select {
				case <-p.done:
				case <-time.After(500 * time.Millisecond):
					_ = master.Close()
				}
			}(p.master)
		}
		return
	}
	if closer, ok := p.stdin.(io.Closer); ok {
		_ = closer.Close()
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type processManager struct {
	// spawnMu serializes child start + registration against wait4 dispatch.
	// Without it an immediately-exiting child could be reaped before byPID has
	// the record, losing its exit frame.
	spawnMu    sync.Mutex
	mu         sync.Mutex
	byID       map[string]*process
	byPID      map[int]*process
	hooks      Hooks
	stopOnce   sync.Once
	stopped    chan struct{}
	reaperDone chan struct{}
}

func newProcessManager(hooks Hooks) *processManager {
	return &processManager{
		byID:       make(map[string]*process),
		byPID:      make(map[int]*process),
		hooks:      hooks,
		stopped:    make(chan struct{}),
		reaperDone: make(chan struct{}),
	}
}

func newProcessID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("p-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// start launches a command and registers it before the reaper can observe its
// exit. A nil frames channel means output is written to the supplied writers
// (the main process); otherwise output is streamed as frames.
func (m *processManager) start(ctx context.Context, request Request, main bool, stdout, stderr io.Writer) (*process, error) {
	if strings.TrimSpace(request.Command) == "" {
		return nil, errors.New("sandbox init exec command is required")
	}
	p := &process{
		id:        newProcessID(),
		command:   request.Command,
		args:      append([]string(nil), request.Args...),
		marker:    strings.TrimSpace(request.Marker),
		tty:       request.TTY,
		main:      main,
		frames:    make(chan Frame, maxFrameBuffer),
		discard:   make(chan struct{}),
		stopOut:   make(chan struct{}),
		done:      make(chan struct{}),
		startedAt: time.Now().UTC(),
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Hold spawnMu from before Start until the process is registered. The
	// reaper takes the same lock after wait4, so a fast exit cannot win the
	// lookup race.
	m.spawnMu.Lock()
	defer m.spawnMu.Unlock()
	cmd := exec.Command(request.Command, request.Args...)
	cmd.Env = processEnvironment(request.Env, p.marker)
	if cwd := strings.TrimSpace(request.Cwd); cwd != "" {
		cmd.Dir = cwd
	}
	if m.hooks.Prepare != nil {
		if err := m.hooks.Prepare(cmd); err != nil {
			return nil, fmt.Errorf("prepare sandbox init child: %w", err)
		}
	}
	if request.TTY {
		ws := &pty.Winsize{Rows: request.Rows, Cols: request.Cols}
		if ws.Rows == 0 {
			ws.Rows = 24
		}
		if ws.Cols == 0 {
			ws.Cols = 80
		}
		master, tty, err := pty.Open()
		if err != nil {
			return nil, fmt.Errorf("open sandbox init pty: %w", err)
		}
		if err := pty.Setsize(master, ws); err != nil {
			_ = master.Close()
			_ = tty.Close()
			return nil, fmt.Errorf("size sandbox init pty: %w", err)
		}
		cmd.Stdin = tty
		cmd.Stdout = tty
		cmd.Stderr = tty
		// Ctty names the child-side descriptor. pty.StartWithSize uses the
		// parent fd, which newer Go rejects ("Ctty not valid in child"); the
		// tty is duped to fd 0/1/2, so 0 is the correct child descriptor.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
		if err := cmd.Start(); err != nil {
			_ = master.Close()
			_ = tty.Close()
			return nil, fmt.Errorf("start sandbox init tty process: %w", err)
		}
		_ = tty.Close()
		p.pid = cmd.Process.Pid
		p.pgid = p.pid
		p.master = master
		p.stdin = master
		p.output.Add(1)
		go p.copyPTY(master, stdout)
	} else {
		stdinR, stdinW, err := os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("create sandbox init stdin pipe: %w", err)
		}
		stdoutR, stdoutW, err := os.Pipe()
		if err != nil {
			_ = stdinR.Close()
			_ = stdinW.Close()
			return nil, fmt.Errorf("create sandbox init stdout pipe: %w", err)
		}
		stderrR, stderrW, err := os.Pipe()
		if err != nil {
			_ = stdinR.Close()
			_ = stdinW.Close()
			_ = stdoutR.Close()
			_ = stdoutW.Close()
			return nil, fmt.Errorf("create sandbox init stderr pipe: %w", err)
		}
		cmd.Stdin = stdinR
		cmd.Stdout = stdoutW
		cmd.Stderr = stderrW
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			_ = stdinR.Close()
			_ = stdinW.Close()
			_ = stdoutR.Close()
			_ = stdoutW.Close()
			_ = stderrR.Close()
			_ = stderrW.Close()
			return nil, fmt.Errorf("start sandbox init process: %w", err)
		}
		p.pid = cmd.Process.Pid
		p.pgid = p.pid
		p.stdin = stdinW
		_ = stdinR.Close()
		_ = stdoutW.Close()
		_ = stderrW.Close()
		p.output.Add(2)
		go p.copyOutput(stdoutR, FrameStdout, stdout)
		go p.copyOutput(stderrR, FrameStderr, stderr)
	}
	// Release the os.Process handle: the init reaper, not os/exec, owns
	// wait4. Releasing does not reap or wait for the child.
	_ = cmd.Process.Release()

	m.mu.Lock()
	m.byID[p.id] = p
	m.byPID[p.pid] = p
	m.mu.Unlock()

	if m.hooks.AfterStart != nil {
		if err := m.hooks.AfterStart(p.pid); err != nil {
			_ = p.signal(syscall.SIGKILL, true)
			return nil, fmt.Errorf("after sandbox init child start: %w", err)
		}
	}
	return p, nil
}

func processEnvironment(extra map[string]string, marker string) []string {
	env := make(map[string]string, len(os.Environ())+len(extra)+1)
	for _, item := range os.Environ() {
		if key, value, ok := strings.Cut(item, "="); ok {
			env[key] = value
		}
	}
	for key, value := range extra {
		env[key] = value
	}
	if marker != "" {
		env[markerEnv] = marker
	}
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

func (p *process) copyPTY(master *os.File, mainOut io.Writer) {
	defer p.output.Done()
	if mainOut != nil {
		_, _ = io.Copy(mainOut, master)
		return
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := master.Read(buf)
		if n > 0 {
			p.emit(Frame{Type: FrameStdout, SessionID: p.id, Data: append([]byte(nil), buf[:n]...)})
		}
		if err != nil {
			return
		}
	}
}

func (p *process) copyOutput(reader *os.File, frameType string, mainOut io.Writer) {
	defer p.output.Done()
	defer func() { _ = reader.Close() }()
	if mainOut != nil {
		_, _ = io.Copy(mainOut, reader)
		return
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			p.emit(Frame{Type: frameType, SessionID: p.id, Data: append([]byte(nil), buf[:n]...)})
		}
		if err != nil {
			return
		}
	}
}

func (p *process) emit(frame Frame) {
	select {
	case p.frames <- frame:
	case <-p.discard:
		// The client is gone. The reader continues draining the pipe in its
		// own loop, so the child never blocks on a full stdout/stderr buffer.
	case <-p.stopOut:
		// The leader exited and its exit frame is being delivered; descendants
		// may still write to the inherited pipes, but those bytes no longer
		// belong to this exec session.
	}
}

// ptySetsize changes the PTY window size.
func ptySetsize(master *os.File, rows, cols uint16) error {
	if master == nil || rows == 0 || cols == 0 {
		return nil
	}
	return pty.Setsize(master, &pty.Winsize{Rows: rows, Cols: cols})
}

func (p *process) signal(sig syscall.Signal, tree bool) error {
	if p.pid <= 0 {
		return errors.New("sandbox init process has no pid")
	}
	target := p.pid
	if tree {
		target = -p.pgid
	}
	if err := syscall.Kill(target, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal sandbox init process %d: %w", p.pid, err)
	}
	return nil
}

func (m *processManager) runReaper() {
	defer close(m.reaperDone)
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if errors.Is(err, syscall.ECHILD) {
				select {
				case <-m.stopped:
					return
				case <-time.After(10 * time.Millisecond):
					continue
				}
			}
			return
		}
		m.spawnMu.Lock()
		m.mu.Lock()
		p := m.byPID[pid]
		if p != nil {
			delete(m.byPID, pid)
			delete(m.byID, p.id)
		}
		m.mu.Unlock()
		m.spawnMu.Unlock()
		if p == nil {
			continue
		}
		code := 0
		var waitErr error
		switch {
		case ws.Exited():
			code = ws.ExitStatus()
		case ws.Signaled():
			code = 128 + int(ws.Signal())
			waitErr = fmt.Errorf("process terminated by %s", ws.Signal())
		}
		if closer, ok := p.stdin.(io.Closer); ok {
			_ = closer.Close()
		}
		p.finish(code, waitErr)
	}
}

func (m *processManager) lookup(id string) *process {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id]
}

func (m *processManager) list() []ProcessInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ProcessInfo, 0, len(m.byID))
	for _, p := range m.byID {
		out = append(out, ProcessInfo{
			ID:        p.id,
			PID:       p.pid,
			Command:   p.command,
			Args:      append([]string(nil), p.args...),
			Marker:    p.marker,
			TTY:       p.tty,
			StartedAt: p.startedAt,
			Main:      p.main,
		})
	}
	return out
}

func (m *processManager) kill(sessionID string, tree bool) error {
	p := m.lookup(sessionID)
	if p == nil {
		return fmt.Errorf("sandbox init session %s not found", sessionID)
	}
	return p.signal(syscall.SIGKILL, tree)
}

func (m *processManager) killTree(marker string) error {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return errors.New("sandbox init process-tree marker is required")
	}
	m.mu.Lock()
	var targets []*process
	for _, p := range m.byID {
		if p.marker == marker {
			targets = append(targets, p)
		}
	}
	m.mu.Unlock()
	for _, p := range targets {
		_ = p.signal(syscall.SIGKILL, true)
	}
	// A detached descendant may have left its original process group. Scan
	// /proc for the marker env inherited at exec and terminate those too.
	return killMarkedProcesses(marker)
}

func (m *processManager) shutdown(ctx context.Context) {
	m.stopOnce.Do(func() { close(m.stopped) })
	// A shut-down init must not leave a reaper goroutine behind: wait4(-1) is
	// process-wide, so a surviving reaper could reap the next init instance
	// (tests/wrappers run more than one Run in one process) and destroy its
	// exit frames. Production PID 1 has no next instance, but the wait is
	// bounded and cheap there too.
	defer func() {
		select {
		case <-m.reaperDone:
		case <-ctx.Done():
		}
	}()
	m.mu.Lock()
	processes := make([]*process, 0, len(m.byID))
	for _, p := range m.byID {
		processes = append(processes, p)
	}
	m.mu.Unlock()
	for _, p := range processes {
		_ = p.signal(syscall.SIGTERM, true)
	}
	// PID 1 also reaps descendants that outlived their parent and therefore
	// never appear in byID (for example `sleep 300 &` from a shell that
	// exited). In a container every other process is ours, so signal the
	// whole namespace; the test/wrapper path (not PID 1) must not do this.
	if os.Getpid() == 1 {
		_ = syscall.Kill(-1, syscall.SIGTERM)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for _, p := range processes {
		select {
		case <-p.done:
		case <-deadline.C:
			for _, remaining := range processes {
				_ = remaining.signal(syscall.SIGKILL, true)
			}
			if os.Getpid() == 1 {
				_ = syscall.Kill(-1, syscall.SIGKILL)
			}
			return
		case <-ctx.Done():
			for _, remaining := range processes {
				_ = remaining.signal(syscall.SIGKILL, true)
			}
			if os.Getpid() == 1 {
				_ = syscall.Kill(-1, syscall.SIGKILL)
			}
			return
		}
	}
}
