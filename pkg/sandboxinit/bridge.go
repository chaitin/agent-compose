package sandboxinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// Bridge connects stdin/stdout to an init control socket. It is the guest-side
// shim used by drivers that cannot expose the socket to the daemon directly
// (Kubernetes pods are the first such driver): the daemon runs this helper
// through its native exec API and speaks the normal protocol over the helper's
// stdio.
func Bridge(ctx context.Context, socketPath string, stdin io.Reader, stdout io.Writer) error {
	if socketPath == "" {
		return errors.New("sandbox init socket path is empty")
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("dial sandbox init socket %s: %w", socketPath, err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var closeOnce sync.Once
	closeWrite := func() {
		closeOnce.Do(func() {
			if tcp, ok := conn.(*net.UnixConn); ok {
				_ = tcp.CloseWrite()
			}
		})
	}

	inputDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(conn, stdin)
		closeWrite()
		inputDone <- copyErr
	}()
	_, outputErr := io.Copy(stdout, conn)
	closeWrite()
	inputErr := <-inputDone
	if outputErr != nil {
		return fmt.Errorf("read sandbox init bridge output: %w", outputErr)
	}
	if inputErr != nil {
		return fmt.Errorf("write sandbox init bridge input: %w", inputErr)
	}
	return nil
}
