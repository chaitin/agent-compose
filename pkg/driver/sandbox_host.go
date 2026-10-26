package driver

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/driver/sandboxhost"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// sandboxHostProtocolCompatible reports whether the daemon can drive a host
// speaking the given protocol version. The daemon accepts the current version
// and the previous one (N and N-1).
func sandboxHostProtocolCompatible(version int64) bool {
	return version == sandboxHostProtocolVersion || version == sandboxHostProtocolVersion-1
}

// ServeSandboxHost constructs the VM runtimes in-process and serves the
// sandbox-host gRPC service on the configured unix socket until ctx is
// cancelled. It is the process body of `agent-compose sandbox-host`.
func ServeSandboxHost(ctx context.Context, config *appconfig.Config) error {
	server, err := newSandboxHostServer(config)
	if err != nil {
		return err
	}
	return serveSandboxHost(ctx, config.SandboxHostSocket, server)
}

func serveSandboxHost(ctx context.Context, socketPath string, server *sandboxHostServer) error {
	if server == nil {
		return fmt.Errorf("sandbox-host server is required")
	}
	if err := os.MkdirAll(socketDir(socketPath), 0o700); err != nil {
		return fmt.Errorf("prepare sandbox-host socket dir: %w", err)
	}
	if err := removeStaleSandboxHostSocket(socketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen sandbox-host socket %q: %w", socketPath, err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("chmod sandbox-host socket %q: %w", socketPath, err)
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(sandboxHostMaxMsgSize),
		grpc.MaxSendMsgSize(sandboxHostMaxMsgSize),
	)
	sandboxhost.RegisterSandboxHostServer(grpcServer, server)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- grpcServer.Serve(listener)
	}()
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		grpcServer.GracefulStop()
		return <-serveErr
	}
}

func socketDir(socketPath string) string {
	return filepath.Dir(socketPath)
}

func removeStaleSandboxHostSocket(socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("sandbox-host socket %q is already in use", socketPath)
	}
	if os.IsPermission(err) {
		return fmt.Errorf("probe sandbox-host socket %q: %w", socketPath, err)
	}
	if removeErr := os.Remove(socketPath); removeErr != nil && !os.IsNotExist(removeErr) {
		return fmt.Errorf("remove stale sandbox-host socket %q: %w", socketPath, removeErr)
	}
	return nil
}

// dialSandboxHost connects to a running sandbox-host and verifies the protocol
// version. The returned conn owns the transport and must be closed by the
// caller when the client is no longer needed.
func dialSandboxHost(ctx context.Context, socketPath string) (sandboxhost.SandboxHostClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(sandboxHostMaxMsgSize),
			grpc.MaxCallSendMsgSize(sandboxHostMaxMsgSize),
		),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create sandbox-host client: %w", err)
	}
	client := sandboxhost.NewSandboxHostClient(conn)
	handshakeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := client.Handshake(handshakeCtx, &sandboxhost.HandshakeRequest{})
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("sandbox-host handshake: %w", err)
	}
	if !sandboxHostProtocolCompatible(resp.GetProtocolVersion()) {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("incompatible sandbox-host protocol version %d (daemon supports %d and %d)", resp.GetProtocolVersion(), sandboxHostProtocolVersion, sandboxHostProtocolVersion-1)
	}
	return client, conn, nil
}
