package sandboxinit

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigureEgressSendsTheTokenOverTheControlSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "init.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen init socket: %v", err)
	}
	defer func() { _ = listener.Close() }()
	received := make(chan Request, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var request Request
		if decodeErr := json.NewDecoder(conn).Decode(&request); decodeErr != nil {
			return
		}
		received <- request
		_ = json.NewEncoder(conn).Encode(Frame{Type: FrameConfigured})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := NewClient(socket).ConfigureEgress(ctx, "relay-token"); err != nil {
		t.Fatalf("ConfigureEgress() error = %v", err)
	}
	select {
	case request := <-received:
		if request.Type != RequestConfigureEgress || request.Token != "relay-token" || request.Version != Version {
			t.Fatalf("control request = %+v, want configure_egress with the relay token", request)
		}
	case <-ctx.Done():
		t.Fatal("the init server did not receive the configure request")
	}
}

func TestConfigureEgressSurfacesARejectedRequest(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "init.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen init socket: %v", err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var request Request
		_ = json.NewDecoder(conn).Decode(&request)
		_ = json.NewEncoder(conn).Encode(Frame{Type: FrameError, Message: "unknown init request"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = NewClient(socket).ConfigureEgress(ctx, "relay-token")
	if !errors.Is(err, ErrControlRejected) {
		t.Fatalf("ConfigureEgress() error = %v, want ErrControlRejected", err)
	}
}
