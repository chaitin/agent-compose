package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/joho/godotenv"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/lmittmann/tint"
	"github.com/samber/do/v2"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"

	"agent-compose/pkg/fxgo/echofn"
	"agent-compose/pkg/fxgo/restful"
	"agent-compose/pkg/fxgo/utils"

	"agent-compose/pkg/agentcompose/api"
	agentcomposeapp "agent-compose/pkg/agentcompose/app"
	"agent-compose/pkg/auth"
	"agent-compose/pkg/compose"
	"agent-compose/pkg/config"
	"agent-compose/pkg/health"
	"agent-compose/pkg/projects"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type daemonRunner func(context.Context) error

type DaemonOptions struct {
	LoadDotEnv      bool
	SetRlimit       bool
	StartBackground func(do.Injector) error
}

type DaemonApp struct {
	DI              do.Injector
	Echo            *echo.Echo
	Logger          *slog.Logger
	Config          *config.Config
	startBackground func(do.Injector) error
	startOnce       sync.Once
	startErr        error
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

type cliClientConfig struct {
	BaseURL       string
	SocketPath    string
	Source        string
	SourceValue   string
	UseUnixSocket bool
	AuthUsername  string
	AuthPassword  string
}

func NewEcho(di do.Injector) (*echo.Echo, error) {
	e := echo.New()
	e.HTTPErrorHandler = echofn.EchoHTTPErrorHandler
	e.JSONSerializer = echofn.NewEpochTimeJSONSerializer()
	conf := do.MustInvoke[*config.Config](di)

	e.GET("/api/version", func(c echo.Context) error {
		return c.JSON(http.StatusOK, restful.NewResponse[map[string]any, restful.StrStatusResp[map[string]any]](nil, codes.OK.String(), map[string]any{
			"version":   conf.Version,
			"timestamp": float64(time.Now().UnixNano()) / 1e9,
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
	if startBackground == nil {
		startBackground = agentcomposeapp.StartBackground
	}
	return &DaemonApp{
		DI:              di,
		Echo:            app,
		Logger:          logger,
		Config:          conf,
		startBackground: startBackground,
	}, nil
}

func installDaemonMiddleware(app *echo.Echo, conf *config.Config) {
	app.Use(middleware.RequestLogger())
	app.Use(middleware.Recover())
	authManager := auth.NewAuthManager(&auth.Config{
		AuthUsername:          conf.AuthUsername,
		AuthPassword:          conf.AuthPassword,
		AuthSecret:            conf.AuthSecret,
		AuthSessionTTL:        conf.AuthSessionTTL,
		OAuthAPIKey:           conf.OAuthAPIKey,
		OAuthSecret:           conf.OAuthSecret,
		OAuthScopes:           conf.OAuthScopes,
		OAuthCallbackURL:      conf.OAuthCallbackURL,
		OAuthAuthURL:          conf.OAuthAuthURL,
		OAuthTokenURL:         conf.OAuthTokenURL,
		OAuthUserInfoURL:      conf.OAuthUserInfoURL,
		OAuthClientAuthMethod: conf.OAuthClientAuthMethod,
		Bypass:                isLocalUnixSocketRequest,
		Skipper:               agentcomposeapp.IsRuntimeLLMFacadeRequest,
	})
	authManager.RegisterRoutes(app)
	app.Use(authManager.Middleware)

	if conf.HTTPBasicAuth != "" {
		username := conf.HTTPBasicAuth
		password := ""
		if i := strings.Index(conf.HTTPBasicAuth, ":"); i >= 0 {
			username = conf.HTTPBasicAuth[:i]
			password = conf.HTTPBasicAuth[i+1:]
		}
		app.Use(middleware.BasicAuthWithConfig(middleware.BasicAuthConfig{
			// Same local-trust rule as AuthManager: CLI requests over the Unix
			// socket skip basic auth too.
			Skipper: func(c echo.Context) bool {
				return isLocalUnixSocketRequest(c.Request()) || agentcomposeapp.IsRuntimeLLMFacadeRequest(c.Request())
			},
			Realm: "Password Required",
			Validator: func(u, p string, c echo.Context) (bool, error) {
				return u == username && p == password, nil
			},
		}))
	}
}

func (a *DaemonApp) StartBackground() error {
	a.startOnce.Do(func() {
		a.startErr = a.startBackground(a.DI)
	})
	return a.startErr
}

func (a *DaemonApp) Run(ctx context.Context) error {
	servers, err := a.listen()
	if err != nil {
		return err
	}

	if err := a.StartBackground(); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if shutdownErr := servers.shutdown(shutdownCtx); shutdownErr != nil {
			err = errors.Join(err, shutdownErr)
		}
		return err
	}

	serverErrCh := servers.serve(a.Logger)
	select {
	case err := <-serverErrCh:
		if err != nil {
			a.Logger.Error("agent-compose server failed", "error", err)
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if shutdownErr := servers.shutdown(shutdownCtx); shutdownErr != nil {
				err = errors.Join(err, shutdownErr)
			}
			return err
		}
	case <-ctx.Done():
		a.Logger.Info("shutdown requested", "error", ctx.Err())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := servers.shutdown(shutdownCtx); err != nil {
		a.Logger.Error("failed to shutdown agent-compose server", "error", err)
		return err
	}
	return nil
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
		servers.add("HTTP_LISTEN", a.Config.HttpListen, tcpListener, a.Echo, nil)
	}

	return servers, nil
}

func (s *daemonServers) add(name, value string, listener net.Listener, handler http.Handler, cleanup func() error) {
	server := &http.Server{Handler: handler}
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

func isLocalUnixSocketRequest(r *http.Request) bool {
	ok, _ := r.Context().Value(localUnixSocketRequestKey{}).(bool)
	return ok
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

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(executeCLI(ctx, os.Stdout, os.Stderr, os.Args[1:], runDaemon))
}

func executeCLI(ctx context.Context, out, errOut io.Writer, args []string, runDaemon daemonRunner) int {
	cmd := newRootCommand(out, errOut, runDaemon)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return commandExitCode(err)
	}
	return 0
}

func newRootCommand(out, errOut io.Writer, runDaemon daemonRunner) *cobra.Command {
	options := cliOptions{}
	root := &cobra.Command{
		Use:           "agent-compose",
		Short:         "agent-compose daemon and CLI",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.SetOut(out)
	root.SetErr(errOut)
	root.CompletionOptions.DisableDefaultCmd = true

	root.PersistentFlags().StringVar(&options.Host, "host", "", "Daemon HTTP endpoint")
	root.PersistentFlags().StringVarP(&options.ComposeFile, "file", "f", "", "Path to agent-compose.yml")
	root.PersistentFlags().StringVar(&options.ProjectName, "project-name", "", "Override compose project name")
	root.PersistentFlags().BoolVar(&options.JSON, "json", false, "Print machine-readable JSON")

	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the agent-compose daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemon(cmd.Context())
		},
	}

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print build version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), config.BuildVersion)
			return err
		},
	}

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Query daemon status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientConfig, err := resolveCLIClientConfig(options.Host)
			if err != nil {
				return err
			}
			body, err := fetchDaemonVersion(cmd.Context(), clientConfig)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(body))
			return err
		},
	}

	configOptions := composeConfigOptions{}
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Validate and print normalized compose config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeConfigCommand(cmd, options, configOptions)
		},
	}
	configCmd.Flags().BoolVar(&configOptions.Quiet, "quiet", false, "Only validate config")

	listOptions := composeListProjectsOptions{}
	listCmd := &cobra.Command{
		Use:   "ls",
		Short: "List daemon projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeListProjectsCommand(cmd, options, listOptions)
		},
	}
	listCmd.Flags().BoolVar(&listOptions.Verbose, "verbose", false, "Show more project details")
	listCmd.Flags().Uint32Var(&listOptions.Limit, "limit", 0, "Maximum number of projects to return")
	listCmd.Flags().Uint32Var(&listOptions.Offset, "offset", 0, "Project list offset")

	upCmd := &cobra.Command{
		Use:   "up",
		Short: "Apply the current compose project to the daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeUpCommand(cmd, options)
		},
	}

	downCmd := &cobra.Command{
		Use:   "down",
		Short: "Stop project schedulers and running sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeDownCommand(cmd, options)
		},
	}

	runOptions := composeRunOptions{}
	runCmd := &cobra.Command{
		Use:   "run <agent> [prompt...]",
		Short: "Run a project agent",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeRunCommand(cmd, options, runOptions, args)
		},
	}
	runCmd.Flags().StringVar(&runOptions.Prompt, "prompt", "", "Prompt to send to the agent")
	runCmd.Flags().StringVar(&runOptions.Trigger, "trigger", "", "Trigger to run for the agent")
	runCmd.Flags().StringVar(&runOptions.Command, "command", "", "Bash command to execute in the agent sandbox")
	runCmd.Flags().StringVar(&runOptions.SandboxID, "sandbox", "", "Reuse an existing sandbox")
	// Deprecated: use --sandbox instead.
	runCmd.Flags().StringVar(&runOptions.SessionID, "session-id", "", "Reuse an existing session")
	runCmd.Flags().BoolVar(&runOptions.KeepRunning, "keep-running", false, "Keep the session runtime running after completion")
	runCmd.Flags().BoolVar(&runOptions.Remove, "rm", false, "Remove the sandbox after a successful run")

	logsOptions := composeLogsOptions{}
	logsCmd := &cobra.Command{
		Use:   "logs [agent]",
		Short: "Print project run logs",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeLogsCommand(cmd, options, logsOptions, args)
		},
	}
	logsCmd.Flags().StringVar(&logsOptions.AgentName, "agent", "", "Filter logs by agent name")
	logsCmd.Flags().StringVar(&logsOptions.RunID, "run-id", "", "Filter logs by run id")
	logsCmd.Flags().StringVar(&logsOptions.SandboxID, "sandbox", "", "Filter logs by sandbox id")
	// Deprecated: use --sandbox instead.
	logsCmd.Flags().StringVar(&logsOptions.SessionID, "session-id", "", "Filter logs by session id")
	logsCmd.Flags().BoolVar(&logsOptions.Follow, "follow", false, "Follow running run output")
	logsCmd.Flags().IntVarP(&logsOptions.TailLines, "tail", "n", -1, "Show the last N lines of run output")
	logsCmd.Flags().BoolVarP(&logsOptions.Timestamp, "timestamp", "t", false, "Prefix text log lines with a run-level timestamp")

	psOptions := composePSOptions{}
	psCmd := &cobra.Command{
		Use:   "ps",
		Short: "List project sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposePSCommand(cmd, options, psOptions)
		},
	}
	psCmd.Flags().BoolVarP(&psOptions.All, "all", "a", false, "Show all recognizable sandboxes")
	psCmd.Flags().StringVar(&psOptions.Status, "status", "", "Filter sandboxes by status, comma-separated")
	psCmd.Flags().BoolVar(&psOptions.Verbose, "verbose", false, "Show more sandbox details")

	stopCmd := &cobra.Command{
		Use:   "stop <sandbox> [<sandbox N>]",
		Short: "Stop one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxActionCommand(cmd, options, "stop", "stopped", args)
		},
	}

	resumeCmd := &cobra.Command{
		Use:   "resume <sandbox> [<sandbox N>]",
		Short: "Resume one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxActionCommand(cmd, options, "resume", "resumed", args)
		},
	}

	removeSandboxOptions := composeSandboxRemoveOptions{}
	rmCmd := &cobra.Command{
		Use:   "rm <sandbox> [<sandbox N>]",
		Short: "Remove one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxRemoveCommand(cmd, options, removeSandboxOptions, args)
		},
	}
	rmCmd.Flags().BoolVar(&removeSandboxOptions.Force, "force", false, "Force remove running sandboxes")

	execOptions := composeExecOptions{}
	execCmd := &cobra.Command{
		Use:   "exec <sandbox> [command] [args...]",
		Short: "Execute a command in a running sandbox",
		Args:  composeExecArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeExecCommand(cmd, options, execOptions, args)
		},
	}
	// Deprecated: use `agent-compose exec <sandbox>` instead.
	execCmd.Flags().StringVar(&execOptions.AgentName, "agent", "", "Select a running session by agent")
	// Deprecated: use `agent-compose exec <sandbox>` instead.
	execCmd.Flags().StringVar(&execOptions.RunID, "run-id", "", "Execute in the session linked to a run")
	// Deprecated: use `agent-compose exec <sandbox>` instead.
	execCmd.Flags().StringVar(&execOptions.SessionID, "session-id", "", "Execute in a specific session")
	execCmd.Flags().StringVar(&execOptions.Command, "command", "", "Shell command to execute in the sandbox")
	execCmd.Flags().StringVar(&execOptions.Cwd, "cwd", "", "Guest working directory")

	imageListOptions := composeImageListOptions{}
	imagesCmd := &cobra.Command{
		Use:   "images",
		Short: "List daemon images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeImageListCommand(cmd, options, imageListOptions)
		},
	}
	addImageListFlags(imagesCmd, &imageListOptions)

	imageCmd := &cobra.Command{
		Use:   "image",
		Short: "Deprecated: use images, pull, rmi, or inspect image",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Deprecated: use top-level image commands instead.
			if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose image", "agent-compose images, agent-compose pull, agent-compose rmi, or agent-compose inspect image"); err != nil {
				return err
			}
			return cmd.Help()
		},
	}
	imageLSOptions := composeImageListOptions{}
	imageLSCmd := &cobra.Command{
		Use:   "ls",
		Short: "List daemon images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Deprecated: use `agent-compose images` instead.
			if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose image ls", "agent-compose images"); err != nil {
				return err
			}
			return runComposeImageListCommand(cmd, options, imageLSOptions)
		},
	}
	addImageListFlags(imageLSCmd, &imageLSOptions)

	pullOptions := composeImagePullOptions{}
	pullCmd := &cobra.Command{
		Use:   "pull [image]",
		Short: "Pull an image or all project images",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposePullCommand(cmd, options, pullOptions, args)
		},
	}
	addImagePullFlags(pullCmd, &pullOptions)
	imagePullOptions := composeImagePullOptions{}
	imagePullCmd := &cobra.Command{
		Use:   "pull <image>",
		Short: "Pull an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Deprecated: use `agent-compose pull <image>` instead.
			if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose image pull", "agent-compose pull"); err != nil {
				return err
			}
			return runComposeImagePullCommand(cmd, options, imagePullOptions, args[0])
		},
	}
	addImagePullFlags(imagePullCmd, &imagePullOptions)

	removeOptions := composeImageRemoveOptions{}
	rmiCmd := &cobra.Command{
		Use:   "rmi <image>",
		Short: "Remove an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeImageRemoveCommand(cmd, options, removeOptions, args[0])
		},
	}
	addImageRemoveFlags(rmiCmd, &removeOptions)
	imageRemoveOptions := composeImageRemoveOptions{}
	imageRemoveCmd := &cobra.Command{
		Use:   "rm <image>",
		Short: "Remove an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Deprecated: use `agent-compose rmi <image>` instead.
			if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose image rm", "agent-compose rmi"); err != nil {
				return err
			}
			return runComposeImageRemoveCommand(cmd, options, imageRemoveOptions, args[0])
		},
	}
	addImageRemoveFlags(imageRemoveCmd, &imageRemoveOptions)

	imageInspectCmd := &cobra.Command{
		Use:   "inspect <image>",
		Short: "Inspect an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Deprecated: use `agent-compose inspect image <image>` instead.
			if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose image inspect", "agent-compose inspect image"); err != nil {
				return err
			}
			return runComposeImageInspectCommand(cmd, options, args[0])
		},
	}
	imageCmd.AddCommand(imageLSCmd, imagePullCmd, imageRemoveCmd, imageInspectCmd)

	inspectCmd := &cobra.Command{
		Use:   "inspect <project|agent|run|sandbox|session|image> [name-or-id]",
		Short: "Inspect project, agent, run, sandbox, session, or image details",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeInspectCommand(cmd, options, args)
		},
	}

	root.AddCommand(daemonCmd, versionCmd, statusCmd, configCmd, listCmd, upCmd, downCmd, runCmd, logsCmd, psCmd, stopCmd, resumeCmd, rmCmd, execCmd, imagesCmd, imageCmd, pullCmd, rmiCmd, inspectCmd)
	return root
}

