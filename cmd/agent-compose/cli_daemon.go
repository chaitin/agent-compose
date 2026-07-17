package main

import (
	agentcomposeapp "agent-compose/pkg/agentcompose/app"
	"agent-compose/pkg/config"
	"agent-compose/pkg/fxgo/echofn"
	"agent-compose/pkg/fxgo/restful"
	"agent-compose/pkg/fxgo/utils"
	"agent-compose/pkg/health"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/lmittmann/tint"
	"github.com/samber/do/v2"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // h2c is required for unencrypted HTTP/2 compatibility with Connect bidi streams.
	"google.golang.org/grpc/codes"
)

type daemonRunner func(context.Context) error

type DaemonOptions struct {
	LoadDotEnv      bool
	SetRlimit       bool
	StartBackground func(do.Injector) error
	StopBackground  func(context.Context, do.Injector) error
}

type DaemonApp struct {
	DI              do.Injector
	Echo            *echo.Echo
	Logger          *slog.Logger
	Config          *config.Config
	startBackground func(do.Injector) error
	stopBackground  func(context.Context, do.Injector) error
	startOnce       sync.Once
	startErr        error
	shutdownTimeout time.Duration
}

type daemonServer struct {
	name     string
	value    string
	listener net.Listener
	server   *http.Server
	cleanup  func() error
}

type localUnixSocketRequestKey struct{}

type daemonServers struct {
	items []*daemonServer
}

func NewEcho(di do.Injector) (*echo.Echo, error) {
	e := echo.New()
	e.HTTPErrorHandler = echofn.EchoHTTPErrorHandler
	e.JSONSerializer = echofn.NewEpochTimeJSONSerializer()
	conf := do.MustInvoke[*config.Config](di)

	e.GET("/api/version", func(c echo.Context) error {
		now := time.Now()
		timezone, timezoneOffset := now.Zone()
		build := buildInfoForVersion(conf.Version)
		return c.JSON(http.StatusOK, restful.NewResponse[map[string]any, restful.StrStatusResp[map[string]any]](nil, codes.OK.String(), map[string]any{
			"version":          build.Version,
			"os":               build.OS,
			"arch":             build.Arch,
			"compiled_drivers": build.CompiledDrivers,
			"timestamp":        float64(now.UnixNano()) / 1e9,
			"timezone":         timezone,
			"timezone_offset":  timezoneOffset,
		}))
	})
	e.GET("/api/null", echofn.EchoWrap(restful.NullHandler[restful.StrStatusResp[any]]))
	return e, nil
}

func NewLogger(di do.Injector) (*slog.Logger, error) {
	logger := slog.New(tint.NewHandler(os.Stderr, &tint.Options{NoColor: false, AddSource: true, TimeFormat: "2006-01-02_15:04:05.000"}))
	slog.SetDefault(logger)
	return logger, nil
}

func NewDaemonApp(ctx context.Context, opts DaemonOptions) (*DaemonApp, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.LoadDotEnv {
		if err := godotenv.Load(); err != nil {
			log.Printf("dotenv load skipped: %v", err)
		}
	}
	if opts.SetRlimit {
		if err := utils.SetRlimitNoFile(); err != nil {
			log.Printf("Warning: Failed to set RLIMIT_NOFILE: %v", err)
		}
	}

	di := do.New()
	do.ProvideValue(di, ctx)
	do.Provide(di, NewLogger)
	config.Setup(di)
	do.Provide(di, NewEcho)
	health.Setup(di)
	agentcomposeapp.Register(di)

	app := do.MustInvoke[*echo.Echo](di)
	logger := do.MustInvoke[*slog.Logger](di)
	conf := do.MustInvoke[*config.Config](di)
	installDaemonMiddleware(app, conf)

	startBackground := opts.StartBackground
	stopBackground := opts.StopBackground
	if startBackground == nil {
		startBackground = agentcomposeapp.StartBackground
		if stopBackground == nil {
			stopBackground = agentcomposeapp.StopBackground
		}
	} else if stopBackground == nil {
		stopBackground = func(context.Context, do.Injector) error { return nil }
	}
	return &DaemonApp{
		DI:              di,
		Echo:            app,
		Logger:          logger,
		Config:          conf,
		startBackground: startBackground,
		stopBackground:  stopBackground,
		shutdownTimeout: 10 * time.Second,
	}, nil
}

func installDaemonMiddleware(app *echo.Echo, conf *config.Config) {
	app.Use(middleware.RequestLogger())
	app.Use(middleware.Recover())
	app.Use(newDaemonAuthMiddleware(conf))
}

func (a *DaemonApp) StartBackground() error {
	a.startOnce.Do(func() {
		a.startErr = a.startBackground(a.DI)
	})
	return a.startErr
}

func (a *DaemonApp) StopBackground(ctx context.Context) error {
	if a == nil || a.stopBackground == nil {
		return nil
	}
	return a.stopBackground(ctx, a.DI)
}

