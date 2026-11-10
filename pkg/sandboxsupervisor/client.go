package sandboxsupervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Identity is the attribution the init process sends with one relayed
// connection. The supervisor forwards it to the daemon proxy, which records it
// with the policy decision.
type Identity struct {
	SessionID string
	Marker    string
	PID       int
	Binary    string
}

// Client is the sandbox-init side of the supervisor protocol. It is used only
// by the trusted init process; the workload container never speaks this
// protocol directly.
type Client struct {
	SocketPath string
	Token      string
	SandboxID  string
	Timeout    time.Duration
}

// RelayedConn is a net.Conn whose reads come from the supervisor handshake
// reader, so bytes the supervisor sent immediately after its response are not
// lost.
type RelayedConn struct {
	net.Conn
	reader *bufio.Reader
}

// Read reads from the relayed stream.
func (c *RelayedConn) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}

// Ping verifies that the supervisor is listening and accepts this token.
func (c *Client) Ping(ctx context.Context) error {
	conn, _, err := c.handshake(ctx, Request{Op: OpPing})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return nil
}

// Connect authenticates one destination and returns the relayed stream. The
// caller owns the returned connection and must close it.
func (c *Client) Connect(ctx context.Context, destination string, identity Identity) (net.Conn, error) {
	normalized, err := NormalizeEndpoint(destination)
	if err != nil {
		return nil, err
	}
	conn, reader, err := c.handshake(ctx, Request{
		Op:          OpConnect,
		Destination: normalized,
		SessionID:   identity.SessionID,
		Marker:      identity.Marker,
		PID:         identity.PID,
		Binary:      identity.Binary,
	})
	if err != nil {
		return nil, err
	}
	return &RelayedConn{Conn: conn, reader: reader}, nil
}

func (c *Client) handshake(ctx context.Context, request Request) (net.Conn, *bufio.Reader, error) {
	if strings.TrimSpace(c.SocketPath) == "" {
		return nil, nil, errors.New("sandbox supervisor socket path is required")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = HandshakeTimeout
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: dial %s: %w", ErrSupervisorUnavailable, c.SocketPath, err)
	}
	fail := func(cause error) (net.Conn, *bufio.Reader, error) {
		_ = conn.Close()
		return nil, nil, cause
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return fail(err)
	}
	request.Version = ProtocolVersion
	request.Token = c.Token
	request.SandboxID = c.SandboxID
	if err := WriteRequest(conn, request); err != nil {
		return fail(err)
	}
	reader := bufio.NewReader(conn)
	response, err := ReadResponse(reader)
	if err != nil {
		return fail(fmt.Errorf("%w: %w", ErrSupervisorUnavailable, err))
	}
	if !response.OK {
		reason := strings.TrimSpace(response.Reason)
		if reason == "" {
			reason = "denied"
		}
		return fail(fmt.Errorf("%w: %s", ErrSupervisorUnavailable, reason))
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fail(err)
	}
	return conn, reader, nil
}