type cliOptions struct {
	Host        string
	ComposeFile string
	ProjectName string
	JSON        bool
}

type composeConfigOptions struct {
	Quiet bool
}

type composeListProjectsOptions struct {
	Verbose bool
	Limit   uint32
	Offset  uint32
}

type composeRunOptions struct {
	Prompt      string
	Trigger     string
	Command     string
	SessionID   string
	SandboxID   string
	KeepRunning bool
	Remove      bool
}

type composeLogsOptions struct {
	AgentName string
	RunID     string
	SessionID string
	SandboxID string
	TailLines int
	Follow    bool
	Timestamp bool
}

type composePSOptions struct {
	All     bool
	Status  string
	Verbose bool
}

type composeExecOptions struct {
	AgentName string
	RunID     string
	SessionID string
	Command   string
	Cwd       string
}

type composeSandboxActionOutput struct {
	Results []composeSandboxActionResult `json:"results"`
}

type composeSandboxActionResult struct {
	Sandbox string `json:"sandbox"`
	Status  string `json:"status"`
}

type composeSandboxRemoveOptions struct {
	Force bool
}

type composeImageListOptions struct {
	Query string
	All   bool
}

type composeImagePullOptions struct {
	Platform string
}

type composeImageRemoveOptions struct {
	Force         bool
	PruneChildren bool
}

func addImageListFlags(cmd *cobra.Command, options *composeImageListOptions) {
	cmd.Flags().StringVar(&options.Query, "query", "", "Filter images by reference")
	cmd.Flags().BoolVarP(&options.All, "all", "a", false, "Show all images")
}

func addImagePullFlags(cmd *cobra.Command, options *composeImagePullOptions) {
	cmd.Flags().StringVar(&options.Platform, "platform", "", "Pull platform as os/arch[/variant]")
}

func addImageRemoveFlags(cmd *cobra.Command, options *composeImageRemoveOptions) {
	cmd.Flags().BoolVar(&options.Force, "force", false, "Force image removal")
	cmd.Flags().BoolVar(&options.PruneChildren, "prune-children", false, "Remove untagged child images")
}

func runComposeConfigCommand(cmd *cobra.Command, cli cliOptions, options composeConfigOptions) error {
	_, normalized, err := loadNormalizedCompose(cli)
	if err != nil {
		return err
	}
	if options.Quiet {
		return nil
	}

	var data []byte
	if cli.JSON {
		data, err = normalized.MarshalCanonicalJSON(true)
	} else {
		data, err = normalized.MarshalCanonicalYAML(true)
	}
	if err != nil {
		return err
	}
	return writeCommandOutput(cmd.OutOrStdout(), data)
}

func runComposeListProjectsCommand(cmd *cobra.Command, cli cliOptions, options composeListProjectsOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	output, err := listProjects(cmd.Context(), clients.project, options)
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("list projects: %w", err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeProjectListText(cmd.OutOrStdout(), output.Projects, options.Verbose)
}

func listProjects(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, options composeListProjectsOptions) (composeProjectListOutput, error) {
	if options.Limit > 0 || options.Offset > 0 {
		return listProjectsPage(ctx, client, options.Offset, options.Limit)
	}
	return listAllProjects(ctx, client)
}

func listProjectsPage(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, offset, limit uint32) (composeProjectListOutput, error) {
	resp, err := client.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{
		Offset: offset,
		Limit:  limit,
	}))
	if err != nil {
		return composeProjectListOutput{}, err
	}
	msg := resp.Msg
	output := composeProjectListOutput{
		Projects:   make([]composeProjectListItem, 0, len(msg.GetProjects())),
		TotalCount: msg.GetTotalCount(),
		HasMore:    msg.GetHasMore(),
		NextOffset: msg.GetNextOffset(),
	}
	for _, project := range msg.GetProjects() {
		output.Projects = append(output.Projects, composeProjectListItemFromSummary(project))
	}
	return output, nil
}

func listAllProjects(ctx context.Context, client agentcomposev2connect.ProjectServiceClient) (composeProjectListOutput, error) {
	const pageSize uint32 = 200
	var output composeProjectListOutput
	for {
		offset := output.NextOffset
		resp, err := client.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{
			Offset: offset,
			Limit:  pageSize,
		}))
		if err != nil {
			return composeProjectListOutput{}, err
		}
		msg := resp.Msg
		output.TotalCount = msg.GetTotalCount()
		output.HasMore = msg.GetHasMore()
		output.NextOffset = msg.GetNextOffset()
		for _, project := range msg.GetProjects() {
			output.Projects = append(output.Projects, composeProjectListItemFromSummary(project))
		}
		if !msg.GetHasMore() {
			break
		}
		if msg.GetNextOffset() == offset {
			return composeProjectListOutput{}, fmt.Errorf("project list pagination did not advance")
		}
	}
	output.HasMore = false
	return output, nil
}

func runComposeUpCommand(cmd *cobra.Command, cli cliOptions) error {
	composePath, normalized, err := loadNormalizedCompose(cli)
	if err != nil {
		return err
	}
	specHash, err := normalized.Hash()
	if err != nil {
		return fmt.Errorf("%s: hash normalized compose spec: %w", composePath, err)
	}
	clientConfig, err := resolveCLIClientConfig(cli.Host)
	if err != nil {
		return err
	}
	client := agentcomposev2connect.NewProjectServiceClient(newDaemonHTTPClient(clientConfig), clientConfig.BaseURL)
	resp, err := client.ApplyProject(cmd.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: api.ProjectSpecToProto(normalized),
		Source: &agentcomposev2.ProjectSource{
			ComposePath: composePath,
			ProjectDir:  filepath.Dir(composePath),
		},
		ExpectedSpecHash: specHash,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("apply project %s: %w", normalized.Name, err))
	}
	msg := resp.Msg
	if len(msg.GetIssues()) > 0 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("apply project %s: %s", normalized.Name, formatProjectValidationIssues(msg.GetIssues()))}
	}
	if cli.JSON {
		data, err := json.MarshalIndent(composeUpOutputFromResponse(msg), "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeComposeUpText(cmd.OutOrStdout(), msg)
}

func runComposeDownCommand(cmd *cobra.Command, cli cliOptions) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.project.RemoveProject(cmd.Context(), connect.NewRequest(&agentcomposev2.RemoveProjectRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID},
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("down project %s: %w", normalized.Name, err))
	}
	output := composeDownOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
			return err
		}
	} else if err := writeComposeDownText(cmd.OutOrStdout(), output); err != nil {
		return err
	}
	if output.FailedSessionStops > 0 {
		return commandExitError{
			Code: exitCodeGeneral,
			Err:  fmt.Errorf("down project %s completed with %d session stop failure(s)", normalized.Name, output.FailedSessionStops),
		}
	}
	return nil
}

func sandboxActionArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("requires at least 1 sandbox")}
	}
	return nil
}

func runComposeSandboxActionCommand(cmd *cobra.Command, cli cliOptions, action, status string, sandboxes []string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	output := composeSandboxActionOutput{
		Results: make([]composeSandboxActionResult, 0, len(sandboxes)),
	}
	for _, sandbox := range sandboxes {
		sandbox = strings.TrimSpace(sandbox)
		if sandbox == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s requires non-empty sandbox", action)}
		}
		req := connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sandbox})
		switch action {
		case "stop":
			_, err = clients.session.StopSession(cmd.Context(), req)
		case "resume":
			_, err = clients.session.ResumeSession(cmd.Context(), req)
		default:
			return fmt.Errorf("unsupported sandbox action %q", action)
		}
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("%s sandbox %s: %w", action, sandbox, err))
		}
		output.Results = append(output.Results, composeSandboxActionResult{
			Sandbox: sandbox,
			Status:  status,
		})
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	for _, result := range output.Results {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s sandbox %s\n", result.Status, result.Sandbox); err != nil {
			return err
		}
	}
	return nil
}