func (a *DaemonApp) Run(ctx context.Context) error {
	servers, err := a.listen()
	if err != nil {
		return err
	}

	if err := a.StartBackground(); err != nil {
		if shutdownErr := a.shutdown(servers); shutdownErr != nil {
			err = errors.Join(err, shutdownErr)
		}
		return err
	}

	serverErrCh := servers.serve(a.Logger)
	select {
	case err := <-serverErrCh:
		if err != nil {
			a.Logger.Error("agent-compose server failed", "error", err)
			if shutdownErr := a.shutdown(servers); shutdownErr != nil {
				err = errors.Join(err, shutdownErr)
			}
			return err
		}
	case <-ctx.Done():
		a.Logger.Info("shutdown requested", "error", ctx.Err())
	}

	if err := a.shutdown(servers); err != nil {
		a.Logger.Error("failed to shutdown agent-compose server", "error", err)
		return err
	}
	return nil
}

func (a *DaemonApp) shutdown(servers *daemonServers) error {
	var joined error
	serverCtx, cancelServers := context.WithTimeout(context.Background(), a.effectiveShutdownTimeout())
	if err := servers.shutdown(serverCtx); err != nil {
		joined = errors.Join(joined, err)
	}
	cancelServers()

	backgroundCtx, cancelBackground := context.WithTimeout(context.Background(), a.effectiveShutdownTimeout())
	if err := a.StopBackground(backgroundCtx); err != nil {
		joined = errors.Join(joined, fmt.Errorf("stop background managers: %w", err))
	}
	cancelBackground()
	return joined
}

func (a *DaemonApp) effectiveShutdownTimeout() time.Duration {
	if a != nil && a.shutdownTimeout > 0 {
		return a.shutdownTimeout
	}
	return 10 * time.Second
}

func (a *DaemonApp) listen() (*daemonServers, error) {
	servers := &daemonServers{}

	unixListener, err := listenUnixSocket(a.Config.AgentComposeSocket)
	if err != nil {
		return nil, err
	}
	servers.add("AGENT_COMPOSE_SOCKET", a.Config.AgentComposeSocket, unixListener, a.Echo, func() error {
		return os.Remove(a.Config.AgentComposeSocket)
	})

	if a.Config.HttpListen != "" {
		tcpListener, err := net.Listen("tcp", a.Config.HttpListen)
		if err != nil {
			shutdownErr := servers.shutdown(context.Background())
			return nil, errors.Join(fmt.Errorf("listen HTTP_LISTEN %q: %w", a.Config.HttpListen, err), shutdownErr)
		}
		if !isLoopbackListenAddress(a.Config.HttpListen) {
			a.Logger.Warn("HTTP_LISTEN exposes unencrypted h2c traffic; use a TLS-terminating reverse proxy before exposing this listener", "address", a.Config.HttpListen)
		}
		servers.add("HTTP_LISTEN", a.Config.HttpListen, tcpListener, a.Echo, nil)
	}

	return servers, nil
}

func isLoopbackListenAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *daemonServers) add(name, value string, listener net.Listener, handler http.Handler, cleanup func() error) {
	server := &http.Server{Handler: h2c.NewHandler(handler, &http2.Server{})} //nolint:staticcheck // h2c is required for unencrypted HTTP/2 compatibility with Connect bidi streams.
	if listener.Addr().Network() == "unix" {
		server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
			if isTrustedUnixSocketConn(conn) {
				return context.WithValue(ctx, localUnixSocketRequestKey{}, true)
			}
			return ctx
		}
	}
	s.items = append(s.items, &daemonServer{
		name:     name,
		value:    value,
		listener: listener,
		server:   server,
		cleanup:  cleanup,
	})
}

// isTrustedUnixSocketConn anchors the auth bypass on the peer's identity
// (SO_PEERCRED / LOCAL_PEERCRED) rather than on socket file permissions alone:
// only the daemon's own user and root get CLI access without credentials.
// Unknown peers (lookup failure, unsupported platform) fall back to normal
// authentication.
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

func (s *daemonServers) serve(logger *slog.Logger) <-chan error {
	errCh := make(chan error, len(s.items))
	for _, item := range s.items {
		go func(item *daemonServer) {
			logger.Info("agent-compose listener started", "config", item.name, "addr", item.listener.Addr().String())
			err := item.server.Serve(item.listener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("serve %s %q: %w", item.name, item.value, err)
				return
			}
			errCh <- nil
		}(item)
	}
	return errCh
}

func (s *daemonServers) shutdown(ctx context.Context) error {
	var joined error
	for _, item := range s.items {
		if err := item.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			joined = errors.Join(joined, fmt.Errorf("shutdown %s %q: %w", item.name, item.value, err))
		}
		if err := item.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			joined = errors.Join(joined, fmt.Errorf("close %s %q: %w", item.name, item.value, err))
		}
		if item.cleanup != nil {
			if err := item.cleanup(); err != nil && !errors.Is(err, os.ErrNotExist) {
				joined = errors.Join(joined, fmt.Errorf("cleanup %s %q: %w", item.name, item.value, err))
			}
		}
	}
	return joined
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

