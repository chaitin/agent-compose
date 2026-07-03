package daemon

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type localUnixSocketRequestKey struct{}

func isTrustedUnixSocketConn(conn net.Conn) bool {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	uid, err := unixSocketPeerUID(unixConn)
	if err != nil {
		return false
	}
	return uid == os.Getuid() || uid == 0
}

func isLocalUnixSocketRequest(r *http.Request) bool {
	ok, _ := r.Context().Value(localUnixSocketRequestKey{}).(bool)
	return ok
}

func listenUnixSocket(path string) (net.Listener, error) {
	if path == "" {
		return nil, fmt.Errorf("AGENT_COMPOSE_SOCKET is empty")
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("prepare AGENT_COMPOSE_SOCKET %q: create parent %q: %w", path, parent, err)
	}

	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("prepare AGENT_COMPOSE_SOCKET %q: path exists and is not a Unix socket", path)
		}
		if err := removeStaleUnixSocket(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("prepare AGENT_COMPOSE_SOCKET %q: stat socket path: %w", path, err)
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen AGENT_COMPOSE_SOCKET %q: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		closeErr := listener.Close()
		removeErr := os.Remove(path)
		return nil, errors.Join(fmt.Errorf("prepare AGENT_COMPOSE_SOCKET %q: chmod socket: %w", path, err), closeErr, removeErr)
	}
	return listener, nil
}

func removeStaleUnixSocket(path string) error {
	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		if closeErr := conn.Close(); closeErr != nil {
			return fmt.Errorf("prepare AGENT_COMPOSE_SOCKET %q: close active socket probe: %w", path, closeErr)
		}
		return fmt.Errorf("prepare AGENT_COMPOSE_SOCKET %q: socket already in use", path)
	}
	if os.IsPermission(err) {
		return fmt.Errorf("prepare AGENT_COMPOSE_SOCKET %q: probe socket: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("prepare AGENT_COMPOSE_SOCKET %q: remove stale socket: %w", path, err)
	}
	return nil
}