func runComposeSandboxRemoveCommand(cmd *cobra.Command, cli cliOptions, options composeSandboxRemoveOptions, sandboxes []string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	output := composeSandboxActionOutput{
		Results: make([]composeSandboxActionResult, 0, len(sandboxes)),
	}
	for _, sandbox := range sandboxes {
		sandbox = strings.TrimSpace(sandbox)
		if sandbox == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("rm requires non-empty sandbox")}
		}
		if err := removeSandbox(cmd.Context(), clients.sandbox, sandbox, options.Force); err != nil {
			return commandExitErrorForConnect(fmt.Errorf("rm sandbox %s: %w", sandbox, err))
		}
		output.Results = append(output.Results, composeSandboxActionResult{
			Sandbox: sandbox,
			Status:  "removed",
		})
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	for _, result := range output.Results {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s sandbox %s\n", result.Status, result.Sandbox); err != nil {
			return err
		}
	}
	return nil
}

func removeSandbox(ctx context.Context, client agentcomposev2connect.SandboxServiceClient, sandboxID string, force bool) error {
	_, err := client.RemoveSandbox(ctx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{
		SandboxId: sandboxID,
		Force:     force,
	}))
	return err
}

func runComposeRunCommand(cmd *cobra.Command, cli cliOptions, options composeRunOptions, args []string) error {
	normalizedOptions, err := normalizeComposeRunOptions(cmd, options)
	if err != nil {
		return err
	}
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	agentName := strings.TrimSpace(args[0])
	prompt := strings.TrimSpace(normalizedOptions.Prompt)
	triggerID := strings.TrimSpace(normalizedOptions.Trigger)
	commandText := strings.TrimSpace(normalizedOptions.Command)
	if cmd.Flags().Changed("command") && commandText == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run --command requires a non-empty command")}
	}
	modeCount := 0
	for _, value := range []string{prompt, triggerID, commandText} {
		if value != "" {
			modeCount++
		}
	}
	if modeCount > 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run requires only one of --trigger, --prompt, or --command")}
	}
	if (triggerID != "" || prompt != "" || commandText != "") && len(args) > 1 {
		mode := "--prompt"
		if triggerID != "" {
			mode = "--trigger"
		} else if commandText != "" {
			mode = "--command"
		}
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run with %s does not accept legacy positional prompt arguments", mode)}
	}
	if prompt == "" && len(args) > 1 {
		// Deprecated: positional prompt arguments will become the trigger position in a future release.
		if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose run <agent> [prompt...]", "agent-compose run <agent> --prompt"); err != nil {
			return err
		}
		prompt = strings.Join(args[1:], " ")
	}
	clientConfig, err := resolveCLIClientConfig(cli.Host)
	if err != nil {
		return err
	}
	cleanupPolicy := agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_STOP_ON_COMPLETION
	if normalizedOptions.KeepRunning {
		cleanupPolicy = agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING
	}
	client := agentcomposev2connect.NewRunServiceClient(newDaemonHTTPClient(clientConfig), clientConfig.BaseURL)
	stream, err := client.RunAgentStream(cmd.Context(), connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       agentName,
		Prompt:          prompt,
		Command:         commandText,
		Source:          agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
		SessionId:       strings.TrimSpace(normalizedOptions.SessionID),
		TriggerId:       triggerID,
		CleanupPolicy:   cleanupPolicy,
		ClientRequestId: manualRunClientRequestID(normalized.Name, agentName, firstNonEmptyString(prompt, triggerID, commandText)),
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("run project %s agent %s: %w", normalized.Name, agentName, err))
	}
	var completed *agentcomposev2.RunSummary
	for stream.Receive() {
		event := stream.Msg()
		switch event.GetEventType() {
		case agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT:
			if cli.JSON {
				continue
			}
			target := cmd.OutOrStdout()
			if event.GetIsStderr() {
				target = cmd.ErrOrStderr()
			}
			if _, err := io.WriteString(target, event.GetChunk()); err != nil {
				return err
			}
		case agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED:
			completed = event.GetRun()
		}
	}
	if err := stream.Err(); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("run project %s agent %s: %w", normalized.Name, agentName, err))
	}
	if completed == nil {
		return fmt.Errorf("run project %s agent %s: stream completed without terminal run", normalized.Name, agentName)
	}
	detail, err := getRunDetail(cmd.Context(), client, projectID, completed.GetRunId())
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", completed.GetRunId(), normalized.Name, err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(composeRunOutputFromDetail(detail.Msg.GetRun()), "", "  ")
		if err != nil {
			return err
		}
		if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
			return err
		}
	}
	if runSummaryFailed(completed) {
		return commandExitError{Code: runSummaryExitCode(completed), Err: fmt.Errorf("run %s for project %s agent %s failed: %s", completed.GetRunId(), normalized.Name, agentName, firstNonEmptyString(completed.GetError(), runStatusText(completed.GetStatus())))}
	}
	if normalizedOptions.Remove {
		sandboxID := strings.TrimSpace(completed.GetSessionId())
		if sandboxID == "" && detail.Msg.GetRun() != nil {
			sandboxID = strings.TrimSpace(detail.Msg.GetRun().GetSummary().GetSessionId())
		}
		if sandboxID == "" {
			return fmt.Errorf("run %s for project %s agent %s completed without sandbox id for --rm", completed.GetRunId(), normalized.Name, agentName)
		}
		clients, err := newCLIServiceClients(cli)
		if err != nil {
			return err
		}
		if err := removeSandbox(cmd.Context(), clients.sandbox, sandboxID, true); err != nil {
			return commandExitErrorForConnect(fmt.Errorf("remove sandbox %s after run %s: %w", sandboxID, completed.GetRunId(), err))
		}
		if !cli.JSON {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "removed sandbox %s\n", sandboxID); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeComposeRunOptions(cmd *cobra.Command, options composeRunOptions) (composeRunOptions, error) {
	if cmd.Flags().Changed("session-id") {
		if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose run --session-id", "agent-compose run --sandbox"); err != nil {
			return options, err
		}
		if strings.TrimSpace(options.SandboxID) == "" {
			options.SandboxID = options.SessionID
		}
	}
	if strings.TrimSpace(options.SandboxID) != "" {
		options.SessionID = options.SandboxID
	}
	return options, nil
}

func runComposeLogsCommand(cmd *cobra.Command, cli cliOptions, options composeLogsOptions, args []string) error {
	if cli.JSON && options.Follow {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs --json cannot be combined with --follow")}
	}
	normalizedOptions, err := normalizeComposeLogsOptions(cmd, options, args)
	if err != nil {
		return err
	}
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clientConfig, err := resolveCLIClientConfig(cli.Host)
	if err != nil {
		return err
	}
	client := agentcomposev2connect.NewRunServiceClient(newDaemonHTTPClient(clientConfig), clientConfig.BaseURL)
	if strings.TrimSpace(normalizedOptions.RunID) != "" {
		run, err := getRunDetail(cmd.Context(), client, projectID, normalizedOptions.RunID)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", strings.TrimSpace(normalizedOptions.RunID), normalized.Name, err))
		}
		return writeLogsForRun(cmd.OutOrStdout(), run.Msg.GetRun(), cli.JSON, normalizedOptions)
	}
	return followOrPrintProjectLogs(cmd, cli, client, projectID, normalized.Name, normalizedOptions)
}

func normalizeComposeLogsOptions(cmd *cobra.Command, options composeLogsOptions, args []string) (composeLogsOptions, error) {
	if len(args) > 0 {
		if cmd.Flags().Changed("agent") {
			return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs agent can be specified either positionally or with --agent, not both")}
		}
		options.AgentName = args[0]
	}
	if cmd.Flags().Changed("session-id") {
		if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose logs --session-id", "agent-compose logs --sandbox"); err != nil {
			return options, err
		}
		if strings.TrimSpace(options.SandboxID) == "" {
			options.SandboxID = options.SessionID
		}
	}
	if strings.TrimSpace(options.SandboxID) != "" {
		options.SessionID = options.SandboxID
	}
	if options.TailLines < -1 {
		return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs --tail must be -1 or greater")}
	}
	return options, nil
}

func composeExecArgs(cmd *cobra.Command, args []string) error {
	if cmd.Flags().Changed("session-id") || cmd.Flags().Changed("run-id") || cmd.Flags().Changed("agent") {
		return nil
	}
	return cobra.MinimumNArgs(1)(cmd, args)
}

func runComposePSCommand(cmd *cobra.Command, cli cliOptions, options composePSOptions) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	project, err := clients.project.GetProject(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID},
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("get project %s: %w", normalized.Name, err))
	}
	output, err := composePSOutputFromProject(cmd.Context(), clients, project.Msg.GetProject(), options)
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("build ps for project %s: %w", normalized.Name, err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writePSText(cmd.OutOrStdout(), output, options.Verbose)
}

func runComposeExecCommand(cmd *cobra.Command, cli cliOptions, options composeExecOptions, args []string) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	req, err := normalizeComposeExecRequest(cmd, normalized.Name, projectID, options, args)
	if err != nil {
		return err
	}
	stream, err := clients.exec.ExecStream(cmd.Context(), connect.NewRequest(req))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s: %w", normalized.Name, err))
	}
	var result *agentcomposev2.ExecResult
	for stream.Receive() {
		event := stream.Msg()
		switch event.GetEventType() {
		case agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_OUTPUT:
			if cli.JSON {
				continue
			}
			target := cmd.OutOrStdout()
			if event.GetIsStderr() {
				target = cmd.ErrOrStderr()
			}
			if _, err := io.WriteString(target, event.GetChunk()); err != nil {
				return err
			}
		case agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED:
			result = event.GetResult()
		}
	}
	if err := stream.Err(); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s: %w", normalized.Name, err))
	}
	if result == nil {
		return fmt.Errorf("exec project %s: stream completed without result", normalized.Name)
	}
	if cli.JSON {
		data, err := json.MarshalIndent(composeExecOutputFromResult(result), "", "  ")
		if err != nil {
			return err
		}
		if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
			return err
		}
	}
	if !result.GetSuccess() {
		return commandExitError{Code: execResultExitCode(result), Err: fmt.Errorf("exec %s in session %s failed: %s", result.GetExecId(), result.GetSessionId(), firstNonEmptyString(result.GetError(), result.GetStderr(), result.GetOutput(), "command failed"))}
	}
	return nil
}

func normalizeComposeExecRequest(cmd *cobra.Command, projectName, projectID string, options composeExecOptions, args []string) (*agentcomposev2.ExecRequest, error) {
	legacyTargetFlags := []string{}
	if cmd.Flags().Changed("session-id") {
		legacyTargetFlags = append(legacyTargetFlags, "--session-id")
	}
	if cmd.Flags().Changed("run-id") {
		legacyTargetFlags = append(legacyTargetFlags, "--run-id")
	}
	if cmd.Flags().Changed("agent") {
		legacyTargetFlags = append(legacyTargetFlags, "--agent")
	}
	if len(legacyTargetFlags) > 1 {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec target can only be specified once")}
	}
	if len(legacyTargetFlags) > 0 {
		if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose exec "+legacyTargetFlags[0], "agent-compose exec <sandbox>"); err != nil {
			return nil, err
		}
		command, err := composeExecCommandFromArgs(options, args)
		if err != nil {
			return nil, err
		}
		req := &agentcomposev2.ExecRequest{
			Command: command,
			Cwd:     strings.TrimSpace(options.Cwd),
		}
		switch legacyTargetFlags[0] {
		case "--session-id":
			sessionID := strings.TrimSpace(options.SessionID)
			if sessionID == "" {
				return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec --session-id requires a value")}
			}
			req.Target = &agentcomposev2.ExecRequest_SessionId{SessionId: sessionID}
		case "--run-id":
			runID := strings.TrimSpace(options.RunID)
			if runID == "" {
				return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec --run-id requires a value")}
			}
			req.Target = &agentcomposev2.ExecRequest_RunId{RunId: runID}
		case "--agent":
			agentName := strings.TrimSpace(options.AgentName)
			if agentName == "" {
				return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec --agent requires a value")}
			}
			req.Target = &agentcomposev2.ExecRequest_Selector{Selector: &agentcomposev2.ExecSessionSelector{
				ProjectId:   projectID,
				ProjectName: projectName,
				AgentName:   agentName,
			}}
		}
		return req, nil
	}
	sandbox := strings.TrimSpace(args[0])
	if sandbox == "" {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec requires non-empty sandbox")}
	}
	command, err := composeExecCommandFromArgs(options, args[1:])
	if err != nil {
		return nil, err
	}
	return &agentcomposev2.ExecRequest{
		Command: command,
		Cwd:     strings.TrimSpace(options.Cwd),
		Target:  &agentcomposev2.ExecRequest_SessionId{SessionId: sandbox},
	}, nil
}

