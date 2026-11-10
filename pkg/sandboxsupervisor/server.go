package sandboxsupervisor

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultDialTimeout bounds the supervisor's connection to the daemon proxy,
// the CONNECT handshake, and a direct engine-endpoint connection.
const DefaultDialTimeout = 10 * time.Second

// DefaultMaxConnections bounds concurrent relayed connections per sandbox. The
// supervisor is a separate process, so this is its own resource bound rather
// than a daemon-wide one.
const DefaultMaxConnections = 128

// proxyHeaderLimit bounds the CONNECT response header read from the daemon
// proxy.
const proxyHeaderLimit = 4096

// ErrSupervisorUnavailable reports a supervisor that cannot be started or is
// not answering its socket. Callers fail closed rather than starting a sandbox
// whose only egress path is already broken.
var ErrSupervisorUnavailable = errors.New("sandbox supervisor unavailable")

// ServerOptions configures one sandbox's supervisor.
type ServerOptions struct {
	// SocketPath is the host path of the Unix domain socket. The sandbox sees
	// the same socket through a bind mount.
	SocketPath string
	// Token authenticates the sandbox init process. It is never placed in the
	// workload environment.
	Token string
	// SandboxID is the sandbox this supervisor serves. A request naming
	// another sandbox is refused even if it somehow carries the token.
	SandboxID string
	// ProxyURL is the daemon forward proxy the supervisor relays through. It
	// carries the per-sandbox credential and never reaches the workload.
	ProxyURL string
	// DirectEndpoints are engine-owned "host:port" or "ip:port" destinations
	// the supervisor connects to directly instead of through the proxy. They
	// are fixed by the daemon at start, never by the guest.
	DirectEndpoints []string
	// Logger receives connection outcomes. Nil uses slog.Default.
	Logger *slog.Logger
	// DialTimeout overrides DefaultDialTimeout.
	DialTimeout time.Duration
	// MaxConnections overrides DefaultMaxConnections.
	MaxConnections int
}

// Server is one sandbox's egress supervisor.
type Server struct {
	options ServerOptions
	proxy   *proxyTarget
	direct  map[string]struct{}
	logger  *slog.Logger
	timeout time.Duration

	listener  net.Listener
	closeOnce sync.Once

	connectionsMu sync.Mutex
	connections   map[net.Conn]struct{}
	handlers      sync.WaitGroup
	slots         chan struct{}
}

// proxyTarget is the daemon forward proxy plus the sandbox credential the
// supervisor presents for it.
type proxyTarget struct {
	address    string
	authHeader string
}

// NewServer validates the supervisor configuration. It does not bind the
// socket; Run does.
func NewServer(options ServerOptions) (*Server, error) {
	if strings.TrimSpace(options.SocketPath) == "" {
		return nil, errors.New("sandbox supervisor socket path is required")
	}
	if strings.TrimSpace(options.Token) == "" {
		return nil, errors.New("sandbox supervisor token is required")
	}
	if strings.TrimSpace(options.SandboxID) == "" {
		return nil, errors.New("sandbox supervisor sandbox id is required")
	}
	proxy, err := parseProxyTarget(options.ProxyURL)
	if err != nil {
		return nil, err
	}
	direct := make(map[string]struct{}, len(options.DirectEndpoints))
	for _, endpoint := range options.DirectEndpoints {
		normalized, normalizeErr := NormalizeEndpoint(endpoint)
		if normalizeErr != nil {
			return nil, fmt.Errorf("sandbox supervisor direct endpoint %q: %w", endpoint, normalizeErr)
		}
		direct[normalized] = struct{}{}
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	timeout := options.DialTimeout
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	limit := options.MaxConnections
	if limit <= 0 {
		limit = DefaultMaxConnections
	}
	return &Server{
		options:     options,
		proxy:       proxy,
		direct:      direct,
		logger:      logger,
		timeout:     timeout,
		connections: make(map[net.Conn]struct{}),
		slots:       make(chan struct{}, limit),
	}, nil
}

func parseProxyTarget(raw string) (*proxyTarget, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("sandbox supervisor proxy url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse sandbox supervisor proxy url: %w", err)
	}
	if parsed.Scheme != "http" {
		return nil, fmt.Errorf("sandbox supervisor proxy url must use http, got %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, errors.New("sandbox supervisor proxy url has no host")
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
	}
	if _, err := parsePort(port); err != nil {
		return nil, err
	}
	target := &proxyTarget{address: net.JoinHostPort(host, port)}
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		target.authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(parsed.User.Username()+":"+password))
	}
	return target, nil
}

