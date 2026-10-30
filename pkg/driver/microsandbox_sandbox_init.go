//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

// microsandboxInitConn adapts one streaming microsandbox exec running
// `ac-init --client` to a net.Conn. The guest cannot expose a Unix socket to
// the host through virtiofs (the host sees a regular file), so the protocol
// travels over the exec's stdin/stdout like the Kubernetes pods/exec bridge.
type microsandboxInitConn struct {
	ctx     context.Context
	cancel  context.CancelFunc
	handle  *microsandbox.ExecHandle
	stdin   *microsandbox.ExecSink
	stdout  *io.PipeReader
	done    chan struct{}
	once    sync.Once
	onClose func()
}

func newMicrosandboxInitConn(ctx context.Context, sandbox *microsandbox.Sandbox, binary, socketPath string) (*microsandboxInitConn, error) {
	if sandbox == nil {
		return nil, errors.New("microsandbox sandbox is nil")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	handle, err := sandbox.ExecStream(streamCtx, binary,
		[]string{"--client", "--control-socket", socketPath},
		microsandbox.WithExecCwd("/"),
		microsandbox.WithExecStdinPipe(),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start microsandbox init client: %w", err)
	}
	stdin := handle.TakeStdin()
	stdoutR, stdoutW := io.Pipe()
	conn := &microsandboxInitConn{
		ctx:    streamCtx,
		cancel: cancel,
		handle: handle,
		stdin:  stdin,
		stdout: stdoutR,
		done:   make(chan struct{}),
	}
	go conn.pump(stdoutW)
	return conn, nil
}

func (c *microsandboxInitConn) pump(stdout *io.PipeWriter) {
	defer close(c.done)
	defer func() { _ = stdout.Close() }()
	for {
		event, err := c.handle.Recv(c.ctx)
		if err != nil {
			if c.ctx.Err() == nil {
				_ = stdout.CloseWithError(fmt.Errorf("microsandbox init client stream: %w", err))
			}
			return
		}
		switch event.Kind {
		case microsandbox.ExecEventStdout:
			if len(event.Data) == 0 {
				continue
			}
			if _, err := stdout.Write(event.Data); err != nil {
				return
			}
		case microsandbox.ExecEventStderr:
			// ac-init logs and bridge failures arrive here. They are not part
			// of the JSON protocol; a bridge failure is reported by the exit
			// path below. Keep the stream drained so the guest cannot block.
		case microsandbox.ExecEventFailed:
			message := "microsandbox init client failed to start"
			if event.Failure != nil && event.Failure.Message != "" {
				message = event.Failure.Message
			}
			_ = stdout.CloseWithError(errors.New(message))
			return
		case microsandbox.ExecEventExited, microsandbox.ExecEventDone:
			return
		}
	}
}

func (c *microsandboxInitConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *microsandboxInitConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *microsandboxInitConn) Close() error {
	c.once.Do(func() {
		c.cancel()
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		_ = c.handle.Close()
		_ = c.stdout.Close()
		<-c.done
		if c.onClose != nil {
			c.onClose()
		}
	})
	return nil
}

func (c *microsandboxInitConn) LocalAddr() net.Addr                { return microsandboxInitAddr("local") }
func (c *microsandboxInitConn) RemoteAddr() net.Addr               { return microsandboxInitAddr("remote") }
func (c *microsandboxInitConn) SetDeadline(t time.Time) error      { return c.setDeadline(t) }
func (c *microsandboxInitConn) SetReadDeadline(t time.Time) error  { return c.setDeadline(t) }
func (c *microsandboxInitConn) SetWriteDeadline(t time.Time) error { return c.setDeadline(t) }

func (c *microsandboxInitConn) setDeadline(t time.Time) error {
	// The SDK has no deadline surface. A deadline already in the past means
	// the caller canceled; closing the bridge unblocks the pending read.
	if !t.IsZero() && !t.After(time.Now()) {
		_ = c.Close()
	}
	return nil
}

type microsandboxInitAddr string

func (a microsandboxInitAddr) Network() string { return "microsandbox-exec" }
func (a microsandboxInitAddr) String() string  { return string(a) }