func composeExecCommandFromArgs(options composeExecOptions, args []string) (*agentcomposev2.ExecCommand, error) {
	commandText := strings.TrimSpace(options.Command)
	if commandText != "" {
		if len(args) > 0 {
			return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec command can be specified either with --command or positional arguments, not both")}
		}
		return &agentcomposev2.ExecCommand{Command: "bash", Args: []string{"-lc", commandText}}, nil
	}
	commandArgs := append([]string(nil), args...)
	if len(commandArgs) == 0 {
		commandArgs = []string{"sh"}
	}
	return &agentcomposev2.ExecCommand{Command: commandArgs[0], Args: append([]string(nil), commandArgs[1:]...)}, nil
}

func runComposeImageListCommand(cmd *cobra.Command, cli cliOptions, options composeImageListOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.image.ListImages(cmd.Context(), connect.NewRequest(&agentcomposev2.ListImagesRequest{
		Query: strings.TrimSpace(options.Query),
		All:   options.All,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("list images: %w", err))
	}
	output := composeImageListOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeImagesText(cmd.OutOrStdout(), output.Images)
}

func runComposePullCommand(cmd *cobra.Command, cli cliOptions, options composeImagePullOptions, args []string) error {
	if len(args) == 1 {
		return runComposeImagePullCommand(cmd, cli, options, args[0])
	}
	_, normalized, err := loadNormalizedCompose(cli)
	if err != nil {
		return err
	}
	imageRefs := projectImageRefs(normalized)
	if len(imageRefs) == 0 {
		if cli.JSON {
			data, err := json.MarshalIndent(composeProjectImagePullOutput{Images: []composeImagePullOutput{}}, "", "  ")
			if err != nil {
				return err
			}
			return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "No project images configured")
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	platform, err := parseImagePlatform(options.Platform)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	output := composeProjectImagePullOutput{
		Images: make([]composeImagePullOutput, 0, len(imageRefs)),
	}
	for _, imageRef := range imageRefs {
		item, err := pullImage(cmd.Context(), clients.image, imageRef, platform)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("pull image %s: %w", imageRef, err))
		}
		output.Images = append(output.Images, item)
		if !cli.JSON {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Pulled %s\nResolved: %s\n", item.ImageRef, firstNonEmptyString(item.ResolvedRef, "-")); err != nil {
				return err
			}
		}
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return nil
}

func projectImageRefs(project *compose.NormalizedProjectSpec) []string {
	seen := make(map[string]struct{}, len(project.Agents))
	refs := make([]string, 0, len(project.Agents))
	for _, agent := range project.Agents {
		imageRef := strings.TrimSpace(agent.Image)
		if imageRef == "" {
			continue
		}
		if _, ok := seen[imageRef]; ok {
			continue
		}
		seen[imageRef] = struct{}{}
		refs = append(refs, imageRef)
	}
	return refs
}

func runComposeImagePullCommand(cmd *cobra.Command, cli cliOptions, options composeImagePullOptions, imageRef string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	platform, err := parseImagePlatform(options.Platform)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	output, err := pullImage(cmd.Context(), clients.image, strings.TrimSpace(imageRef), platform)
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("pull image %s: %w", strings.TrimSpace(imageRef), err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Pulled %s\nResolved: %s\n", output.ImageRef, firstNonEmptyString(output.ResolvedRef, "-"))
	return err
}

func pullImage(ctx context.Context, client agentcomposev2connect.ImageServiceClient, imageRef string, platform *agentcomposev2.ImagePlatform) (composeImagePullOutput, error) {
	resp, err := client.PullImage(ctx, connect.NewRequest(&agentcomposev2.PullImageRequest{
		ImageRef: imageRef,
		Platform: platform,
	}))
	if err != nil {
		return composeImagePullOutput{}, err
	}
	return composeImagePullOutputFromResponse(resp.Msg), nil
}

func runComposeImageRemoveCommand(cmd *cobra.Command, cli cliOptions, options composeImageRemoveOptions, imageRef string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.image.RemoveImage(cmd.Context(), connect.NewRequest(&agentcomposev2.RemoveImageRequest{
		ImageRef:      strings.TrimSpace(imageRef),
		Force:         options.Force,
		PruneChildren: options.PruneChildren,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("remove image %s: %w", strings.TrimSpace(imageRef), err))
	}
	output := composeImageRemoveOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	for _, ref := range output.UntaggedRefs {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Untagged: %s\n", ref); err != nil {
			return err
		}
	}
	for _, id := range output.DeletedIDs {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Deleted: %s\n", id); err != nil {
			return err
		}
	}
	if len(output.UntaggedRefs) == 0 && len(output.DeletedIDs) == 0 {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed: %s\n", output.ImageRef)
		return err
	}
	return nil
}

func runComposeImageInspectCommand(cmd *cobra.Command, cli cliOptions, imageRef string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.image.InspectImage(cmd.Context(), connect.NewRequest(&agentcomposev2.InspectImageRequest{
		ImageRef: strings.TrimSpace(imageRef),
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("inspect image %s: %w", strings.TrimSpace(imageRef), err))
	}
	output := composeImageInspectOutputFromResponse(resp.Msg)
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
}

func writeDeprecatedWarning(out io.Writer, oldUsage string, newUsage string) error {
	_, err := fmt.Fprintf(out, "Warning: %s is deprecated and will be removed in a future release; use %s instead.\n", oldUsage, newUsage)
	return err
}

func runComposeInspectCommand(cmd *cobra.Command, cli cliOptions, args []string) error {
	kind := strings.ToLower(strings.TrimSpace(args[0]))
	target := ""
	if len(args) > 1 {
		target = strings.TrimSpace(args[1])
	}
	if kind == "image" {
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect image requires an image reference")}
		}
		return runComposeImageInspectCommand(cmd, cli, target)
	}
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	var output any
	switch kind {
	case "project":
		project, err := clients.project.GetProject(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{
			Project:     &agentcomposev2.ProjectRef{ProjectId: projectID},
			IncludeSpec: true,
		}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect project %s: %w", normalized.Name, err))
		}
		output = composeProjectOutputFromProject(project.Msg.GetProject())
	case "agent":
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect agent requires an agent name")}
		}
		project, err := clients.project.GetProject(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{
			Project:     &agentcomposev2.ProjectRef{ProjectId: projectID},
			IncludeSpec: true,
		}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect agent %s in project %s: %w", target, normalized.Name, err))
		}
		agent, err := composeAgentInspectOutputFor(cmd.Context(), clients, project.Msg.GetProject(), target)
		if err != nil {
			return err
		}
		output = agent
	case "run":
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect run requires a run id")}
		}
		run, err := getRunDetail(cmd.Context(), clients.run, projectID, target)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect run %s in project %s: %w", target, normalized.Name, err))
		}
		output = composeRunOutputFromDetail(run.Msg.GetRun())
	case "sandbox":
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect sandbox requires a sandbox")}
		}
		output, err = composeSandboxInspectOutputFor(cmd.Context(), clients, target)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect sandbox %s: %w", target, err))
		}
	case "session":
		// Deprecated: use `agent-compose inspect sandbox <sandbox>` instead.
		if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose inspect session", "agent-compose inspect sandbox"); err != nil {
			return err
		}
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect session requires a session id")}
		}
		output, err = composeSandboxInspectOutputFor(cmd.Context(), clients, target)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect session %s: %w", target, err))
		}
	default:
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("unsupported inspect target %q", kind)}
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
}

func composeSandboxInspectOutputFor(ctx context.Context, clients cliServiceClients, sandbox string) (composeSessionOutput, error) {
	session, err := clients.session.GetSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sandbox}))
	if err != nil {
		return composeSessionOutput{}, err
	}
	return composeSessionOutputFromSummary(session.Msg.GetSession().GetSummary()), nil
}

func resolveComposeProject(cli cliOptions) (string, *compose.NormalizedProjectSpec, string, error) {
	composePath, normalized, err := loadNormalizedCompose(cli)
	if err != nil {
		return "", nil, "", err
	}
	project, err := projects.NewRecordFromSpec(normalized, composePath)
	if err != nil {
		return "", nil, "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s: resolve project %s: %w", composePath, normalized.Name, err)}
	}
	return composePath, normalized, project.ID, nil
}

func loadNormalizedCompose(cli cliOptions) (string, *compose.NormalizedProjectSpec, error) {
	composePath, err := resolveComposePath(cli.ComposeFile)
	if err != nil {
		return "", nil, err
	}
	spec, err := compose.ParseFile(composePath)
	if err != nil {
		return "", nil, commandExitError{Code: exitCodeUsage, Err: err}
	}
	if projectName := strings.TrimSpace(cli.ProjectName); projectName != "" {
		spec.Name = projectName
	}
	normalized, err := compose.Normalize(spec, compose.NormalizeOptions{ComposePath: composePath})
	if err != nil {
		return "", nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s: %w", composePath, err)}
	}
	return composePath, normalized, nil
}

func resolveComposePath(pathFlag string) (string, error) {
	pathFlag = strings.TrimSpace(pathFlag)
	if pathFlag != "" {
		abs, err := filepath.Abs(pathFlag)
		if err != nil {
			return pathFlag, fmt.Errorf("resolve --file %q: %w", pathFlag, err)
		}
		return abs, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("find agent-compose.yml or agent-compose.yaml: %w", err)
	}
	ymlPath := filepath.Join(wd, "agent-compose.yml")
	yamlPath := filepath.Join(wd, "agent-compose.yaml")
	ymlExists, err := fileExists(ymlPath)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", ymlPath, err)
	}
	yamlExists, err := fileExists(yamlPath)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", yamlPath, err)
	}
	switch {
	case ymlExists && yamlExists:
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("both %s and %s exist; use --file to choose one", ymlPath, yamlPath)}
	case ymlExists:
		return ymlPath, nil
	case yamlExists:
		return yamlPath, nil
	default:
		return ymlPath, nil
	}
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func writeCommandOutput(out io.Writer, data []byte) error {
	if _, err := out.Write(data); err != nil {
		return err
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return nil
	}
	_, err := fmt.Fprintln(out)
	return err
}

type composeUpOutput struct {
	Project   composeUpProjectOutput  `json:"project"`
	Revision  composeUpRevisionOutput `json:"revision"`
	Applied   bool                    `json:"applied"`
	Unchanged bool                    `json:"unchanged"`
	Changes   []composeUpChangeOutput `json:"changes"`
}

type composeDownOutput struct {
	Project            composeUpProjectOutput  `json:"project"`
	Status             string                  `json:"status"`
	FailedSessionStops uint32                  `json:"failed_session_stops"`
	Changes            []composeUpChangeOutput `json:"changes"`
}

type composeProjectListOutput struct {
	Projects   []composeProjectListItem `json:"projects"`
	TotalCount uint32                   `json:"total_count"`
	HasMore    bool                     `json:"has_more"`
	NextOffset uint32                   `json:"next_offset"`
}

type composeProjectListItem struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	ConfigFile      string  `json:"config_file"`
	ProjectDir      string  `json:"project_dir,omitempty"`
	Revision        uint64  `json:"revision"`
	SpecHash        string  `json:"spec_hash,omitempty"`
	AgentCount      uint32  `json:"agent_count"`
	SchedulerCount  uint32  `json:"scheduler_count"`
	ServiceCount    *uint32 `json:"service_count"`
	RunningRunCount uint32  `json:"running_run_count"`
	LatestRunID     string  `json:"latest_run_id,omitempty"`
	CreatedAt       string  `json:"created_at,omitempty"`
	UpdatedAt       string  `json:"updated_at,omitempty"`
	RemovedAt       string  `json:"removed_at,omitempty"`
}