// NormalizeEndpoint validates and canonicalizes a "host:port" destination.
func NormalizeEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("destination is required")
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("destination %q is not host:port: %w", raw, err)
	}
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if host == "" {
		return "", fmt.Errorf("destination %q has no host", raw)
	}
	if strings.ContainsAny(host, "\r\n\x00") {
		return "", fmt.Errorf("destination %q has an invalid host", raw)
	}
	value, err := parsePort(port)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(value)), nil
}

func parsePort(raw string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("destination port %q is not between 1 and 65535", raw)
	}
	return port, nil
}

// Run binds the socket and serves until ctx is cancelled. The socket is
// removed when Run returns.
func (s *Server) Run(ctx context.Context) error {
	if err := s.listen(); err != nil {
		return err
	}
	defer s.cleanupSocket()
	return s.serve(ctx)
}

func (s *Server) listen() error {
	path := strings.TrimSpace(s.options.SocketPath)
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return fmt.Errorf("create sandbox supervisor socket directory: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale sandbox supervisor socket %s: %w", path, err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen on sandbox supervisor socket %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("secure sandbox supervisor socket %s: %w", path, err)
	}
	s.listener = listener
	return nil
}

func dirOf(path string) string {
	index := strings.LastIndexByte(path, os.PathSeparator)
	if index <= 0 {
		return "."
	}
	return path[:index]
}

func (s *Server) cleanupSocket() {
	_ = os.Remove(s.options.SocketPath)
}

func (s *Server) serve(ctx context.Context) error {
	listener := s.listener
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-stop:
		}
	}()
	defer close(stop)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				s.handlers.Wait()
				return nil
			}
			return fmt.Errorf("accept sandbox supervisor connection: %w", err)
		}
		select {
		case s.slots <- struct{}{}:
		default:
			s.logger.Warn("sandbox supervisor connection limit reached", "sandbox_id", s.options.SandboxID)
			_ = conn.Close()
			continue
		}
		s.track(conn)
		s.handlers.Add(1)
		go func() {
			defer s.handlers.Done()
			defer func() { <-s.slots }()
			defer s.untrack(conn)
			defer func() { _ = conn.Close() }()
			s.handle(ctx, conn)
		}()
	}
}

// Close stops the listener and every relay. It is safe to call more than once.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		if s.listener != nil {
			_ = s.listener.Close()
		}
		s.connectionsMu.Lock()
		for conn := range s.connections {
			_ = conn.Close()
		}
		s.connectionsMu.Unlock()
	})
}

func (s *Server) track(conn net.Conn) {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()
	s.connections[conn] = struct{}{}
}

func (s *Server) untrack(conn net.Conn) {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()
	delete(s.connections, conn)
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	reader := bufio.NewReader(conn)
	_ = conn.SetDeadline(time.Now().Add(HandshakeTimeout))
	request, err := ReadRequest(reader)
	if err != nil {
		s.logger.Warn("sandbox supervisor rejected a malformed handshake", "sandbox_id", s.options.SandboxID, "error", err)
		_ = WriteResponse(conn, Response{Reason: "malformed handshake"})
		return
	}
	if !s.authenticate(request) {
		s.logger.Warn("sandbox supervisor rejected an unauthenticated handshake", "sandbox_id", s.options.SandboxID)
		_ = WriteResponse(conn, Response{Reason: "unauthorized"})
		return
	}
	switch request.Op {
	case OpPing:
		_ = WriteResponse(conn, Response{OK: true})
	case OpConnect:
		s.handleConnect(ctx, conn, reader, request)
	default:
		_ = WriteResponse(conn, Response{Reason: "unsupported operation"})
	}
}

func (s *Server) authenticate(request Request) bool {
	if request.Version != ProtocolVersion || strings.TrimSpace(request.Token) == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(request.Token), []byte(s.options.Token)) != 1 {
		return false
	}
	claimed := strings.TrimSpace(request.SandboxID)
	return claimed == "" || claimed == s.options.SandboxID
}
