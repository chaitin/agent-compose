//go:build linux

package sandboxinit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Options configures the init server.
type Options struct {
	// ControlSocket is the Unix socket the daemon connects to. It must be an
	// absolute path inside the sandbox.
	ControlSocket string
	// MainShell, when set, is run as /bin/sh -lc <MainShell>. It is the
	// sandbox's keepalive or Jupyter launcher.
	MainShell string
	// MainCommand is used when MainShell is empty. The first element is the
	// program and the rest are arguments.
	MainCommand []string
	// Hooks is the future isolation extension point.
	Hooks Hooks
}

// ExitError reports the main process exit status so the init binary can exit
// with the same code.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("sandbox main process exited with code %d", e.Code)
}

// Run starts the init server, launches the main process, and blocks until the
// main process exits or ctx is canceled. It is the entry point for
// cmd/ac-init.
func Run(ctx context.Context, opts Options) error {
	socketPath := strings.TrimSpace(opts.ControlSocket)
	if socketPath == "" {
		return errors.New("sandbox init control socket is required")
	}
	if !filepath.IsAbs(socketPath) {
		return fmt.Errorf("sandbox init control socket must be absolute: %s", socketPath)
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return fmt.Errorf("create sandbox init control directory: %w", err)
	}
	// The socket is unique to this container/init generation. Removing a
	// stale path prevents a container restart from inheriting a dead socket.
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale sandbox init socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on sandbox init socket: %w", err)
	}
	// The parent directory is the access gate: the daemon creates it with
	// mode 0700 and only the daemon user and sandbox root can traverse it.
	// The socket itself is 0666 so a native daemon that is not root can connect
	// to a socket created by the container's root init process.
	if err := os.Chmod(socketPath, 0o666); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return fmt.Errorf("set sandbox init socket mode: %w", err)
	}

	// A container init is PID 1 and already receives orphans. When the same
	// binary is run in a test or a supervisor wrapper, become a subreaper so a
	// daemonized grandchild is still reparented here and reaped.
	if os.Getpid() != 1 {
		if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
			_ = listener.Close()
			_ = os.Remove(socketPath)
			return fmt.Errorf("become sandbox init subreaper: %w", err)
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	manager := newProcessManager(opts.Hooks)
	go manager.runReaper()

	server := &initServer{
		listener: listener,
		manager:  manager,
		stop:     cancel,
	}
	go server.acceptLoop(runCtx)

	mainDone := make(chan struct{})
	var mainProcess *process
	if command, args, ok := mainInvocation(opts); ok {
		p, err := manager.start(context.Background(), Request{
			Command: command,
			Args:    args,
		}, true, os.Stdout, os.Stderr)
		if err != nil {
			_ = listener.Close()
			_ = os.Remove(socketPath)
			return err
		}
		mainProcess = p
		go func() {
			<-p.done
			close(mainDone)
		}()
	}

	select {
	case <-runCtx.Done():
	case <-mainDone:
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	manager.shutdown(shutdownCtx)
	_ = listener.Close()
	_ = os.Remove(socketPath)
	if mainProcess != nil && mainProcess.exitCode != 0 {
		return &ExitError{Code: mainProcess.exitCode}
	}
	return nil
}

func mainInvocation(opts Options) (string, []string, bool) {
	if shell := strings.TrimSpace(opts.MainShell); shell != "" {
		return "/bin/sh", []string{"-lc", shell}, true
	}
	if len(opts.MainCommand) > 0 && strings.TrimSpace(opts.MainCommand[0]) != "" {
		return opts.MainCommand[0], append([]string(nil), opts.MainCommand[1:]...), true
	}
	return "", nil, false
}

type initServer struct {
	listener net.Listener
	manager  *processManager
	stop     context.CancelFunc
	stopOnce sync.Once
}

func (s *initServer) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *initServer) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	decoder := newFrameDecoder(conn)
	encoder := newFrameEncoder(conn)
	var request Request
	if err := decoder.Decode(&request); err != nil {
		_ = encoder.Encode(Frame{Type: FrameError, Message: fmt.Sprintf("decode request: %v", err)})
		return
	}
	if request.Version != Version {
		_ = encoder.Encode(Frame{Type: FrameError, Message: fmt.Sprintf("unsupported init protocol version %d, want %d", request.Version, Version)})
		return
	}
	switch request.Type {
	case RequestExec:
		s.handleExec(ctx, conn, decoder, encoder, request)
	case RequestKill:
		if err := s.manager.kill(request.SessionID, request.Tree); err != nil {
			_ = encoder.Encode(Frame{Type: FrameError, Message: err.Error()})
			return
		}
		_ = encoder.Encode(Frame{Type: FrameKilled})
	case RequestKillTree:
		if err := s.manager.killTree(request.Marker); err != nil {
			_ = encoder.Encode(Frame{Type: FrameError, Message: err.Error()})
			return
		}
		_ = encoder.Encode(Frame{Type: FrameKilled})
	case RequestList:
		_ = encoder.Encode(Frame{Type: FrameProcesses, Processes: s.manager.list()})
	case RequestPing:
		_ = encoder.Encode(Frame{Type: FramePong})
	case RequestShutdown:
		_ = encoder.Encode(Frame{Type: FrameShutdown})
		s.stopOnce.Do(s.stop)
	default:
		_ = encoder.Encode(Frame{Type: FrameError, Message: fmt.Sprintf("unknown init request %q", request.Type)})
	}
}