type composeUpProjectOutput struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	SourcePath      string `json:"source_path"`
	CurrentRevision uint64 `json:"current_revision"`
	SpecHash        string `json:"spec_hash"`
	AgentCount      uint32 `json:"agent_count"`
	SchedulerCount  uint32 `json:"scheduler_count"`
}

type composeUpRevisionOutput struct {
	Revision uint64 `json:"revision"`
	SpecHash string `json:"spec_hash"`
}

type composeUpChangeOutput struct {
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Name         string `json:"name"`
	Message      string `json:"message,omitempty"`
}

type composeRunOutput struct {
	RunID        string `json:"run_id"`
	ProjectID    string `json:"project_id"`
	ProjectName  string `json:"project_name"`
	AgentName    string `json:"agent_name"`
	Source       string `json:"source"`
	Status       string `json:"status"`
	SessionID    string `json:"session_id"`
	ExitCode     int32  `json:"exit_code"`
	Error        string `json:"error,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
	DurationMs   int64  `json:"duration_ms,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	Output       string `json:"output,omitempty"`
	ResultJSON   string `json:"result_json,omitempty"`
	LogsPath     string `json:"logs_path,omitempty"`
	ArtifactsDir string `json:"artifacts_dir,omitempty"`
	CleanupError string `json:"cleanup_error,omitempty"`
	Driver       string `json:"driver,omitempty"`
	ImageRef     string `json:"image_ref,omitempty"`
}

type composeLogsOutput struct {
	Runs []composeRunOutput `json:"runs"`
}

type cliServiceClients struct {
	project agentcomposev2connect.ProjectServiceClient
	run     agentcomposev2connect.RunServiceClient
	exec    agentcomposev2connect.ExecServiceClient
	image   agentcomposev2connect.ImageServiceClient
	sandbox agentcomposev2connect.SandboxServiceClient
	session agentcomposev1connect.SessionServiceClient
}

type composePSOutput struct {
	Project   composeUpProjectOutput   `json:"project"`
	Sandboxes []composePSSandboxOutput `json:"sandboxes"`
}