func runDaemon(ctx context.Context) error {
	app, err := NewDaemonApp(ctx, DaemonOptions{LoadDotEnv: true, SetRlimit: true})
	if err != nil {
		return err
	}
	return app.Run(ctx)
}

func fetchDaemonVersion(ctx context.Context, clientConfig cliClientConfig) ([]byte, error) {
	client := newDaemonHTTPClient(clientConfig)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientConfig.BaseURL+"/api/version", nil)
	if err != nil {
		return nil, fmt.Errorf("create daemon request for %s %q: %w", clientConfig.Source, clientConfig.SourceValue, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, commandExitError{Code: exitCodeUnavailable, Err: fmt.Errorf("connect daemon via %s %q: %w", clientConfig.Source, clientConfig.SourceValue, err)}
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read daemon response from %s %q: %w", clientConfig.Source, clientConfig.SourceValue, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, daemonHTTPStatusError{
			Source:      clientConfig.Source,
			SourceValue: clientConfig.SourceValue,
			StatusCode:  resp.StatusCode,
			Body:        strings.TrimSpace(string(body)),
		}
	}
	return body, nil
}

type daemonStatusResponse struct {
	Err  json.RawMessage `json:"err"`
	Msg  string          `json:"msg"`
	Data struct {
		Timestamp       float64  `json:"timestamp"`
		Timezone        string   `json:"timezone"`
		TimezoneOffset  *int     `json:"timezone_offset"`
		Version         string   `json:"version"`
		OS              string   `json:"os"`
		Arch            string   `json:"arch"`
		CompiledDrivers []string `json:"compiled_drivers"`
	} `json:"data"`
}

func writeDaemonStatusText(out io.Writer, body []byte) error {
	var response daemonStatusResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decode daemon status response: %w", err)
	}

	status := strings.TrimSpace(response.Msg)
	if status == "" {
		status = "unknown"
	}
	if len(response.Err) > 0 && string(response.Err) != "null" {
		status = "error"
	}
	uptime := "-"
	if response.Data.Timestamp > 0 {
		uptime = formatDaemonStatusTime(response.Data.Timestamp, response.Data.Timezone, response.Data.TimezoneOffset)
	}
	version := strings.TrimSpace(response.Data.Version)
	if version == "" {
		version = "-"
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "STATUS\tUPTIME\tVERSION"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", status, uptime, version); err != nil {
		return err
	}
	return tw.Flush()
}

func formatDaemonStatusTime(timestamp float64, timezone string, timezoneOffset *int) string {
	location := time.UTC
	if timezoneOffset != nil {
		name := strings.TrimSpace(timezone)
		if name == "" {
			name = "UTC"
		}
		location = time.FixedZone(name, *timezoneOffset)
	}
	return time.Unix(0, int64(timestamp*float64(time.Second))).In(location).Format("2006-01-02 15:04:05 MST -0700")
}

func newDaemonHTTPClient(clientConfig cliClientConfig) *http.Client {
	return newDaemonHTTPClientWithTimeout(clientConfig, 10*time.Minute)
}

func newDaemonAttachHTTPClient(clientConfig cliClientConfig) *http.Client {
	return newDaemonHTTPClientWithTimeout(clientConfig, 0)
}

func newDaemonHTTPClientWithTimeout(clientConfig cliClientConfig, timeout time.Duration) *http.Client {
	roundTripper := http.RoundTripper(newDaemonBaseRoundTripper(clientConfig))
	if !clientConfig.UseUnixSocket && clientConfig.AuthToken != "" {
		roundTripper = bearerAuthRoundTripper{token: clientConfig.AuthToken, next: roundTripper}
	}
	return &http.Client{
		Transport: roundTripper,
		Timeout:   timeout,
	}
}

func newDaemonBaseRoundTripper(clientConfig cliClientConfig) http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if clientConfig.UseUnixSocket {
		socketPath := clientConfig.SocketPath
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		}
	}
	if !clientConfig.UseUnixSocket && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(clientConfig.BaseURL)), "http://") {
		return transport
	}
	h2cTransport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			if clientConfig.UseUnixSocket {
				network = "unix"
				addr = clientConfig.SocketPath
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, addr)
		},
	}
	return daemonAttachRoundTripper{
		defaultTransport: transport,
		attachTransport:  h2cTransport,
	}
}

type daemonAttachRoundTripper struct {
	defaultTransport http.RoundTripper
	attachTransport  http.RoundTripper
}

func (t daemonAttachRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if isAttachRPCPath(req.URL.Path) {
		return t.attachTransport.RoundTrip(req)
	}
	return t.defaultTransport.RoundTrip(req)
}
