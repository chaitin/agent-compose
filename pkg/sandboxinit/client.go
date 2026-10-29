package sandboxinit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// DefaultDialTimeout bounds one control-socket connection attempt.
const DefaultDialTimeout = 5 * time.Second

// Dialer opens one control connection. A nil Dialer means the client dials
// SocketPath over a Unix socket.
type Dialer func(context.Context) (net.Conn, error)

// Client is a portable control-socket client. It is safe to use from multiple
// goroutines: each operation opens its own connection, while an Exec Session
// has its own encoder lock and one reader.
type Client struct {
	SocketPath  string
	DialTimeout time.Duration
	// Dialer lets a driver bridge the protocol through its native exec API.
	// It is called once per operation; the returned conn must be full duplex.
	Dialer Dialer
}

// NewClient returns a client for socketPath. An empty path is an error at call
// time, not at construction, so a driver can build the client before it knows
// whether the sandbox uses init.
func NewClient(socketPath string) *Client {
	return &Client{SocketPath: socketPath, DialTimeout: DefaultDialTimeout}
}

// NewClientWithDialer returns a client whose connections are opened by dial.
// The sandbox ID and socket path are still useful for diagnostics.
func NewClientWithDialer(socketPath string, dial Dialer) *Client {
	return &Client{SocketPath: socketPath, DialTimeout: DefaultDialTimeout, Dialer: dial}
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	if c == nil {
		return nil, errors.New("sandbox init client is nil")
	}
	if c.Dialer != nil {
		conn, err := c.Dialer(ctx)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	if c.SocketPath == "" {
		return nil, errors.New("sandbox init socket path is empty")
	}
	timeout := c.DialTimeout
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("dial sandbox init socket %s: %w", c.SocketPath, err)
	}
	return conn, nil
}

// Exec starts one process. It returns after the server reports that the
// process started; output and exit are consumed with Session.Next.
func (c *Client) Exec(ctx context.Context, request Request) (*Session, error) {
	request.Version = Version
	request.Type = RequestExec
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)
	if err := writeJSON(encoder, request); err != nil {
		_ = conn.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	setDeadline(ctx, conn)
	var frame Frame
	if err := decoder.Decode(&frame); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read sandbox init exec response: %w", err)
	}
	clearDeadline(conn)
	if frame.Type == FrameError {
		_ = conn.Close()
		return nil, fmt.Errorf("start sandbox init process: %s", frame.Message)
	}
	if frame.Type != FrameStarted || frame.SessionID == "" {
		_ = conn.Close()
		return nil, fmt.Errorf("sandbox init returned %q without a session id", frame.Type)
	}
	return &Session{
		ID:      frame.SessionID,
		conn:    conn,
		encoder: encoder,
		decoder: decoder,
	}, nil
}

// Session is one running process and its control connection.
type Session struct {
	ID string

	conn    net.Conn
	encoder *json.Encoder
	decoder *json.Decoder

	writeMu sync.Mutex
	closed  bool
}

// Next reads the next frame. It returns io.EOF after the server closes the
// connection; the exit frame normally arrives before EOF.
func (s *Session) Next(ctx context.Context) (Frame, error) {
	if s == nil || s.decoder == nil {
		return Frame{}, errors.New("sandbox init session is not open")
	}
	stop := context.AfterFunc(ctx, func() { _ = s.conn.SetDeadline(time.Now()) })
	setDeadline(ctx, s.conn)
	var frame Frame
	err := s.decoder.Decode(&frame)
	stop()
	clearDeadline(s.conn)
	if err != nil {
		return Frame{}, err
	}
	return frame, nil
}

// SendStdin writes one stdin chunk.
func (s *Session) SendStdin(ctx context.Context, data []byte) error {
	return s.send(ctx, Request{Type: RequestStdin, Data: data})
}

// CloseStdin closes the child's stdin.
func (s *Session) CloseStdin(ctx context.Context) error {
	return s.send(ctx, Request{Type: RequestStdinEOF})
}