type composePSSandboxOutput struct {
	Sandbox   string `json:"sandbox"`
	Agent     string `json:"agent,omitempty"`
	Status    string `json:"status"`
	Run       string `json:"run,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Driver    string `json:"driver,omitempty"`
	Image     string `json:"image,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

type composeProjectOutput struct {
	Project    composeUpProjectOutput          `json:"project"`
	Agents     []composeProjectAgentOutput     `json:"agents"`
	Schedulers []composeProjectSchedulerOutput `json:"schedulers"`
}

type composeProjectAgentOutput struct {
	AgentName        string `json:"agent_name"`
	ManagedAgentID   string `json:"managed_agent_id"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	Image            string `json:"image,omitempty"`
	Driver           string `json:"driver,omitempty"`
	SchedulerEnabled bool   `json:"scheduler_enabled"`
}

type composeProjectSchedulerOutput struct {
	AgentName       string `json:"agent_name"`
	SchedulerID     string `json:"scheduler_id"`
	ManagedLoaderID string `json:"managed_loader_id"`
	Enabled         bool   `json:"enabled"`
	TriggerCount    uint32 `json:"trigger_count"`
}

type composeAgentInspectOutput struct {
	Project         composeUpProjectOutput          `json:"project"`
	Agent           composeProjectAgentOutput       `json:"agent"`
	Schedulers      []composeProjectSchedulerOutput `json:"schedulers"`
	LatestRun       *composeRunOutput               `json:"latest_run,omitempty"`
	RunningSessions []composeSessionOutput          `json:"running_sessions,omitempty"`
}

type composeSessionOutput struct {
	SessionID     string            `json:"session_id"`
	Title         string            `json:"title,omitempty"`
	Driver        string            `json:"driver,omitempty"`
	VMStatus      string            `json:"vm_status,omitempty"`
	WorkspacePath string            `json:"workspace_path,omitempty"`
	ProxyPath     string            `json:"proxy_path,omitempty"`
	GuestImage    string            `json:"guest_image,omitempty"`
	TriggerSource string            `json:"trigger_source,omitempty"`
	CreatedAt     string            `json:"created_at,omitempty"`
	UpdatedAt     string            `json:"updated_at,omitempty"`
	CellCount     uint32            `json:"cell_count"`
	EventCount    uint32            `json:"event_count"`
	Tags          map[string]string `json:"tags,omitempty"`
}

type composeExecOutput struct {
	ExecID    string   `json:"exec_id"`
	SessionID string   `json:"session_id"`
	RunID     string   `json:"run_id,omitempty"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	ExitCode  int32    `json:"exit_code"`
	Success   bool     `json:"success"`
	Stdout    string   `json:"stdout,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	Output    string   `json:"output,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type composeImageListOutput struct {
	Images      []composeImageOutput    `json:"images"`
	TotalCount  uint32                  `json:"total_count"`
	HasMore     bool                    `json:"has_more"`
	NextOffset  uint32                  `json:"next_offset"`
	StoreStatus composeImageStoreOutput `json:"store_status"`
}

type composeImageInspectOutput struct {
	Image       composeImageOutput      `json:"image"`
	StoreStatus composeImageStoreOutput `json:"store_status"`
}

type composeImagePullOutput struct {
	ImageRef    string                     `json:"image_ref"`
	ResolvedRef string                     `json:"resolved_ref,omitempty"`
	Status      string                     `json:"status"`
	Image       composeImageOutput         `json:"image"`
	Progress    []composeImageProgressItem `json:"progress,omitempty"`
	Warnings    []string                   `json:"warnings,omitempty"`
}

type composeProjectImagePullOutput struct {
	Images []composeImagePullOutput `json:"images"`
}

type composeImageRemoveOutput struct {
	ImageRef     string   `json:"image_ref"`
	UntaggedRefs []string `json:"untagged_refs,omitempty"`
	DeletedIDs   []string `json:"deleted_ids,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

type composeImageOutput struct {
	ImageID            string            `json:"image_id"`
	ImageRef           string            `json:"image_ref"`
	ResolvedRef        string            `json:"resolved_ref,omitempty"`
	RepoTags           []string          `json:"repo_tags,omitempty"`
	RepoDigests        []string          `json:"repo_digests,omitempty"`
	Store              string            `json:"store"`
	AvailabilityStatus string            `json:"availability_status"`
	Platform           string            `json:"platform,omitempty"`
	SizeBytes          uint64            `json:"size_bytes"`
	VirtualSizeBytes   uint64            `json:"virtual_size_bytes"`
	CreatedAt          string            `json:"created_at,omitempty"`
	InspectedAt        string            `json:"inspected_at,omitempty"`
	Dangling           bool              `json:"dangling"`
	ContainerCount     uint64            `json:"container_count"`
	Labels             map[string]string `json:"labels,omitempty"`
}

type composeImageStoreOutput struct {
	Store     string `json:"store"`
	Available bool   `json:"available"`
	Endpoint  string `json:"endpoint,omitempty"`
	Error     string `json:"error,omitempty"`
}

type composeImageProgressItem struct {
	ID           string `json:"id,omitempty"`
	Status       string `json:"status,omitempty"`
	Progress     string `json:"progress,omitempty"`
	CurrentBytes uint64 `json:"current_bytes,omitempty"`
	TotalBytes   uint64 `json:"total_bytes,omitempty"`
}

func composeProjectListItemFromSummary(summary *agentcomposev2.ProjectSummary) composeProjectListItem {
	configFile := summary.GetSourcePath()
	projectDir := ""
	if configFile != "" {
		projectDir = filepath.Dir(configFile)
	}
	return composeProjectListItem{
		ID:              summary.GetProjectId(),
		Name:            summary.GetName(),
		ConfigFile:      configFile,
		ProjectDir:      projectDir,
		Revision:        summary.GetCurrentRevision(),
		SpecHash:        summary.GetSpecHash(),
		AgentCount:      summary.GetAgentCount(),
		SchedulerCount:  summary.GetSchedulerCount(),
		ServiceCount:    nil,
		RunningRunCount: summary.GetRunningRunCount(),
		LatestRunID:     summary.GetLatestRunId(),
		CreatedAt:       summary.GetCreatedAt(),
		UpdatedAt:       summary.GetUpdatedAt(),
		RemovedAt:       summary.GetRemovedAt(),
	}
}

func composeUpOutputFromResponse(resp *agentcomposev2.ApplyProjectResponse) composeUpOutput {
	summary := resp.GetProject().GetSummary()
	revision := resp.GetRevision()
	changes := make([]composeUpChangeOutput, 0, len(resp.GetChanges()))
	for _, change := range resp.GetChanges() {
		changes = append(changes, composeUpChangeOutput{
			Action:       projectChangeActionText(change.GetAction()),
			ResourceType: change.GetResourceType(),
			ResourceID:   change.GetResourceId(),
			Name:         change.GetName(),
			Message:      change.GetMessage(),
		})
	}
	return composeUpOutput{
		Project: composeUpProjectOutput{
			ID:              summary.GetProjectId(),
			Name:            summary.GetName(),
			SourcePath:      summary.GetSourcePath(),
			CurrentRevision: summary.GetCurrentRevision(),
			SpecHash:        summary.GetSpecHash(),
			AgentCount:      summary.GetAgentCount(),
			SchedulerCount:  summary.GetSchedulerCount(),
		},
		Revision: composeUpRevisionOutput{
			Revision: revision.GetRevision(),
			SpecHash: revision.GetSpecHash(),
		},
		Applied:   resp.GetApplied(),
		Unchanged: resp.GetUnchanged(),
		Changes:   changes,
	}
}

func composeDownOutputFromResponse(resp *agentcomposev2.RemoveProjectResponse) composeDownOutput {
	changes := composeChangeOutputs(resp.GetChanges())
	failedSessionStops := countProjectDownFailedSessionStops(resp.GetChanges())
	status := "down"
	if len(changes) == 0 {
		status = "unchanged"
	}
	if failedSessionStops > 0 {
		status = "partial-failure"
	}
	return composeDownOutput{
		Project:            composeProjectSummaryOutput(resp.GetProject().GetSummary()),
		Status:             status,
		FailedSessionStops: uint32(failedSessionStops),
		Changes:            changes,
	}
}

func composeChangeOutputs(changes []*agentcomposev2.ProjectChange) []composeUpChangeOutput {
	output := make([]composeUpChangeOutput, 0, len(changes))
	for _, change := range changes {
		output = append(output, composeUpChangeOutput{
			Action:       projectChangeActionText(change.GetAction()),
			ResourceType: change.GetResourceType(),
			ResourceID:   change.GetResourceId(),
			Name:         change.GetName(),
			Message:      change.GetMessage(),
		})
	}
	return output
}

func writeComposeUpText(out io.Writer, resp *agentcomposev2.ApplyProjectResponse) error {
	summary := resp.GetProject().GetSummary()
	revision := resp.GetRevision()
	status := "applied"
	if resp.GetUnchanged() {
		status = "unchanged"
	} else if !resp.GetApplied() {
		status = "not-applied"
	}
	if _, err := fmt.Fprintf(out, "Project: %s\nID: %s\nRevision: %d\nSpec: %s\nStatus: %s\nAgents: %d\nSchedulers: %d\n\n",
		summary.GetName(),
		summary.GetProjectId(),
		revision.GetRevision(),
		revision.GetSpecHash(),
		status,
		summary.GetAgentCount(),
		summary.GetSchedulerCount(),
	); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ACTION\tTYPE\tNAME\tID"); err != nil {
		return err
	}
	for _, change := range resp.GetChanges() {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			projectChangeActionText(change.GetAction()),
			change.GetResourceType(),
			change.GetName(),
			change.GetResourceId(),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeProjectListText(out io.Writer, projects []composeProjectListItem, verbose bool) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if verbose {
		if _, err := fmt.Fprintln(tw, "PROJECT\tCONFIG FILE\tREVISION\tAGENTS\tSCHEDULERS\tSERVICES\tPROJECT ID\tPROJECT DIR\tSPEC HASH\tUPDATED\tSTATUS"); err != nil {
			return err
		}
		for _, project := range projects {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
				project.Name,
				firstNonEmptyString(project.ConfigFile, "-"),
				project.Revision,
				project.AgentCount,
				project.SchedulerCount,
				projectServiceCountText(project.ServiceCount),
				firstNonEmptyString(project.ID, "-"),
				firstNonEmptyString(project.ProjectDir, "-"),
				firstNonEmptyString(project.SpecHash, "-"),
				firstNonEmptyString(project.UpdatedAt, "-"),
				projectListStatus(project),
			); err != nil {
				return err
			}
		}
		return tw.Flush()
	}
	if _, err := fmt.Fprintln(tw, "PROJECT\tCONFIG FILE\tREVISION\tAGENTS\tSCHEDULERS\tSERVICES"); err != nil {
		return err
	}
	for _, project := range projects {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n",
			project.Name,
			firstNonEmptyString(project.ConfigFile, "-"),
			project.Revision,
			project.AgentCount,
			project.SchedulerCount,
			projectServiceCountText(project.ServiceCount),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func projectServiceCountText(count *uint32) string {
	if count == nil {
		return "-"
	}
	return strconv.FormatUint(uint64(*count), 10)
}

func projectListStatus(project composeProjectListItem) string {
	if project.RemovedAt != "" {
		return "removed"
	}
	return "active"
}

func writeComposeDownText(out io.Writer, output composeDownOutput) error {
	if _, err := fmt.Fprintf(out, "Project: %s\nID: %s\nStatus: %s\nFailed session stops: %d\n\n",
		output.Project.Name,
		output.Project.ID,
		output.Status,
		output.FailedSessionStops,
	); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ACTION\tTYPE\tNAME\tID\tMESSAGE"); err != nil {
		return err
	}
	for _, change := range output.Changes {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			change.Action,
			change.ResourceType,
			change.Name,
			change.ResourceID,
			change.Message,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func countProjectDownFailedSessionStops(changes []*agentcomposev2.ProjectChange) int {
	count := 0
	for _, change := range changes {
		if change.GetAction() == agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED &&
			change.GetResourceType() == "session" &&
			strings.TrimSpace(change.GetMessage()) != "" {
			count++
		}
	}
	return count
}

func projectChangeActionText(action agentcomposev2.ProjectChangeAction) string {
	switch action {
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED:
		return "created"
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED:
		return "updated"
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED:
		return "removed"
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED:
		return "unchanged"
	default:
		return "unspecified"
	}
}

func formatProjectValidationIssues(issues []*agentcomposev2.ProjectValidationIssue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.GetPath() == "" {
			parts = append(parts, issue.GetMessage())
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", issue.GetPath(), issue.GetMessage()))
	}
	return strings.Join(parts, "; ")
}

func newCLIServiceClients(cli cliOptions) (cliServiceClients, error) {
	clientConfig, err := resolveCLIClientConfig(cli.Host)
	if err != nil {
		return cliServiceClients{}, err
	}
	httpClient := newDaemonHTTPClient(clientConfig)
	return cliServiceClients{
		project: agentcomposev2connect.NewProjectServiceClient(httpClient, clientConfig.BaseURL),
		run:     agentcomposev2connect.NewRunServiceClient(httpClient, clientConfig.BaseURL),
		exec:    agentcomposev2connect.NewExecServiceClient(httpClient, clientConfig.BaseURL),
		image:   agentcomposev2connect.NewImageServiceClient(httpClient, clientConfig.BaseURL),
		sandbox: agentcomposev2connect.NewSandboxServiceClient(httpClient, clientConfig.BaseURL),
		session: agentcomposev1connect.NewSessionServiceClient(httpClient, clientConfig.BaseURL),
	}, nil
}

func composePSOutputFromProject(ctx context.Context, clients cliServiceClients, project *agentcomposev2.Project, options composePSOptions) (composePSOutput, error) {
	output := composePSOutput{Project: composeProjectSummaryOutput(project.GetSummary())}
	statusFilter, err := composePSStatusFilter(options)
	if err != nil {
		return composePSOutput{}, err
	}
	projectID := project.GetSummary().GetProjectId()
	runs, err := listProjectRuns(ctx, clients.run, projectID)
	if err != nil {
		return composePSOutput{}, err
	}
	runBySession := latestRunsBySession(runs)
	sessions, err := listAllSessions(ctx, clients.session)
	if err != nil {
		return composePSOutput{}, err
	}
	for _, session := range sessions {
		if !composePSSessionBelongsToProject(session, project, runBySession) {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(session.GetVmStatus()))
		if status == "" {
			status = "unknown"
		}
		if statusFilter != nil && !statusFilter[status] {
			continue
		}
		run := runBySession[session.GetSessionId()]
		tags := sessionTagsMap(session.GetTags())
		agent := firstNonEmptyString(run.GetAgentName(), tags["agent"])
		runID := firstNonEmptyString(run.GetRunId(), tags["run_id"])
		output.Sandboxes = append(output.Sandboxes, composePSSandboxOutput{
			Sandbox:   session.GetSessionId(),
			Agent:     agent,
			Status:    status,
			Run:       runID,
			CreatedAt: session.GetCreatedAt(),
			UpdatedAt: session.GetUpdatedAt(),
			Driver:    session.GetDriver(),
			Image:     session.GetGuestImage(),
			Workspace: session.GetWorkspacePath(),
		})
	}
	return output, nil
}

func composePSStatusFilter(options composePSOptions) (map[string]bool, error) {
	values := strings.Split(options.Status, ",")
	result := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		result[value] = true
	}
	if len(result) > 0 {
		return result, nil
	}
	if options.All {
		return nil, nil
	}
	return map[string]bool{"running": true}, nil
}

func listAllSessions(ctx context.Context, client agentcomposev1connect.SessionServiceClient) ([]*agentcomposev1.SessionSummary, error) {
	var result []*agentcomposev1.SessionSummary
	var offset uint32
	const limit uint32 = 100
	for {
		resp, err := client.ListSessions(ctx, connect.NewRequest(&agentcomposev1.ListSessionsRequest{
			Offset: offset,
			Limit:  limit,
		}))
		if err != nil {
			return nil, err
		}
		result = append(result, resp.Msg.GetSessions()...)
		if !resp.Msg.GetHasMore() {
			break
		}
		next := resp.Msg.GetNextOffset()
		if next <= offset {
			break
		}
		offset = next
	}
	return result, nil
}

func listProjectRuns(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID string) ([]*agentcomposev2.RunSummary, error) {
	var result []*agentcomposev2.RunSummary
	var offset uint32
	const limit uint32 = 100
	for {
		resp, err := client.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
			ProjectId: projectID,
			Offset:    offset,
			Limit:     limit,
		}))
		if err != nil {
			return nil, err
		}
		runs := resp.Msg.GetRuns()
		result = append(result, runs...)
		if uint32(len(runs)) < limit {
			break
		}
		offset += limit
	}
	return result, nil
}

func latestRunsBySession(runs []*agentcomposev2.RunSummary) map[string]*agentcomposev2.RunSummary {
	result := map[string]*agentcomposev2.RunSummary{}
	for _, run := range runs {
		sessionID := strings.TrimSpace(run.GetSessionId())
		if sessionID == "" {
			continue
		}
		if current := result[sessionID]; current == nil || runSortTime(run) > runSortTime(current) {
			result[sessionID] = run
		}
	}
	return result
}

func runSortTime(run *agentcomposev2.RunSummary) string {
	return firstNonEmptyString(run.GetUpdatedAt(), run.GetCreatedAt(), run.GetStartedAt(), run.GetCompletedAt())
}

func composePSSessionBelongsToProject(session *agentcomposev1.SessionSummary, project *agentcomposev2.Project, runsBySession map[string]*agentcomposev2.RunSummary) bool {
	projectID := strings.TrimSpace(project.GetSummary().GetProjectId())
	projectName := strings.TrimSpace(project.GetSummary().GetName())
	sourcePath := strings.TrimSpace(project.GetSummary().GetSourcePath())
	if run := runsBySession[session.GetSessionId()]; run != nil {
		if strings.TrimSpace(run.GetProjectId()) == projectID {
			return true
		}
	}
	tags := sessionTagsMap(session.GetTags())
	for _, key := range []string{"project", "project_id"} {
		if value := strings.TrimSpace(tags[key]); value != "" && (value == projectID || value == projectName || value == sourcePath) {
			return true
		}
	}
	if value := strings.TrimSpace(session.GetTriggerSource()); value != "" {
		value = strings.ToLower(value)
		return (projectID != "" && strings.Contains(value, strings.ToLower(projectID))) ||
			(projectName != "" && strings.Contains(value, strings.ToLower(projectName)))
	}
	return false
}

func sessionTagsMap(items []*agentcomposev1.SessionTag) map[string]string {
	result := make(map[string]string, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.GetName())
		if name == "" {
			continue
		}
		result[name] = strings.TrimSpace(item.GetValue())
	}
	return result
}

func latestRunOutput(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID, agentName string) (*composeRunOutput, error) {
	resp, err := client.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: projectID,
		AgentName: agentName,
		Limit:     1,
	}))
	if err != nil {
		return nil, err
	}
	if len(resp.Msg.GetRuns()) == 0 {
		return nil, nil
	}
	detail, err := getRunDetail(ctx, client, projectID, resp.Msg.GetRuns()[0].GetRunId())
	if err != nil {
		return nil, err
	}
	output := composeRunOutputFromDetail(detail.Msg.GetRun())
	return &output, nil
}

func firstRunningSessionOutput(ctx context.Context, clients cliServiceClients, projectID, agentName string) (*composeSessionOutput, error) {
	resp, err := clients.run.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: projectID,
		AgentName: agentName,
		Limit:     20,
	}))
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, run := range resp.Msg.GetRuns() {
		sessionID := strings.TrimSpace(run.GetSessionId())
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		session, err := clients.session.GetSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID}))
		if err != nil {
			continue
		}
		summary := session.Msg.GetSession().GetSummary()
		if strings.EqualFold(summary.GetVmStatus(), "running") {
			output := composeSessionOutputFromSummary(summary)
			return &output, nil
		}
	}
	return nil, nil
}

func writePSText(out io.Writer, output composePSOutput, verbose bool) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if verbose {
		if _, err := fmt.Fprintln(tw, "SANDBOX\tAGENT\tSTATUS\tRUN\tCREATED\tUPDATED\tDRIVER\tIMAGE\tWORKSPACE"); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(tw, "SANDBOX\tAGENT\tSTATUS\tRUN\tCREATED\tUPDATED"); err != nil {
		return err
	}
	for _, sandbox := range output.Sandboxes {
		if verbose {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				sandbox.Sandbox,
				firstNonEmptyString(sandbox.Agent, "-"),
				firstNonEmptyString(sandbox.Status, "-"),
				firstNonEmptyString(sandbox.Run, "-"),
				firstNonEmptyString(sandbox.CreatedAt, "-"),
				firstNonEmptyString(sandbox.UpdatedAt, "-"),
				firstNonEmptyString(sandbox.Driver, "-"),
				firstNonEmptyString(sandbox.Image, "-"),
				firstNonEmptyString(sandbox.Workspace, "-"),
			); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			sandbox.Sandbox,
			firstNonEmptyString(sandbox.Agent, "-"),
			firstNonEmptyString(sandbox.Status, "-"),
			firstNonEmptyString(sandbox.Run, "-"),
			firstNonEmptyString(sandbox.CreatedAt, "-"),
			firstNonEmptyString(sandbox.UpdatedAt, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func composeProjectOutputFromProject(project *agentcomposev2.Project) composeProjectOutput {
	output := composeProjectOutput{Project: composeProjectSummaryOutput(project.GetSummary())}
	for _, agent := range project.GetAgents() {
		output.Agents = append(output.Agents, composeProjectAgentOutputFromProto(agent))
	}
	for _, scheduler := range project.GetSchedulers() {
		output.Schedulers = append(output.Schedulers, composeProjectSchedulerOutputFromProto(scheduler))
	}
	return output
}

func composeAgentInspectOutputFor(ctx context.Context, clients cliServiceClients, project *agentcomposev2.Project, agentName string) (composeAgentInspectOutput, error) {
	var found *agentcomposev2.ProjectAgent
	for _, agent := range project.GetAgents() {
		if agent.GetAgentName() == agentName {
			found = agent
			break
		}
	}
	if found == nil {
		return composeAgentInspectOutput{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent %s not found in project %s", agentName, project.GetSummary().GetName())}
	}
	output := composeAgentInspectOutput{
		Project: composeProjectSummaryOutput(project.GetSummary()),
		Agent:   composeProjectAgentOutputFromProto(found),
	}
	for _, scheduler := range project.GetSchedulers() {
		if scheduler.GetAgentName() == agentName {
			output.Schedulers = append(output.Schedulers, composeProjectSchedulerOutputFromProto(scheduler))
		}
	}
	if latest, err := latestRunOutput(ctx, clients.run, project.GetSummary().GetProjectId(), agentName); err != nil {
		return composeAgentInspectOutput{}, commandExitErrorForConnect(fmt.Errorf("list latest run for agent %s: %w", agentName, err))
	} else {
		output.LatestRun = latest
	}
	if session, err := firstRunningSessionOutput(ctx, clients, project.GetSummary().GetProjectId(), agentName); err != nil {
		return composeAgentInspectOutput{}, commandExitErrorForConnect(fmt.Errorf("list running session for agent %s: %w", agentName, err))
	} else if session != nil {
		output.RunningSessions = append(output.RunningSessions, *session)
	}
	return output, nil
}

func composeProjectSummaryOutput(summary *agentcomposev2.ProjectSummary) composeUpProjectOutput {
	return composeUpProjectOutput{
		ID:              summary.GetProjectId(),
		Name:            summary.GetName(),
		SourcePath:      summary.GetSourcePath(),
		CurrentRevision: summary.GetCurrentRevision(),
		SpecHash:        summary.GetSpecHash(),
		AgentCount:      summary.GetAgentCount(),
		SchedulerCount:  summary.GetSchedulerCount(),
	}
}

func composeProjectAgentOutputFromProto(agent *agentcomposev2.ProjectAgent) composeProjectAgentOutput {
	return composeProjectAgentOutput{
		AgentName:        agent.GetAgentName(),
		ManagedAgentID:   agent.GetManagedAgentId(),
		Provider:         agent.GetProvider(),
		Model:            agent.GetModel(),
		Image:            agent.GetImage(),
		Driver:           agent.GetDriver(),
		SchedulerEnabled: agent.GetSchedulerEnabled(),
	}
}

func composeProjectSchedulerOutputFromProto(scheduler *agentcomposev2.ProjectScheduler) composeProjectSchedulerOutput {
	return composeProjectSchedulerOutput{
		AgentName:       scheduler.GetAgentName(),
		SchedulerID:     scheduler.GetSchedulerId(),
		ManagedLoaderID: scheduler.GetManagedLoaderId(),
		Enabled:         scheduler.GetEnabled(),
		TriggerCount:    scheduler.GetTriggerCount(),
	}
}

func composeRunOutputFromDetail(run *agentcomposev2.RunDetail) composeRunOutput {
	return composeRunOutputFromDetailWithOptions(run, composeLogsOptions{TailLines: -1})
}

func composeRunOutputFromDetailWithOptions(run *agentcomposev2.RunDetail, options composeLogsOptions) composeRunOutput {
	summary := run.GetSummary()
	return composeRunOutput{
		RunID:        summary.GetRunId(),
		ProjectID:    summary.GetProjectId(),
		ProjectName:  summary.GetProjectName(),
		AgentName:    summary.GetAgentName(),
		Source:       runSourceText(summary.GetSource()),
		Status:       runStatusText(summary.GetStatus()),
		SessionID:    summary.GetSessionId(),
		ExitCode:     summary.GetExitCode(),
		Error:        summary.GetError(),
		StartedAt:    summary.GetStartedAt(),
		CompletedAt:  summary.GetCompletedAt(),
		DurationMs:   summary.GetDurationMs(),
		Prompt:       run.GetPrompt(),
		Output:       tailLogOutput(run.GetOutput(), options.TailLines),
		ResultJSON:   run.GetResultJson(),
		LogsPath:     run.GetLogsPath(),
		ArtifactsDir: run.GetArtifactsDir(),
		CleanupError: run.GetCleanupError(),
		Driver:       run.GetDriver(),
		ImageRef:     run.GetImageRef(),
	}
}

func composeExecOutputFromResult(result *agentcomposev2.ExecResult) composeExecOutput {
	return composeExecOutput{
		ExecID:    result.GetExecId(),
		SessionID: result.GetSessionId(),
		RunID:     result.GetRunId(),
		Command:   result.GetCommand().GetCommand(),
		Args:      append([]string(nil), result.GetCommand().GetArgs()...),
		Cwd:       result.GetCwd(),
		ExitCode:  result.GetExitCode(),
		Success:   result.GetSuccess(),
		Stdout:    result.GetStdout(),
		Stderr:    result.GetStderr(),
		Output:    result.GetOutput(),
		Error:     result.GetError(),
	}
}

func composeImageListOutputFromResponse(resp *agentcomposev2.ListImagesResponse) composeImageListOutput {
	output := composeImageListOutput{
		Images:      make([]composeImageOutput, 0, len(resp.GetImages())),
		TotalCount:  resp.GetTotalCount(),
		HasMore:     resp.GetHasMore(),
		NextOffset:  resp.GetNextOffset(),
		StoreStatus: composeImageStoreOutputFromProto(resp.GetStoreStatus()),
	}
	for _, image := range resp.GetImages() {
		output.Images = append(output.Images, composeImageOutputFromProto(image))
	}
	return output
}

func composeImagePullOutputFromResponse(resp *agentcomposev2.PullImageResponse) composeImagePullOutput {
	output := composeImagePullOutput{
		ImageRef:    firstNonEmptyString(resp.GetImage().GetImageRef(), resp.GetResolvedRef()),
		ResolvedRef: resp.GetResolvedRef(),
		Status:      imageOperationStatusText(resp.GetStatus()),
		Image:       composeImageOutputFromProto(resp.GetImage()),
		Warnings:    append([]string(nil), resp.GetWarnings()...),
		Progress:    make([]composeImageProgressItem, 0, len(resp.GetProgress())),
	}
	for _, item := range resp.GetProgress() {
		output.Progress = append(output.Progress, composeImageProgressItem{
			ID:           item.GetId(),
			Status:       item.GetStatus(),
			Progress:     item.GetProgress(),
			CurrentBytes: item.GetCurrentBytes(),
			TotalBytes:   item.GetTotalBytes(),
		})
	}
	return output
}

func composeImageInspectOutputFromResponse(resp *agentcomposev2.InspectImageResponse) composeImageInspectOutput {
	return composeImageInspectOutput{
		Image:       composeImageOutputFromProto(resp.GetImage()),
		StoreStatus: composeImageStoreOutputFromProto(resp.GetStoreStatus()),
	}
}

func composeImageRemoveOutputFromResponse(resp *agentcomposev2.RemoveImageResponse) composeImageRemoveOutput {
	return composeImageRemoveOutput{
		ImageRef:     resp.GetImageRef(),
		UntaggedRefs: append([]string(nil), resp.GetUntaggedRefs()...),
		DeletedIDs:   append([]string(nil), resp.GetDeletedIds()...),
		Warnings:     append([]string(nil), resp.GetWarnings()...),
	}
}

func composeImageOutputFromProto(image *agentcomposev2.Image) composeImageOutput {
	if image == nil {
		return composeImageOutput{}
	}
	return composeImageOutput{
		ImageID:            image.GetImageId(),
		ImageRef:           image.GetImageRef(),
		ResolvedRef:        image.GetResolvedRef(),
		RepoTags:           append([]string(nil), image.GetRepoTags()...),
		RepoDigests:        append([]string(nil), image.GetRepoDigests()...),
		Store:              imageStoreText(image.GetStore()),
		AvailabilityStatus: imageAvailabilityStatusText(image.GetAvailabilityStatus()),
		Platform:           imagePlatformText(image.GetPlatform()),
		SizeBytes:          image.GetSizeBytes(),
		VirtualSizeBytes:   image.GetVirtualSizeBytes(),
		CreatedAt:          image.GetCreatedAt(),
		InspectedAt:        image.GetInspectedAt(),
		Dangling:           image.GetDangling(),
		ContainerCount:     image.GetContainerCount(),
		Labels:             cloneStringMapForCLI(image.GetLabels()),
	}
}

func composeImageStoreOutputFromProto(status *agentcomposev2.ImageStoreStatus) composeImageStoreOutput {
	if status == nil {
		return composeImageStoreOutput{}
	}
	return composeImageStoreOutput{
		Store:     imageStoreText(status.GetStore()),
		Available: status.GetAvailable(),
		Endpoint:  status.GetEndpoint(),
		Error:     status.GetError(),
	}
}

func writeImagesText(out io.Writer, images []composeImageOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "IMAGE ID\tREF\tSTATUS\tSIZE\tCREATED"); err != nil {
		return err
	}
	for _, image := range images {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			shortImageID(image.ImageID),
			firstNonEmptyString(image.ImageRef, image.ResolvedRef, "-"),
			firstNonEmptyString(image.AvailabilityStatus, "-"),
			image.SizeBytes,
			firstNonEmptyString(image.CreatedAt, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func composeSessionOutputFromSummary(summary *agentcomposev1.SessionSummary) composeSessionOutput {
	tags := make(map[string]string, len(summary.GetTags()))
	for _, tag := range summary.GetTags() {
		name := strings.TrimSpace(tag.GetName())
		if name == "" {
			continue
		}
		tags[name] = tag.GetValue()
	}
	if len(tags) == 0 {
		tags = nil
	}
	return composeSessionOutput{
		SessionID:     summary.GetSessionId(),
		Title:         summary.GetTitle(),
		Driver:        summary.GetDriver(),
		VMStatus:      strings.ToLower(strings.TrimSpace(summary.GetVmStatus())),
		WorkspacePath: summary.GetWorkspacePath(),
		ProxyPath:     summary.GetProxyPath(),
		GuestImage:    summary.GetGuestImage(),
		TriggerSource: summary.GetTriggerSource(),
		CreatedAt:     summary.GetCreatedAt(),
		UpdatedAt:     summary.GetUpdatedAt(),
		CellCount:     summary.GetCellCount(),
		EventCount:    summary.GetEventCount(),
		Tags:          tags,
	}
}

func followOrPrintProjectLogs(cmd *cobra.Command, cli cliOptions, client agentcomposev2connect.RunServiceClient, projectID, projectName string, options composeLogsOptions) error {
	printed := map[string]int{}
	for {
		runs, err := listLogRuns(cmd.Context(), client, projectID, options)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("list logs for project %s: %w", projectName, err))
		}
		if len(runs) == 0 {
			if cli.JSON {
				data, err := json.MarshalIndent(composeLogsOutput{}, "", "  ")
				if err != nil {
					return err
				}
				return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
			}
			if !options.Follow {
				return nil
			}
		}
		details := make([]*agentcomposev2.RunDetail, 0, len(runs))
		anyRunning := false
		for _, summary := range runs {
			detail, err := getRunDetail(cmd.Context(), client, projectID, summary.GetRunId())
			if err != nil {
				return commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", summary.GetRunId(), projectName, err))
			}
			details = append(details, detail.Msg.GetRun())
			if !runSummaryTerminal(detail.Msg.GetRun().GetSummary()) {
				anyRunning = true
			}
		}
		if cli.JSON {
			output := composeLogsOutput{Runs: make([]composeRunOutput, 0, len(details))}
			for _, detail := range details {
				output.Runs = append(output.Runs, composeRunOutputFromDetailWithOptions(detail, options))
			}
			data, err := json.MarshalIndent(output, "", "  ")
			if err != nil {
				return err
			}
			if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
				return err
			}
			if !options.Follow || !anyRunning {
				return nil
			}
		} else if err := writeLogDetails(cmd.OutOrStdout(), details, printed, options); err != nil {
			return err
		}
		if !options.Follow || !anyRunning {
			return nil
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func listLogRuns(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID string, options composeLogsOptions) ([]*agentcomposev2.RunSummary, error) {
	resp, err := client.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: projectID,
		AgentName: strings.TrimSpace(options.AgentName),
		SessionId: strings.TrimSpace(options.SessionID),
		Limit:     20,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetRuns(), nil
}

func getRunDetail(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID, runID string) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	return client.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{
		ProjectId: strings.TrimSpace(projectID),
		RunId:     strings.TrimSpace(runID),
	}))
}

func writeLogsForRun(out io.Writer, run *agentcomposev2.RunDetail, asJSON bool, options composeLogsOptions) error {
	if asJSON {
		data, err := json.MarshalIndent(composeLogsOutput{Runs: []composeRunOutput{composeRunOutputFromDetailWithOptions(run, options)}}, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(out, append(data, '\n'))
	}
	return writeLogDetails(out, []*agentcomposev2.RunDetail{run}, map[string]int{}, options)
}

func writeLogDetails(out io.Writer, details []*agentcomposev2.RunDetail, printed map[string]int, options composeLogsOptions) error {
	for _, detail := range details {
		summary := detail.GetSummary()
		output := detail.GetOutput()
		start := 0
		if options.Follow {
			start = printed[summary.GetRunId()]
			if start > len(output) {
				start = 0
			}
		}
		if start == len(output) {
			continue
		}
		chunk := output[start:]
		if options.TailLines >= 0 && (!options.Follow || start == 0) {
			chunk = tailLogOutput(chunk, options.TailLines)
		}
		if err := writePrefixedRunOutput(out, summary, chunk, options.Timestamp); err != nil {
			return err
		}
		printed[summary.GetRunId()] = len(output)
	}
	return nil
}

func writePrefixedRunOutput(out io.Writer, summary *agentcomposev2.RunSummary, output string, timestamp bool) error {
	if output == "" {
		return nil
	}
	agentName := firstNonEmptyString(summary.GetAgentName(), summary.GetRunId(), "-")
	runTime := runLogTimestamp(summary)
	for len(output) > 0 {
		line := output
		rest := ""
		if idx := strings.IndexByte(output, '\n'); idx >= 0 {
			line = output[:idx+1]
			rest = output[idx+1:]
		}
		if _, err := fmt.Fprintf(out, "%s | ", agentName); err != nil {
			return err
		}
		if timestamp && runTime != "" {
			if _, err := fmt.Fprintf(out, "%s ", runTime); err != nil {
				return err
			}
		}
		if err := writeCommandOutput(out, []byte(line)); err != nil {
			return err
		}
		if !strings.HasSuffix(line, "\n") {
			if _, err := fmt.Fprintln(out); err != nil {
				return err
			}
		}
		output = rest
	}
	return nil
}

func tailLogOutput(output string, lines int) string {
	if lines < 0 || output == "" {
		return output
	}
	if lines == 0 {
		return ""
	}
	trimmed := strings.TrimSuffix(output, "\n")
	if trimmed == "" {
		return output
	}
	parts := strings.Split(trimmed, "\n")
	if len(parts) <= lines {
		return output
	}
	result := strings.Join(parts[len(parts)-lines:], "\n")
	if strings.HasSuffix(output, "\n") {
		result += "\n"
	}
	return result
}

func runLogTimestamp(summary *agentcomposev2.RunSummary) string {
	return firstNonEmptyString(summary.GetCompletedAt(), summary.GetUpdatedAt(), summary.GetStartedAt())
}

func manualRunClientRequestID(projectName, agentName, prompt string) string {
	value := strings.TrimSpace(projectName) + "|" + strings.TrimSpace(agentName) + "|" + strings.TrimSpace(prompt) + "|" + time.Now().UTC().Format(time.RFC3339Nano)
	return value
}

func runSummaryFailed(run *agentcomposev2.RunSummary) bool {
	switch run.GetStatus() {
	case agentcomposev2.RunStatus_RUN_STATUS_FAILED, agentcomposev2.RunStatus_RUN_STATUS_CANCELED:
		return true
	default:
		return false
	}
}

func runSummaryTerminal(run *agentcomposev2.RunSummary) bool {
	switch run.GetStatus() {
	case agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, agentcomposev2.RunStatus_RUN_STATUS_FAILED, agentcomposev2.RunStatus_RUN_STATUS_CANCELED:
		return true
	default:
		return false
	}
}

func runSummaryExitCode(run *agentcomposev2.RunSummary) int {
	if code := int(run.GetExitCode()); code > 0 && code < 126 {
		return code
	}
	return exitCodeGeneral
}

func execResultExitCode(result *agentcomposev2.ExecResult) int {
	if code := int(result.GetExitCode()); code > 0 && code < 126 {
		return code
	}
	return exitCodeGeneral
}

func parseImagePlatform(value string) (*agentcomposev2.ImagePlatform, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, "/")
	if len(parts) < 2 || len(parts) > 3 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return nil, fmt.Errorf("invalid --platform %q: expected os/arch[/variant]", value)
	}
	platform := &agentcomposev2.ImagePlatform{
		Os:           strings.TrimSpace(parts[0]),
		Architecture: strings.TrimSpace(parts[1]),
	}
	if len(parts) == 3 {
		platform.Variant = strings.TrimSpace(parts[2])
	}
	return platform, nil
}

func imageStoreText(store agentcomposev2.ImageStoreKind) string {
	switch store {
	case agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON:
		return "docker"
	case agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE:
		return "oci-cache"
	default:
		return "unspecified"
	}
}

func imageAvailabilityStatusText(status agentcomposev2.ImageAvailabilityStatus) string {
	switch status {
	case agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE:
		return "available"
	case agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_MISSING:
		return "missing"
	case agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_ERROR:
		return "error"
	default:
		return "unspecified"
	}
}

func imageOperationStatusText(status agentcomposev2.ImageOperationStatus) string {
	switch status {
	case agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED:
		return "succeeded"
	case agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_FAILED:
		return "failed"
	default:
		return "unspecified"
	}
}

func imagePlatformText(platform *agentcomposev2.ImagePlatform) string {
	if platform == nil {
		return ""
	}
	parts := []string{strings.TrimSpace(platform.GetOs()), strings.TrimSpace(platform.GetArchitecture())}
	if strings.TrimSpace(platform.GetVariant()) != "" {
		parts = append(parts, strings.TrimSpace(platform.GetVariant()))
	}
	if parts[0] == "" || parts[1] == "" {
		return strings.Trim(strings.Join(parts, "/"), "/")
	}
	return strings.Join(parts, "/")
}

func shortImageID(id string) string {
	id = strings.TrimPrefix(strings.TrimSpace(id), "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func cloneStringMapForCLI(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func runStatusText(status agentcomposev2.RunStatus) string {
	switch status {
	case agentcomposev2.RunStatus_RUN_STATUS_PENDING:
		return "pending"
	case agentcomposev2.RunStatus_RUN_STATUS_RUNNING:
		return "running"
	case agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED:
		return "succeeded"
	case agentcomposev2.RunStatus_RUN_STATUS_FAILED:
		return "failed"
	case agentcomposev2.RunStatus_RUN_STATUS_CANCELED:
		return "canceled"
	default:
		return "unspecified"
	}
}

func runSourceText(source agentcomposev2.RunSource) string {
	switch source {
	case agentcomposev2.RunSource_RUN_SOURCE_MANUAL:
		return "manual"
	case agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER:
		return "scheduler"
	case agentcomposev2.RunSource_RUN_SOURCE_API:
		return "api"
	default:
		return "unspecified"
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type commandExitError struct {
	Code int
	Err  error
}

func (e commandExitError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e commandExitError) Unwrap() error {
	return e.Err
}

const (
	exitCodeGeneral     = 1
	exitCodeUsage       = 2
	exitCodeUnavailable = 3
	exitCodeUnsupported = 4
)

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr commandExitError
	if errors.As(err, &exitErr) && exitErr.Code > 0 {
		return exitErr.Code
	}
	return exitCodeGeneral
}

func commandExitErrorForConnect(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnimplemented:
		return commandExitError{Code: exitCodeUnsupported, Err: err}
	case connect.CodeUnavailable:
		return commandExitError{Code: exitCodeUnavailable, Err: err}
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeNotFound:
		return commandExitError{Code: exitCodeUsage, Err: err}
	default:
		return err
	}
}

func runDaemon(ctx context.Context) error {
	app, err := NewDaemonApp(ctx, DaemonOptions{LoadDotEnv: true, SetRlimit: true})
	if err != nil {
		return err
	}
	return app.Run(ctx)
}

func resolveCLIClientConfig(hostFlag string) (cliClientConfig, error) {
	hostFlag = strings.TrimSpace(hostFlag)
	if hostFlag != "" {
		baseURL, err := normalizeCLIHost("--host", hostFlag)
		if err != nil {
			return cliClientConfig{}, commandExitError{Code: exitCodeUsage, Err: err}
		}
		config := cliClientConfig{
			BaseURL:     baseURL,
			Source:      "--host",
			SourceValue: hostFlag,
		}
		applyCLIAuthFromEnv(&config)
		return config, nil
	}

	if envHost := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_HOST")); envHost != "" {
		baseURL, err := normalizeCLIHost("AGENT_COMPOSE_HOST", envHost)
		if err != nil {
			return cliClientConfig{}, commandExitError{Code: exitCodeUsage, Err: err}
		}
		config := cliClientConfig{
			BaseURL:     baseURL,
			Source:      "AGENT_COMPOSE_HOST",
			SourceValue: envHost,
		}
		applyCLIAuthFromEnv(&config)
		return config, nil
	}

	socketPath, err := resolveAgentComposeSocketForCLI(os.Getenv("AGENT_COMPOSE_SOCKET"))
	if err != nil {
		return cliClientConfig{}, commandExitError{Code: exitCodeUsage, Err: err}
	}
	return cliClientConfig{
		BaseURL:       "http://agent-compose",
		SocketPath:    socketPath,
		Source:        "AGENT_COMPOSE_SOCKET",
		SourceValue:   socketPath,
		UseUnixSocket: true,
	}, nil
}

func applyCLIAuthFromEnv(config *cliClientConfig) {
	config.AuthUsername = os.Getenv("AUTH_USERNAME")
	config.AuthPassword = os.Getenv("AUTH_PASSWORD")
}

func normalizeCLIHost(name, value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid %s %q: %w", name, value, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid %s %q: scheme must be http or https", name, value)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid %s %q: host is required", name, value)
	}
	return strings.TrimRight(value, "/"), nil
}

func resolveAgentComposeSocketForCLI(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = config.DefaultAgentComposeSocket()
	}
	if value == "" {
		return "", fmt.Errorf("AGENT_COMPOSE_SOCKET is empty")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("invalid AGENT_COMPOSE_SOCKET %q: path contains NUL byte", value)
	}
	resolved, err := filepath.Abs(value)
	if err != nil {
		return value, nil
	}
	return resolved, nil
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
		return nil, fmt.Errorf("daemon via %s %q returned HTTP %d: %s", clientConfig.Source, clientConfig.SourceValue, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func newDaemonHTTPClient(clientConfig cliClientConfig) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if clientConfig.UseUnixSocket {
		socketPath := clientConfig.SocketPath
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		}
	}
	var roundTripper http.RoundTripper = transport
	if !clientConfig.UseUnixSocket && (clientConfig.AuthUsername != "" || clientConfig.AuthPassword != "") {
		roundTripper = basicAuthRoundTripper{
			username: clientConfig.AuthUsername,
			password: clientConfig.AuthPassword,
			next:     roundTripper,
		}
	}
	return &http.Client{
		Transport: roundTripper,
		Timeout:   10 * time.Minute,
	}
}

type basicAuthRoundTripper struct {
	username string
	password string
	next     http.RoundTripper
}

func (t basicAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.SetBasicAuth(t.username, t.password)
	return t.next.RoundTrip(cloned)
}
