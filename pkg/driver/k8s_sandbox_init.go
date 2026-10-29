//go:build k8scompose

package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

// k8sInitConn adapts one pods/exec stream to net.Conn. The exec runs
// `ac-init --client`, so the JSON protocol is carried over stdin/stdout and the
// workload still runs under init.
type k8sInitConn struct {
	ctx    context.Context
	cancel context.CancelFunc
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	done   chan struct{}
	once   sync.Once
}

func newK8sInitConn(ctx context.Context, executor remotecommand.Executor) net.Conn {
	streamCtx, cancel := context.WithCancel(ctx)
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	conn := &k8sInitConn{
		ctx:    streamCtx,
		cancel: cancel,
		stdin:  stdinW,
		stdout: stdoutR,
		done:   make(chan struct{}),
	}
	go func() {
		err := executor.StreamWithContext(streamCtx, remotecommand.StreamOptions{
			Stdin:  stdinR,
			Stdout: stdoutW,
			Stderr: io.Discard,
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			_ = stdoutW.CloseWithError(fmt.Errorf("k8s init client stream: %w", err))
		} else {
			_ = stdoutW.Close()
		}
		_ = stdinR.Close()
		close(conn.done)
	}()
	return conn
}

func (c *k8sInitConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *k8sInitConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *k8sInitConn) Close() error {
	c.once.Do(func() {
		c.cancel()
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		<-c.done
	})
	return nil
}

func (c *k8sInitConn) LocalAddr() net.Addr                { return k8sInitAddr("local") }
func (c *k8sInitConn) RemoteAddr() net.Addr               { return k8sInitAddr("remote") }
func (c *k8sInitConn) SetDeadline(t time.Time) error      { return c.setDeadline(t) }
func (c *k8sInitConn) SetReadDeadline(t time.Time) error  { return c.setDeadline(t) }
func (c *k8sInitConn) SetWriteDeadline(t time.Time) error { return c.setDeadline(t) }

func (c *k8sInitConn) setDeadline(t time.Time) error {
	// remotecommand has no deadline surface. A deadline already in the past
	// means the caller canceled; closing the bridge unblocks the read.
	if !t.IsZero() && !t.After(time.Now()) {
		_ = c.Close()
	}
	return nil
}

type k8sInitAddr string

func (a k8sInitAddr) Network() string { return "k8s-exec" }
func (a k8sInitAddr) String() string  { return string(a) }

func (r *k8sRuntime) sandboxInitClient(sandbox *Sandbox, vmState VMState) *sandboxinit.Client {
	return sandboxinit.NewClientWithDialer(sandboxInitGuestSocket, func(ctx context.Context) (net.Conn, error) {
		spec := ExecSpec{
			Command: r.sandboxInitBinary(),
			Args:    []string{"--client", "--control-socket", sandboxInitGuestSocket},
			Cwd:     "/",
		}
		executor, err := r.newExecutorFor(ctx, k8sExecRequest{Sandbox: sandbox, VMState: vmState, Spec: spec}, true)
		if err != nil {
			return nil, err
		}
		return newK8sInitConn(ctx, executor), nil
	})
}

func (r *k8sRuntime) waitForK8sSandboxInit(ctx context.Context, clientset kubernetes.Interface, namespace string, pod *corev1.Pod, sandbox *Sandbox, vmState VMState) error {
	timeout := 2 * time.Minute
	if r != nil && r.config != nil && r.config.SandboxStartTimeout > 0 {
		timeout = r.config.SandboxStartTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := r.sandboxInitClient(sandbox, vmState)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := client.Ping(waitCtx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		current, err := clientset.CoreV1().Pods(namespace).Get(waitCtx, pod.Name, metav1.GetOptions{})
		if err == nil && k8sPodIsTerminal(current) {
			return fmt.Errorf("k8s sandbox init did not become ready: pod %s is %s", pod.Name, current.Status.Phase)
		}
		select {
		case <-waitCtx.Done():
			if lastErr == nil {
				lastErr = waitCtx.Err()
			}
			return fmt.Errorf("k8s sandbox init did not become ready within %s: %w", timeout, lastErr)
		case <-ticker.C:
		}
	}
}

func (r *k8sRuntime) initExecWithStream(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	return runSandboxInitExec(ctx, r.sandboxInitClient(sandbox, vmState), sandbox.Summary.ID, r.config.GuestWorkspacePath, spec, stream)
}