// Resize changes the PTY size. It is a no-op for a non-PTY session.
func (s *Session) Resize(ctx context.Context, rows, cols uint16) error {
	return s.send(ctx, Request{Type: RequestResize, Rows: rows, Cols: cols})
}

// Signal sends a signal to the session leader.
func (s *Session) Signal(ctx context.Context, signal string) error {
	return s.send(ctx, Request{Type: RequestSignal, Signal: signal})
}

func (s *Session) send(ctx context.Context, request Request) error {
	if s == nil || s.conn == nil {
		return errors.New("sandbox init session is not open")
	}
	request.Version = Version
	request.SessionID = s.ID
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return errors.New("sandbox init session is closed")
	}
	setDeadline(ctx, s.conn)
	err := writeJSON(s.encoder, request)
	clearDeadline(s.conn)
	if err != nil {
		return fmt.Errorf("write sandbox init request: %w", err)
	}
	return nil
}

// Close closes the control connection. It does not kill the process: the
// server keeps the process running so a detached task can survive a daemon
// disconnect. Callers that need termination send Signal or use Kill.
func (s *Session) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.conn.Close()
}

// Kill terminates one session. tree also terminates its process group.
func (c *Client) Kill(ctx context.Context, sessionID string, tree bool) error {
	return c.control(ctx, Request{Type: RequestKill, SessionID: sessionID, Tree: tree}, FrameKilled)
}

// KillTree terminates every process carrying marker. It is the init-backed
// replacement for Docker's marker control exec.
func (c *Client) KillTree(ctx context.Context, marker string) error {
	if marker == "" {
		return errors.New("sandbox init process-tree marker is required")
	}
	return c.control(ctx, Request{Type: RequestKillTree, Marker: marker}, FrameKilled)
}

// List returns the processes currently owned by init.
func (c *Client) List(ctx context.Context) ([]ProcessInfo, error) {
	frame, err := c.controlFrame(ctx, Request{Type: RequestList})
	if err != nil {
		return nil, err
	}
	if frame.Type != FrameProcesses {
		return nil, fmt.Errorf("sandbox init list returned %q", frame.Type)
	}
	return frame.Processes, nil
}

// Ping verifies that init is reachable and speaking this protocol version.
func (c *Client) Ping(ctx context.Context) error {
	return c.control(ctx, Request{Type: RequestPing}, FramePong)
}

// Shutdown asks init to terminate its children and exit.
func (c *Client) Shutdown(ctx context.Context) error {
	return c.control(ctx, Request{Type: RequestShutdown}, FrameShutdown)
}

func (c *Client) control(ctx context.Context, request Request, want string) error {
	frame, err := c.controlFrame(ctx, request)
	if err != nil {
		return err
	}
	if frame.Type != want {
		if frame.Type == FrameError {
			return fmt.Errorf("sandbox init: %s", frame.Message)
		}
		return fmt.Errorf("sandbox init returned %q, want %q", frame.Type, want)
	}
	return nil
}

func (c *Client) controlFrame(ctx context.Context, request Request) (Frame, error) {
	request.Version = Version
	conn, err := c.dial(ctx)
	if err != nil {
		return Frame{}, err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	setDeadline(ctx, conn)
	defer clearDeadline(conn)
	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)
	if err := writeJSON(encoder, request); err != nil {
		return Frame{}, err
	}
	var frame Frame
	if err := decoder.Decode(&frame); err != nil {
		return Frame{}, fmt.Errorf("read sandbox init response: %w", err)
	}
	return frame, nil
}

func writeJSON(encoder *json.Encoder, value any) error {
	if encoder == nil {
		return errors.New("sandbox init encoder is nil")
	}
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write sandbox init request: %w", err)
	}
	return nil
}

func setDeadline(ctx context.Context, conn net.Conn) {
	if conn == nil {
		return
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
}

func clearDeadline(conn net.Conn) {
	if conn != nil {
		_ = conn.SetDeadline(time.Time{})
	}
}
