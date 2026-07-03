//go:build linux || darwin

package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// Uses a short /tmp path directly: t.TempDir() exceeds the 104-byte unix
// socket path limit on macOS.
func TestIsTrustedUnixSocketConn(t *testing.T) {
	socketPath := filepath.Join("/tmp", fmt.Sprintf("ac-peercred-%d.sock", os.Getpid()))
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			t.Fatalf("close unix listener: %v", err)
		}
	}()
	defer func() {
		if err := os.Remove(socketPath); err != nil {
			t.Fatalf("remove unix socket: %v", err)
		}
	}()

	dialErr := make(chan error, 1)
	go func() {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			defer func() { _ = conn.Close() }()
		}
		dialErr <- err
	}()

	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept unix connection: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Fatalf("close unix connection: %v", err)
		}
	}()
	if err := <-dialErr; err != nil {
		t.Fatalf("dial unix socket: %v", err)
	}

	// Same-process peer shares our UID and must be trusted.
	if !isTrustedUnixSocketConn(conn) {
		t.Fatal("same-uid unix peer should be trusted")
	}

	// Non-unix connections must never be trusted.
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer func() {
		if err := tcpListener.Close(); err != nil {
			t.Fatalf("close tcp listener: %v", err)
		}
	}()
	go func() {
		conn, err := net.Dial("tcp", tcpListener.Addr().String())
		if err == nil {
			defer func() { _ = conn.Close() }()
		}
		dialErr <- err
	}()
	tcpConn, err := tcpListener.Accept()
	if err != nil {
		t.Fatalf("accept tcp connection: %v", err)
	}
	defer func() {
		if err := tcpConn.Close(); err != nil {
			t.Fatalf("close tcp connection: %v", err)
		}
	}()
	if err := <-dialErr; err != nil {
		t.Fatalf("dial tcp: %v", err)
	}
	if isTrustedUnixSocketConn(tcpConn) {
		t.Fatal("tcp connection must not be trusted")
	}
}