func (s *initServer) handleExec(ctx context.Context, conn net.Conn, decoder *frameDecoder, encoder *frameEncoder, request Request) {
	p, err := s.manager.start(ctx, request, false, nil, nil)
	if err != nil {
		_ = encoder.Encode(Frame{Type: FrameError, Message: err.Error()})
		return
	}
	if err := encoder.Encode(Frame{Type: FrameStarted, SessionID: p.id, Processes: []ProcessInfo{{ID: p.id, PID: p.pid}}}); err != nil {
		p.detach()
		return
	}
	// The writer forwards child output. On a write error the client is gone;
	// detaching makes the child's output drain to io.Discard and keeps the
	// process alive for a detach/reattach flow.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case frame, ok := <-p.frames:
				if !ok {
					return
				}
				if err := encoder.Encode(frame); err != nil {
					p.detach()
					return
				}
			case <-p.discard:
				return
			}
		}
	}()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			var next Request
			if err := decoder.Decode(&next); err != nil {
				if !errors.Is(err, io.EOF) {
					_ = encoder.Encode(Frame{Type: FrameError, Message: fmt.Sprintf("decode request: %v", err)})
				}
				p.detach()
				return
			}
			switch next.Type {
			case RequestStdin:
				if p.stdin != nil {
					_, _ = p.stdin.Write(next.Data)
				}
			case RequestStdinEOF:
				if closer, ok := p.stdin.(io.Closer); ok {
					_ = closer.Close()
				}
			case RequestResize:
				if p.master != nil {
					_ = ptySetsize(p.master, next.Rows, next.Cols)
				}
			case RequestSignal:
				_ = p.signal(signalByName(next.Signal), false)
			default:
				_ = encoder.Encode(Frame{Type: FrameError, Message: fmt.Sprintf("unknown session request %q", next.Type)})
			}
		}
	}()
	select {
	case <-p.done:
		<-writerDone
	case <-readerDone:
		p.detach()
	case <-ctx.Done():
		_ = p.signal(syscall.SIGKILL, true)
		p.detach()
	}
	_ = conn.Close()
}

func signalByName(name string) syscall.Signal {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "KILL", "SIGKILL":
		return syscall.SIGKILL
	case "INT", "SIGINT":
		return syscall.SIGINT
	case "HUP", "SIGHUP":
		return syscall.SIGHUP
	default:
		return syscall.SIGTERM
	}
}

// frameEncoder writes one JSON line per frame. The exec writer goroutine and
// the request reader can both report errors, so writes are serialized.
type frameEncoder struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func newFrameEncoder(w io.Writer) *frameEncoder {
	return &frameEncoder{enc: json.NewEncoder(w)}
}

func (e *frameEncoder) Encode(value any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enc.Encode(value)
}

// frameDecoder wraps a persistent JSON decoder so buffered bytes from one
// request are not lost before the next request is read.
type frameDecoder struct {
	dec *json.Decoder
}

func newFrameDecoder(r io.Reader) *frameDecoder {
	return &frameDecoder{dec: json.NewDecoder(r)}
}

func (d *frameDecoder) Decode(value any) error {
	return d.dec.Decode(value)
}
