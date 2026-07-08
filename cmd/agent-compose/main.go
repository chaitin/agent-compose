package main

import (
	"bufio"
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
	"sort"
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
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"agent-compose/pkg/fxgo/echofn"
	"agent-compose/pkg/fxgo/restful"
	"agent-compose/pkg/fxgo/utils"

	"agent-compose/pkg/agentcompose/api"
	agentcomposeapp "agent-compose/pkg/agentcompose/app"
	"agent-compose/pkg/compose"
	"agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/health"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

const optionalRunModeFlagNoValue = "\x00agent-compose-run-mode"

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
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return commandExitError{Code: exitCodeUsage, Err: err}
	})
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
		Short: "Stop project schedulers and running sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeDownCommand(cmd, options)
		},
	}

	runOptions := composeRunOptions{}
	runCmd := &cobra.Command{
		Use:   "run <agent>",
		Short: "Run a project agent",
		Args:  composeRunArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeRunCommand(cmd, options, runOptions, args)
		},
	}
	runCmd.Flags().StringVar(&runOptions.Prompt, "prompt", "", "Prompt to send to the agent")
	runCmd.Flags().StringVar(&runOptions.Command, "command", "", "Bash command to execute in the agent sandbox")
	runCmd.Flags().StringVar(&runOptions.SandboxID, "sandbox-id", "", "Reuse an existing sandbox")
	runCmd.Flags().StringVar(&runOptions.Driver, "driver", "", "Runtime driver override for a new sandbox")
	runCmd.Flags().BoolVar(&runOptions.KeepRunning, "keep-running", false, "Keep the sandbox runtime running after completion")
	runCmd.Flags().BoolVar(&runOptions.Remove, "rm", false, "Remove the sandbox after a successful run")
	runCmd.Flags().BoolVar(&runOptions.Jupyter, "jupyter", false, "Enable Jupyter for this run")
	runCmd.Flags().BoolVar(&runOptions.JupyterExpose, "jupyter-expose", false, "Mark the Jupyter proxy endpoint for this run as user-accessible")
	runCmd.Flags().BoolVarP(&runOptions.Detach, "detach", "d", false, "Start the run in the daemon and return immediately")
	runCmd.Flags().BoolVarP(&runOptions.Interactive, "interactive", "i", false, "Reserved for future interactive runs")
	runCmd.Flags().Lookup("prompt").NoOptDefVal = optionalRunModeFlagNoValue
	runCmd.Flags().Lookup("command").NoOptDefVal = optionalRunModeFlagNoValue
	hideOptionalFlagNoValueInUsage(runCmd, "prompt", "command")

	schedulerTriggerOptions := composeSchedulerTriggerOptions{}
	schedulerCmd := &cobra.Command{
		Use:   "scheduler",
		Short: "List, trigger, and inspect project scheduler triggers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	schedulerLSCmd := &cobra.Command{
		Use:   "ls [agent]",
		Short: "List project scheduler triggers",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerListCommand(cmd, options, args)
		},
	}
	schedulerTriggerCmd := &cobra.Command{
		Use:   "trigger <agent> <trigger>",
		Short: "Manually run a scheduler trigger",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerTriggerCommand(cmd, options, schedulerTriggerOptions, args[0], args[1])
		},
	}
	schedulerTriggerCmd.Flags().StringVar(&schedulerTriggerOptions.SandboxID, "sandbox-id", "", "Reuse an existing sandbox")
	schedulerTriggerCmd.Flags().StringVar(&schedulerTriggerOptions.Driver, "driver", "", "Runtime driver override for a new sandbox")
	schedulerTriggerCmd.Flags().BoolVar(&schedulerTriggerOptions.KeepRunning, "keep-running", false, "Keep the sandbox runtime running after completion")
	schedulerTriggerCmd.Flags().BoolVar(&schedulerTriggerOptions.Remove, "rm", false, "Remove the sandbox after a successful run")
	schedulerTriggerCmd.Flags().BoolVar(&schedulerTriggerOptions.Jupyter, "jupyter", false, "Enable Jupyter for this run")
	schedulerTriggerCmd.Flags().BoolVar(&schedulerTriggerOptions.JupyterExpose, "jupyter-expose", false, "Mark the Jupyter proxy endpoint for this run as user-accessible")
	schedulerTriggerCmd.Flags().BoolVarP(&schedulerTriggerOptions.Detach, "detach", "d", false, "Start the run in the daemon and return immediately")
	schedulerInspectCmd := &cobra.Command{
		Use:   "inspect <agent> <trigger>",
		Short: "Inspect a scheduler trigger",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerInspectCommand(cmd, options, args[0], args[1])
		},
	}
	schedulerCmd.AddCommand(schedulerLSCmd, schedulerTriggerCmd, schedulerInspectCmd)

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

	statsCmd := &cobra.Command{
		Use:   "stats [sandbox]",
		Short: "Print sandbox resource stats",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeStatsCommand(cmd, options, args)
		},
	}

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

	sandboxPSOptions := composePSOptions{}
	sandboxLSCmd := &cobra.Command{
		Use:   "ls",
		Short: "List project sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposePSCommand(cmd, options, sandboxPSOptions)
		},
	}
	sandboxLSCmd.Flags().BoolVarP(&sandboxPSOptions.All, "all", "a", false, "Show all recognizable sandboxes")
	sandboxLSCmd.Flags().StringVar(&sandboxPSOptions.Status, "status", "", "Filter sandboxes by status, comma-separated")
	sandboxLSCmd.Flags().BoolVar(&sandboxPSOptions.Verbose, "verbose", false, "Show more sandbox details")

	sandboxStopCmd := &cobra.Command{
		Use:   "stop <sandbox> [<sandbox N>]",
		Short: "Stop one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxActionCommand(cmd, options, "stop", "stopped", args)
		},
	}

	sandboxResumeCmd := &cobra.Command{
		Use:   "resume <sandbox> [<sandbox N>]",
		Short: "Resume one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxActionCommand(cmd, options, "resume", "resumed", args)
		},
	}

	sandboxRemoveOptions := composeSandboxRemoveOptions{}
	sandboxRMCmd := &cobra.Command{
		Use:   "rm <sandbox> [<sandbox N>]",
		Short: "Remove one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxRemoveCommand(cmd, options, sandboxRemoveOptions, args)
		},
	}
	sandboxRMCmd.Flags().BoolVar(&sandboxRemoveOptions.Force, "force", false, "Force remove running sandboxes")

	sandboxPruneOptions := composeSandboxPruneOptions{}
	sandboxPruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune stopped or failed sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxPruneCommand(cmd, options, sandboxPruneOptions)
		},
	}
	addSandboxPruneFlags(sandboxPruneCmd, &sandboxPruneOptions)

	sandboxCmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage project sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	sandboxCmd.AddCommand(sandboxLSCmd, sandboxStopCmd, sandboxResumeCmd, sandboxRMCmd, sandboxPruneCmd)

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
	execCmd.Flags().StringVar(&execOptions.AgentName, "agent", "", "Deprecated target selection by agent; use exec <sandbox>")
	// Deprecated: use `agent-compose exec <sandbox>` instead.
	execCmd.Flags().StringVar(&execOptions.RunID, "run-id", "", "Deprecated target selection by run; use exec <sandbox>")
	execCmd.Flags().StringVar(&execOptions.Command, "command", "", "Shell command to execute in the sandbox")
	execCmd.Flags().BoolVarP(&execOptions.Interactive, "interactive", "i", false, "Reserved for future interactive exec")
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

	cacheCmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage daemon runtime caches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cacheLSOptions := composeCacheFilterOptions{}
	cacheLSCmd := &cobra.Command{
		Use:   "ls",
		Short: "List daemon runtime caches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeCacheListCommand(cmd, options, cacheLSOptions)
		},
	}
	addCacheFilterFlags(cacheLSCmd, &cacheLSOptions)
	cacheInspectCmd := &cobra.Command{
		Use:   "inspect <cache-id>",
		Short: "Inspect a daemon runtime cache item",
		Args:  cacheInspectArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeCacheInspectCommand(cmd, options, args[0])
		},
	}
	cachePruneOptions := composeCachePruneOptions{}
	cachePruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune daemon runtime caches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeCachePruneCommand(cmd, options, cachePruneOptions)
		},
	}
	addCachePruneFlags(cachePruneCmd, &cachePruneOptions)
	cacheRemoveOptions := composeCacheRemoveOptions{}
	cacheRemoveCmd := &cobra.Command{
		Use:   "rm <cache-id>",
		Short: "Remove a daemon runtime cache item",
		Args:  cacheRemoveArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeCacheRemoveCommand(cmd, options, cacheRemoveOptions, args[0])
		},
	}
	addCacheRemoveFlags(cacheRemoveCmd, &cacheRemoveOptions)
	cacheCmd.AddCommand(cacheLSCmd, cacheInspectCmd, cachePruneCmd, cacheRemoveCmd)

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

	buildOptions := composeImageBuildOptions{}
	buildCmd := &cobra.Command{
		Use:   "build [agent...]",
		Short: "Build project agent images",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeBuildCommand(cmd, options, buildOptions, args)
		},
	}
	addImageBuildFlags(buildCmd, &buildOptions)
	imageBuildOptions := composeImageBuildOptions{}
	imageBuildCmd := &cobra.Command{
		Use:   "build [agent...]",
		Short: "Build project agent images",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose image build", "agent-compose build"); err != nil {
				return err
			}
			return runComposeBuildCommand(cmd, options, imageBuildOptions, args)
		},
	}
	addImageBuildFlags(imageBuildCmd, &imageBuildOptions)

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
	imageCmd.AddCommand(imageLSCmd, imagePullCmd, imageBuildCmd, imageRemoveCmd, imageInspectCmd)

	inspectCmd := &cobra.Command{
		Use:   "inspect <project|agent|run|sandbox|image|cache> [name-or-id]",
		Short: "Inspect project, agent, run, sandbox, image, or cache details",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeInspectCommand(cmd, options, args)
		},
	}

	root.AddCommand(daemonCmd, versionCmd, statusCmd, configCmd, listCmd, upCmd, downCmd, runCmd, schedulerCmd, logsCmd, psCmd, statsCmd, sandboxCmd, stopCmd, resumeCmd, rmCmd, execCmd, imagesCmd, cacheCmd, imageCmd, pullCmd, buildCmd, rmiCmd, inspectCmd)
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
	Prompt        string
	Command       string
	SandboxID     string
	Driver        string
	KeepRunning   bool
	Remove        bool
	Jupyter       bool
	JupyterExpose bool
	Detach        bool
	Interactive   bool
}

type composeSchedulerTriggerOptions struct {
	SandboxID     string
	Driver        string
	KeepRunning   bool
	Remove        bool
	Jupyter       bool
	JupyterExpose bool
	Detach        bool
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
	AgentName   string
	RunID       string
	Command     string
	Cwd         string
	Interactive bool
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

type composeSandboxPruneOptions struct {
	Status    string
	Agent     string
	Driver    string
	OlderThan string
	Force     bool
}

type composeImageListOptions struct {
	Query string
	All   bool
}

type composeImagePullOptions struct {
	Platform string
}

type composeImageBuildOptions struct {
	Tags       []string
	Dockerfile string
	Target     string
	BuildArgs  []string
	Platform   string
	NoCache    bool
	Pull       bool
}

type composeImageRemoveOptions struct {
	Force         bool
	PruneChildren bool
}

type composeCacheFilterOptions struct {
	Driver string
	Type   string
	Status string
}

type composeCachePruneOptions struct {
	composeCacheFilterOptions
	Unused            bool
	Orphaned          bool
	Expired           bool
	OlderThan         string
	IncludeReferenced bool
	Force             bool
}

type composeCacheRemoveOptions struct {
	Force bool
}

func addImageListFlags(cmd *cobra.Command, options *composeImageListOptions) {
	cmd.Flags().StringVar(&options.Query, "query", "", "Filter images by reference")
	cmd.Flags().BoolVarP(&options.All, "all", "a", false, "Show all images")
}

func addImagePullFlags(cmd *cobra.Command, options *composeImagePullOptions) {
	cmd.Flags().StringVar(&options.Platform, "platform", "", "Pull platform as os/arch[/variant]")
}

func addImageBuildFlags(cmd *cobra.Command, options *composeImageBuildOptions) {
	cmd.Flags().StringArrayVarP(&options.Tags, "tag", "t", nil, "Name and optionally tag in name:tag format")
	cmd.Flags().StringVar(&options.Dockerfile, "dockerfile", "", "Name of the Dockerfile")
	cmd.Flags().StringVar(&options.Target, "target", "", "Build target stage")
	cmd.Flags().StringArrayVar(&options.BuildArgs, "build-arg", nil, "Set build-time variables")
	cmd.Flags().StringVar(&options.Platform, "platform", "", "Build platform as os/arch[/variant]")
	cmd.Flags().BoolVar(&options.NoCache, "no-cache", false, "Do not use cache when building")
	cmd.Flags().BoolVar(&options.Pull, "pull", false, "Always attempt to pull a newer base image")
}

func addImageRemoveFlags(cmd *cobra.Command, options *composeImageRemoveOptions) {
	cmd.Flags().BoolVar(&options.Force, "force", false, "Force image removal")
	cmd.Flags().BoolVar(&options.PruneChildren, "prune-children", false, "Remove untagged child images")
}

func addCacheFilterFlags(cmd *cobra.Command, options *composeCacheFilterOptions) {
	cmd.Flags().StringVar(&options.Driver, "driver", "", "Filter caches by driver: docker, boxlite, microsandbox, or all")
	cmd.Flags().StringVar(&options.Type, "type", "", "Filter caches by type: oci, materialized, runtime, or sandbox")
	cmd.Flags().StringVar(&options.Status, "status", "", "Filter caches by status: active, referenced, unused, expired, orphaned, or unknown")
}

func addCachePruneFlags(cmd *cobra.Command, options *composeCachePruneOptions) {
	addCacheFilterFlags(cmd, &options.composeCacheFilterOptions)
	cmd.Flags().BoolVar(&options.Unused, "unused", false, "Only match unused caches")
	cmd.Flags().BoolVar(&options.Orphaned, "orphaned", false, "Only match orphaned caches")
	cmd.Flags().BoolVar(&options.Expired, "expired", false, "Only match expired caches")
	cmd.Flags().StringVar(&options.OlderThan, "older-than", "", "Only match caches older than a duration such as 7d or 24h")
	cmd.Flags().BoolVar(&options.IncludeReferenced, "include-referenced", false, "Allow referenced caches to be removed")
	cmd.Flags().BoolVar(&options.Force, "force", false, "Actually remove matched caches")
}

func addSandboxPruneFlags(cmd *cobra.Command, options *composeSandboxPruneOptions) {
	cmd.Flags().StringVar(&options.Status, "status", "", "Filter sandboxes by status, comma-separated")
	cmd.Flags().StringVar(&options.Agent, "agent", "", "Filter sandboxes by agent name")
	cmd.Flags().StringVar(&options.Driver, "driver", "", "Filter sandboxes by driver: docker, boxlite, or microsandbox")
	cmd.Flags().StringVar(&options.OlderThan, "older-than", "", "Only match sandboxes older than a duration such as 7d or 24h")
	cmd.Flags().BoolVar(&options.Force, "force", false, "Actually remove matched sandboxes")
}

func addCacheRemoveFlags(cmd *cobra.Command, options *composeCacheRemoveOptions) {
	cmd.Flags().BoolVar(&options.Force, "force", false, "Actually remove the cache item")
}

func cacheInspectArgs(_ *cobra.Command, args []string) error {
	if len(args) != 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("cache inspect accepts 1 arg(s), received %d", len(args))}
	}
	return nil
}

func cacheRemoveArgs(_ *cobra.Command, args []string) error {
	if len(args) != 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("cache rm accepts 1 arg(s), received %d", len(args))}
	}
	return nil
}

func composeRunArgs(_ *cobra.Command, args []string) error {
	if len(args) < 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run requires an agent")}
	}
	return nil
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
			Err:  fmt.Errorf("down project %s completed with %d sandbox stop failure(s)", normalized.Name, output.FailedSessionStops),
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

func runComposeSandboxPruneCommand(cmd *cobra.Command, cli cliOptions, options composeSandboxPruneOptions) error {
	statusFilter, err := sandboxPruneStatusFilter(options.Status)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	driverFilter, err := sandboxPruneDriverFilterValue(options.Driver)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	options.Driver = driverFilter
	var olderThanSeconds uint64
	if strings.TrimSpace(options.OlderThan) != "" {
		olderThanSeconds, err = parseOlderThanSeconds(options.OlderThan)
		if err != nil {
			return commandExitError{Code: exitCodeUsage, Err: err}
		}
	}
	composePath, normalized, projectID, err := resolveComposeProject(cli)
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
		return commandExitErrorForComposeProject(fmt.Errorf("get project %s: %w", normalized.Name, err), "sandbox prune", normalized.Name, composePath)
	}
	psOutput, err := composePSOutputFromProject(cmd.Context(), clients, project.Msg.GetProject(), composePSOptions{All: true})
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("build sandbox prune candidates for project %s: %w", normalized.Name, err))
	}
	output := composeSandboxPruneDryRunOutput(psOutput.Sandboxes, statusFilter, options, olderThanSeconds)
	if options.Force {
		output.DryRun = false
		for _, sandbox := range output.Matched {
			if err := removeSandbox(cmd.Context(), clients.sandbox, sandbox.Sandbox, false); err != nil {
				output.Skipped = append(output.Skipped, composeSandboxPruneSkipped{
					Sandbox:   sandbox.Sandbox,
					Agent:     sandbox.Agent,
					Status:    sandbox.Status,
					Driver:    sandbox.Driver,
					UpdatedAt: firstNonEmptyString(sandbox.UpdatedAt, sandbox.CreatedAt),
					Reason:    fmt.Sprintf("remove failed: %s", err),
				})
				continue
			}
			output.Removed = append(output.Removed, sandbox.Sandbox)
		}
	}
	if err := writeSandboxPruneOutput(cmd.OutOrStdout(), cli.JSON, output); err != nil {
		return err
	}
	if len(output.Skipped) > 0 {
		return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("sandbox prune skipped %d sandbox(es)", len(output.Skipped))}
	}
	return nil
}

func composeSandboxPruneDryRunOutput(sandboxes []composePSSandboxOutput, statusFilter map[string]bool, options composeSandboxPruneOptions, olderThanSeconds uint64) composeSandboxPruneOutput {
	output := composeSandboxPruneOutput{
		DryRun:  true,
		Matched: []composePSSandboxOutput{},
		Removed: []string{},
		Skipped: []composeSandboxPruneSkipped{},
	}
	agentFilter := strings.ToLower(strings.TrimSpace(options.Agent))
	driverFilter := strings.ToLower(strings.TrimSpace(options.Driver))
	var cutoff time.Time
	if olderThanSeconds > 0 {
		cutoff = time.Now().UTC().Add(-time.Duration(olderThanSeconds) * time.Second)
	}
	for _, sandbox := range sandboxes {
		status := strings.ToLower(strings.TrimSpace(sandbox.Status))
		if !statusFilter[status] {
			continue
		}
		if agentFilter != "" && strings.ToLower(strings.TrimSpace(sandbox.Agent)) != agentFilter {
			continue
		}
		if driverFilter != "" && strings.ToLower(strings.TrimSpace(sandbox.Driver)) != driverFilter {
			continue
		}
		if !cutoff.IsZero() {
			timestamp, _, err := sandboxPruneTimestamp(sandbox)
			if err != nil {
				output.Warnings = append(output.Warnings, fmt.Sprintf("sandbox %s skipped: %s", sandbox.Sandbox, err))
				continue
			}
			if timestamp.After(cutoff) {
				continue
			}
		}
		output.Matched = append(output.Matched, sandbox)
	}
	return output
}

func sandboxPruneStatusFilter(value string) (map[string]bool, error) {
	result := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		status := strings.ToLower(strings.TrimSpace(item))
		if status == "" {
			continue
		}
		if status == "running" || status == "pending" {
			return nil, fmt.Errorf("sandbox prune cannot target %s sandboxes; use `agent-compose sandbox rm --force <sandbox>` for running sandboxes", status)
		}
		result[status] = true
	}
	if len(result) == 0 {
		result["stopped"] = true
		result["failed"] = true
	}
	return result, nil
}

func sandboxPruneDriverFilterValue(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "docker", "boxlite", "microsandbox":
		return strings.ToLower(strings.TrimSpace(value)), nil
	default:
		return "", fmt.Errorf("invalid --driver %q: expected docker, boxlite, or microsandbox", value)
	}
}

func sandboxPruneTimestamp(sandbox composePSSandboxOutput) (time.Time, string, error) {
	source := "updated_at"
	value := strings.TrimSpace(sandbox.UpdatedAt)
	if value == "" {
		source = "created_at"
		value = strings.TrimSpace(sandbox.CreatedAt)
	}
	if value == "" {
		return time.Time{}, source, fmt.Errorf("missing updated_at and created_at")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, source, fmt.Errorf("invalid %s %q", source, value)
	}
	return parsed.UTC(), source, nil
}

func removeSandbox(ctx context.Context, client agentcomposev2connect.SandboxServiceClient, sandboxID string, force bool) error {
	_, err := client.RemoveSandbox(ctx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{
		SandboxId: sandboxID,
		Force:     force,
	}))
	return err
}

func runComposeStatsCommand(cmd *cobra.Command, cli cliOptions, args []string) error {
	if len(args) > 0 {
		return runComposeSingleStatsCommand(cmd, cli, args[0])
	}
	composePath, normalized, projectID, err := resolveComposeProject(cli)
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
		return commandExitErrorForComposeProject(fmt.Errorf("get project %s: %w", normalized.Name, err), "stats", normalized.Name, composePath)
	}
	output, err := composeProjectStatsOutputFromProject(cmd.Context(), clients, project.Msg.GetProject())
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("build stats for project %s: %w", normalized.Name, err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeStatsText(cmd.OutOrStdout(), output.Stats)
}

func runComposeSingleStatsCommand(cmd *cobra.Command, cli cliOptions, sandboxID string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("stats requires non-empty sandbox")}
	}
	output, err := composeStatsOutputForSandbox(cmd.Context(), clients.sandbox, sandboxID)
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("get sandbox %s stats: %w", sandboxID, err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeStatsText(cmd.OutOrStdout(), []composeStatsOutput{output})
}

func runComposeRunCommand(cmd *cobra.Command, cli cliOptions, options composeRunOptions, args []string) error {
	normalizedOptions, err := normalizeComposeRunOptions(cmd, options)
	if err != nil {
		return err
	}
	promptFlagChanged := cmd.Flags().Changed("prompt")
	commandFlagChanged := cmd.Flags().Changed("command")
	prompt := normalizeOptionalRunModeValue(normalizedOptions.Prompt)
	commandText := normalizeOptionalRunModeValue(normalizedOptions.Command)
	if promptFlagChanged && normalizedOptions.Prompt == optionalRunModeFlagNoValue && len(args) > 1 {
		prompt = strings.TrimSpace(args[1])
		args = append(args[:1], args[2:]...)
	}
	if commandFlagChanged && normalizedOptions.Command == optionalRunModeFlagNoValue && len(args) > 1 {
		commandText = strings.TrimSpace(args[1])
		args = append(args[:1], args[2:]...)
	}
	if normalizedOptions.Interactive && len(args) > 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -i/--interactive does not accept additional positional arguments")}
	}
	if len(args) > 1 {
		if promptFlagChanged {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run with --prompt does not accept additional positional arguments")}
		}
		if commandFlagChanged {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run with --command does not accept additional positional arguments")}
		}
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run does not accept positional trigger arguments; use scheduler trigger <agent> <trigger>")}
	}
	if len(args) == 0 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run requires an agent")}
	}
	if normalizedOptions.Detach && normalizedOptions.Interactive {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -d/--detach cannot be combined with -i/--interactive")}
	}
	if normalizedOptions.Interactive && cli.JSON {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -i/--interactive cannot be combined with --json")}
	}
	if normalizedOptions.Interactive && promptFlagChanged == commandFlagChanged {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -i/--interactive requires exactly one of --prompt or --command")}
	}
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	agentName := strings.TrimSpace(args[0])
	if normalizedOptions.Interactive && promptFlagChanged {
		if err := validateInteractivePromptProvider(normalized, agentName); err != nil {
			return err
		}
	}
	if !normalizedOptions.Interactive && cmd.Flags().Changed("command") && commandText == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run --command requires a non-empty command")}
	}
	if !normalizedOptions.Interactive && cmd.Flags().Changed("prompt") && prompt == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run --prompt requires a non-empty prompt")}
	}
	modeCount := 0
	if !normalizedOptions.Interactive {
		for _, value := range []string{prompt, commandText} {
			if value != "" {
				modeCount++
			}
		}
	}
	if modeCount > 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run requires only one of --prompt or --command")}
	}
	if !normalizedOptions.Interactive && modeCount == 0 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run requires --prompt or --command")}
	}
	clientConfig, err := resolveCLIClientConfig(cli.Host)
	if err != nil {
		return err
	}
	cleanupPolicy := agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_STOP_ON_COMPLETION
	if normalizedOptions.KeepRunning {
		cleanupPolicy = agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING
	} else if normalizedOptions.Remove {
		cleanupPolicy = agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_REMOVE_ON_COMPLETION
	}
	client := agentcomposev2connect.NewRunServiceClient(newDaemonHTTPClient(clientConfig), clientConfig.BaseURL)
	var jupyter *agentcomposev2.RunJupyterSpec
	if normalizedOptions.Jupyter || normalizedOptions.JupyterExpose {
		jupyter = &agentcomposev2.RunJupyterSpec{
			Enabled: normalizedOptions.Jupyter || normalizedOptions.JupyterExpose,
			Expose:  normalizedOptions.JupyterExpose,
		}
	}
	runReq := &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       agentName,
		Prompt:          prompt,
		Command:         commandText,
		Source:          agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
		SessionId:       strings.TrimSpace(normalizedOptions.SandboxID),
		Driver:          strings.TrimSpace(normalizedOptions.Driver),
		CleanupPolicy:   cleanupPolicy,
		ClientRequestId: manualRunClientRequestID(normalized.Name, agentName, firstNonEmptyString(prompt, commandText)),
		Jupyter:         jupyter,
	}
	if normalizedOptions.Detach {
		return startDetachedRun(cmd, cli, normalized.Name, client, runReq)
	}
	if normalizedOptions.Interactive {
		runReq.Prompt = ""
		runReq.Command = ""
		runReq.CleanupPolicy = agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING
		sandboxClient := agentcomposev2connect.NewSandboxServiceClient(newDaemonHTTPClient(clientConfig), clientConfig.BaseURL)
		return runInteractiveComposeRun(cmd, normalizedOptions, normalized.Name, client, sandboxClient, runReq, promptFlagChanged, prompt, commandText)
	}
	return executeComposeRunRequest(cmd, cli, normalized.Name, projectID, client, runReq, normalizedOptions.Detach)
}

func executeComposeRunRequest(cmd *cobra.Command, cli cliOptions, projectName, projectID string, client agentcomposev2connect.RunServiceClient, runReq *agentcomposev2.RunAgentRequest, detach bool) error {
	if detach {
		return startDetachedRun(cmd, cli, projectName, client, runReq)
	}
	detail, completed, warnings, err := runComposeRunStreamAndDetail(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), client, projectID, projectName, runReq, cli.JSON)
	if err != nil {
		return err
	}
	if cli.JSON {
		output := composeRunOutputFromDetail(detail)
		output.Warnings = appendUniqueStrings(output.Warnings, warnings...)
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
			return err
		}
	}
	if !cli.JSON {
		if err := writeRunWarnings(cmd.ErrOrStderr(), warnings); err != nil {
			return err
		}
	}
	return composeRunCompletionError(projectName, runReq.GetAgentName(), completed, detail)
}

func runComposeSchedulerListCommand(cmd *cobra.Command, cli cliOptions, args []string) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	agentFilter := ""
	if len(args) > 0 {
		agentFilter = strings.TrimSpace(args[0])
	}
	triggers, err := listComposeSchedulerTriggers(cmd.Context(), clients, normalized, projectID, agentFilter)
	if err != nil {
		return err
	}
	output := composeSchedulerListOutput{
		Project:  composeUpProjectOutput{ID: projectID, Name: normalized.Name},
		Triggers: triggers,
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeSchedulerListText(cmd.OutOrStdout(), output)
}

func runComposeSchedulerTriggerCommand(cmd *cobra.Command, cli cliOptions, options composeSchedulerTriggerOptions, agentName, triggerRef string) error {
	options, err := normalizeComposeSchedulerTriggerOptions(options)
	if err != nil {
		return err
	}
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	trigger, err := resolveComposeSchedulerTrigger(cmd.Context(), clients, normalized, projectID, agentName, triggerRef)
	if err != nil {
		return err
	}
	cleanupPolicy := agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_STOP_ON_COMPLETION
	if options.KeepRunning {
		cleanupPolicy = agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING
	} else if options.Remove {
		cleanupPolicy = agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_REMOVE_ON_COMPLETION
	}
	var jupyter *agentcomposev2.RunJupyterSpec
	if options.Jupyter || options.JupyterExpose {
		jupyter = &agentcomposev2.RunJupyterSpec{
			Enabled: options.Jupyter || options.JupyterExpose,
			Expose:  options.JupyterExpose,
		}
	}
	runReq := &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       trigger.AgentName,
		Source:          agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
		SessionId:       strings.TrimSpace(options.SandboxID),
		Driver:          strings.TrimSpace(options.Driver),
		SchedulerId:     trigger.SchedulerID,
		TriggerId:       trigger.TriggerID,
		CleanupPolicy:   cleanupPolicy,
		ClientRequestId: manualRunClientRequestID(normalized.Name, trigger.AgentName, trigger.TriggerID),
		Jupyter:         jupyter,
	}
	return executeComposeRunRequest(cmd, cli, normalized.Name, projectID, clients.run, runReq, options.Detach)
}

func runComposeSchedulerInspectCommand(cmd *cobra.Command, cli cliOptions, agentName, triggerRef string) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	trigger, err := resolveComposeSchedulerTrigger(cmd.Context(), clients, normalized, projectID, agentName, triggerRef)
	if err != nil {
		return err
	}
	output := composeSchedulerInspectOutput{
		Project:   composeUpProjectOutput{ID: projectID, Name: normalized.Name},
		Source:    trigger.Source,
		AgentName: trigger.AgentName,
		Trigger:   trigger,
	}
	if trigger.Source == "declarative" && trigger.declarative != nil {
		output.Definition = api.TriggerYAMLShape(trigger.declarative)
	} else if trigger.registered != nil {
		output.Registered = loaderTriggerYAMLShape(trigger.registered)
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeSchedulerInspectText(cmd.OutOrStdout(), output)
}

func normalizeComposeSchedulerTriggerOptions(options composeSchedulerTriggerOptions) (composeSchedulerTriggerOptions, error) {
	options.SandboxID = strings.TrimSpace(options.SandboxID)
	options.Driver = strings.TrimSpace(options.Driver)
	if options.Driver != "" {
		driver, err := driverpkg.ResolveSessionRuntimeDriver(options.Driver, "")
		if err != nil {
			return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler trigger --driver: %w", err)}
		}
		options.Driver = driver
	}
	if options.SandboxID != "" && options.Driver != "" {
		return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler trigger --driver cannot be combined with --sandbox-id")}
	}
	return options, nil
}

func listComposeSchedulerTriggers(ctx context.Context, clients cliServiceClients, normalized *compose.NormalizedProjectSpec, projectID, agentFilter string) ([]composeSchedulerTriggerItem, error) {
	var items []composeSchedulerTriggerItem
	for _, agent := range normalized.Agents {
		if agentFilter != "" && agent.Name != agentFilter {
			continue
		}
		if agent.Scheduler == nil {
			continue
		}
		schedulerID, err := domain.StableProjectSchedulerID(projectID, agent.Name, "")
		if err != nil {
			return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolve scheduler for agent %q: %w", agent.Name, err)}
		}
		managedLoaderID, err := domain.StableManagedLoaderID(projectID, agent.Name, "")
		if err != nil {
			return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolve scheduler loader for agent %q: %w", agent.Name, err)}
		}
		schedulerEnabled := agent.Scheduler.Enabled
		if strings.TrimSpace(agent.Scheduler.Script) != "" {
			loader, err := clients.loader.GetLoader(ctx, connect.NewRequest(&agentcomposev1.LoaderIDRequest{LoaderId: managedLoaderID}))
			if err != nil {
				return nil, commandExitErrorForConnect(fmt.Errorf("get scheduler loader %s: %w", managedLoaderID, err))
			}
			for _, trigger := range loader.Msg.GetLoader().GetTriggers() {
				items = append(items, schedulerTriggerItemFromRegistered(agent.Name, schedulerID, managedLoaderID, schedulerEnabled, trigger))
			}
			continue
		}
		for index, trigger := range agent.Scheduler.Triggers {
			id, err := domain.StableManagedTriggerID(projectID, agent.Name, "", trigger.Name, index)
			if err != nil {
				return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolve trigger for agent %q: %w", agent.Name, err)}
			}
			items = append(items, schedulerTriggerItemFromDeclarative(agent.Name, schedulerID, managedLoaderID, schedulerEnabled, id, trigger))
		}
	}
	if agentFilter != "" && len(items) == 0 {
		if _, ok := composeRunAgentSpec(normalized, agentFilter); !ok {
			return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent %q is not configured in this project", agentFilter)}
		}
	}
	return items, nil
}

func resolveComposeSchedulerTrigger(ctx context.Context, clients cliServiceClients, normalized *compose.NormalizedProjectSpec, projectID, agentName, triggerRef string) (composeSchedulerTriggerItem, error) {
	agentName = strings.TrimSpace(agentName)
	triggerRef = strings.TrimSpace(triggerRef)
	if agentName == "" || triggerRef == "" {
		return composeSchedulerTriggerItem{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler trigger requires non-empty agent and trigger")}
	}
	items, err := listComposeSchedulerTriggers(ctx, clients, normalized, projectID, agentName)
	if err != nil {
		return composeSchedulerTriggerItem{}, err
	}
	var matches []composeSchedulerTriggerItem
	for _, item := range items {
		if item.TriggerID == triggerRef || (item.Name != "" && item.Name == triggerRef) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return composeSchedulerTriggerItem{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler trigger %q not found for agent %q", triggerRef, agentName)}
	}
	if len(matches) > 1 {
		return composeSchedulerTriggerItem{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler trigger %q for agent %q is ambiguous; use the trigger id", triggerRef, agentName)}
	}
	return matches[0], nil
}

func schedulerTriggerItemFromDeclarative(agentName, schedulerID, managedLoaderID string, schedulerEnabled bool, triggerID string, trigger compose.NormalizedTriggerSpec) composeSchedulerTriggerItem {
	protoTrigger := api.TriggerSpecToProto(trigger)
	return composeSchedulerTriggerItem{
		AgentName:        agentName,
		Name:             strings.TrimSpace(trigger.Name),
		TriggerID:        triggerID,
		Kind:             trigger.Kind,
		Source:           "declarative",
		SchedulerID:      schedulerID,
		ManagedLoaderID:  managedLoaderID,
		SchedulerEnabled: schedulerEnabled,
		TriggerEnabled:   true,
		declarative:      protoTrigger,
	}
}

func schedulerTriggerItemFromRegistered(agentName, schedulerID, managedLoaderID string, schedulerEnabled bool, trigger *agentcomposev1.LoaderTrigger) composeSchedulerTriggerItem {
	return composeSchedulerTriggerItem{
		AgentName:        agentName,
		TriggerID:        trigger.GetTriggerId(),
		Kind:             loaderTriggerKindText(trigger.GetKind()),
		Source:           "script",
		SchedulerID:      schedulerID,
		ManagedLoaderID:  managedLoaderID,
		SchedulerEnabled: schedulerEnabled,
		TriggerEnabled:   trigger.GetEnabled(),
		Topic:            trigger.GetTopic(),
		IntervalMs:       trigger.GetIntervalMs(),
		SpecJSON:         trigger.GetSpecJson(),
		NextFireAt:       trigger.GetNextFireAt(),
		LastFiredAt:      trigger.GetLastFiredAt(),
		registered:       trigger,
	}
}

func writeSchedulerListText(out io.Writer, output composeSchedulerListOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "AGENT\tTRIGGER\tKIND\tSOURCE\tSCHEDULER\tENABLED"); err != nil {
		return err
	}
	for _, trigger := range output.Triggers {
		name := firstNonEmptyString(trigger.Name, trigger.TriggerID)
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%t\n",
			trigger.AgentName,
			name,
			trigger.Kind,
			trigger.Source,
			firstNonEmptyString(trigger.SchedulerID, "-"),
			trigger.TriggerEnabled,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeSchedulerInspectText(out io.Writer, output composeSchedulerInspectOutput) error {
	var target map[string]any
	if output.Source == "declarative" {
		target = output.Definition
	} else {
		target = output.Registered
	}
	data, err := yaml.Marshal(target)
	if err != nil {
		return err
	}
	return writeCommandOutput(out, data)
}

func loaderTriggerYAMLShape(trigger *agentcomposev1.LoaderTrigger) map[string]any {
	raw := map[string]any{
		"loader_id":     trigger.GetLoaderId(),
		"trigger_id":    trigger.GetTriggerId(),
		"kind":          loaderTriggerKindText(trigger.GetKind()),
		"enabled":       trigger.GetEnabled(),
		"auto_id":       trigger.GetAutoId(),
		"interval_ms":   trigger.GetIntervalMs(),
		"topic":         trigger.GetTopic(),
		"spec_json":     trigger.GetSpecJson(),
		"next_fire_at":  trigger.GetNextFireAt(),
		"last_fired_at": trigger.GetLastFiredAt(),
	}
	return raw
}

func loaderTriggerKindText(kind agentcomposev1.LoaderTriggerKind) string {
	switch kind {
	case agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_INTERVAL:
		return "interval"
	case agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_TIMEOUT:
		return "timeout"
	case agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_CRON:
		return "cron"
	case agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_EVENT:
		return "event"
	default:
		return "unspecified"
	}
}

func runComposeRunStreamAndDetail(ctx context.Context, stdout, stderr io.Writer, client agentcomposev2connect.RunServiceClient, projectID, projectName string, runReq *agentcomposev2.RunAgentRequest, suppressOutput bool) (*agentcomposev2.RunDetail, *agentcomposev2.RunSummary, []string, error) {
	stream, err := client.RunAgentStream(ctx, connect.NewRequest(runReq))
	if err != nil {
		return nil, nil, nil, commandExitErrorForConnect(fmt.Errorf("run project %s agent %s: %w", projectName, runReq.GetAgentName(), err))
	}
	var completed *agentcomposev2.RunSummary
	var warnings []string
	var runID string
	defer func() {
		if ctx.Err() != nil && strings.TrimSpace(runID) != "" {
			_, _ = client.StopRun(context.Background(), connect.NewRequest(&agentcomposev2.StopRunRequest{
				RunId:  runID,
				Reason: "client interrupted",
			}))
		}
	}()
	for stream.Receive() {
		event := stream.Msg()
		if strings.TrimSpace(event.GetRunId()) != "" {
			runID = event.GetRunId()
		}
		warnings = appendUniqueStrings(warnings, event.GetWarnings()...)
		if event.GetRun() != nil {
			warnings = appendUniqueStrings(warnings, event.GetRun().GetWarnings()...)
		}
		switch event.GetEventType() {
		case agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT:
			if suppressOutput {
				continue
			}
			if err := writeTranscriptOrChunk(stdout, stderr, event.GetTranscript(), event.GetChunk(), event.GetStream()); err != nil {
				return nil, nil, nil, err
			}
		case agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED:
			completed = event.GetRun()
			if completed.GetRunId() != "" {
				runID = completed.GetRunId()
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, nil, nil, commandExitErrorForConnect(fmt.Errorf("run project %s agent %s: %w", projectName, runReq.GetAgentName(), err))
	}
	if completed == nil {
		return nil, nil, nil, fmt.Errorf("run project %s agent %s: stream completed without terminal run", projectName, runReq.GetAgentName())
	}
	warnings = appendUniqueStrings(warnings, completed.GetWarnings()...)
	detail, err := getRunDetail(ctx, client, projectID, completed.GetRunId())
	if err != nil {
		return nil, nil, nil, commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", completed.GetRunId(), projectName, err))
	}
	return detail.Msg.GetRun(), completed, warnings, nil
}

func composeRunCompletionError(projectName, agentName string, completed *agentcomposev2.RunSummary, detail *agentcomposev2.RunDetail) error {
	cleanupErr := runDetailCleanupError(detail)
	if runSummaryFailed(completed) {
		message := fmt.Sprintf("run %s for project %s agent %s failed: %s", completed.GetRunId(), projectName, agentName, firstNonEmptyString(completed.GetError(), runStatusText(completed.GetStatus())))
		if cleanupErr != "" {
			message += fmt.Sprintf("; cleanup warning: %s", cleanupErr)
		}
		return commandExitError{Code: runSummaryExitCode(completed), Err: fmt.Errorf("%s", message)}
	}
	if cleanupErr != "" {
		return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("run %s for project %s agent %s succeeded but sandbox cleanup failed: %s", completed.GetRunId(), projectName, agentName, cleanupErr)}
	}
	return nil
}

func runInteractiveComposeRun(cmd *cobra.Command, options composeRunOptions, projectName string, client agentcomposev2connect.RunServiceClient, sandboxClient agentcomposev2connect.SandboxServiceClient, baseReq *agentcomposev2.RunAgentRequest, promptMode bool, firstPrompt, firstCommand string) (err error) {
	sessionID := strings.TrimSpace(baseReq.GetSessionId())
	removeOnExit := options.Remove && sessionID == ""
	defer func() {
		if !removeOnExit || strings.TrimSpace(sessionID) == "" {
			return
		}
		removeErr := removeSandbox(context.Background(), sandboxClient, sessionID, true)
		if removeErr == nil {
			return
		}
		wrapped := commandExitErrorForConnect(fmt.Errorf("remove interactive sandbox %s: %w", sessionID, removeErr))
		if err == nil {
			err = wrapped
			return
		}
		_ = writeRunWarnings(cmd.ErrOrStderr(), []string{fmt.Sprintf("interactive sandbox cleanup failed: %v", removeErr)})
	}()

	firstInput := firstCommand
	if promptMode {
		firstInput = firstPrompt
	}
	pending := make([]string, 0, 1)
	if strings.TrimSpace(firstInput) != "" {
		pending = append(pending, firstInput)
	}
	scanner := bufio.NewScanner(cmd.InOrStdin())
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		var line string
		if len(pending) > 0 {
			line = pending[0]
			pending = pending[1:]
		} else {
			if !scanner.Scan() {
				if scanErr := scanner.Err(); scanErr != nil {
					return scanErr
				}
				return nil
			}
			line = scanner.Text()
		}
		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		if input == "/exit" {
			return nil
		}
		runReq := proto.Clone(baseReq).(*agentcomposev2.RunAgentRequest)
		runReq.SessionId = sessionID
		if strings.TrimSpace(sessionID) != "" {
			runReq.Driver = ""
		}
		runReq.ClientRequestId = manualRunClientRequestID(projectName, baseReq.GetAgentName(), input)
		if promptMode {
			runReq.Prompt = input
			runReq.Command = ""
		} else {
			runReq.Prompt = ""
			runReq.Command = input
		}
		detail, completed, warnings, runErr := runComposeRunStreamAndDetail(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), client, baseReq.GetProjectId(), projectName, runReq, false)
		if runErr != nil {
			return runErr
		}
		if completed.GetSessionId() != "" {
			sessionID = completed.GetSessionId()
		}
		if err := writeRunWarnings(cmd.ErrOrStderr(), warnings); err != nil {
			return err
		}
		if err := composeRunCompletionError(projectName, baseReq.GetAgentName(), completed, detail); err != nil {
			return err
		}
	}
}

func startDetachedRun(cmd *cobra.Command, cli cliOptions, projectName string, client agentcomposev2connect.RunServiceClient, req *agentcomposev2.RunAgentRequest) error {
	resp, err := client.StartRun(cmd.Context(), connect.NewRequest(&agentcomposev2.StartRunRequest{Run: req}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("start run project %s agent %s: %w", projectName, req.GetAgentName(), err))
	}
	run := resp.Msg.GetRun()
	if run == nil {
		return fmt.Errorf("start run project %s agent %s: response did not include run summary", projectName, req.GetAgentName())
	}
	warnings := appendUniqueStrings(append([]string(nil), resp.Msg.GetWarnings()...), run.GetWarnings()...)
	logsCommand := detachedRunLogsCommand(cli, run.GetRunId())
	if cli.JSON {
		output := composeRunOutputFromSummary(run, projectName, logsCommand)
		output.Warnings = warnings
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	if err := writeRunWarnings(cmd.ErrOrStderr(), warnings); err != nil {
		return err
	}
	return writeDetachedRunText(cmd.OutOrStdout(), run, logsCommand)
}

func runDetailCleanupError(detail *agentcomposev2.RunDetail) string {
	if detail == nil {
		return ""
	}
	return strings.TrimSpace(detail.GetCleanupError())
}

func writeRunWarnings(out io.Writer, warnings []string) error {
	for _, warning := range appendUniqueStrings(nil, warnings...) {
		if _, err := fmt.Fprintf(out, "warning: %s\n", warning); err != nil {
			return err
		}
	}
	return nil
}

func writeTranscriptOrChunk(stdout, stderr io.Writer, transcript *agentcomposev2.TranscriptEvent, chunk string, stream agentcomposev2.StdioStream) error {
	text := chunk
	if transcript != nil {
		text = transcript.GetText()
		stream = transcript.GetStream()
	}
	if text == "" {
		return nil
	}
	target := stdout
	if stream == agentcomposev2.StdioStream_STDIO_STREAM_STDERR {
		target = stderr
	}
	_, err := io.WriteString(target, text)
	return err
}

func writeDetachedRunText(out io.Writer, run *agentcomposev2.RunSummary, logsCommand string) error {
	if _, err := fmt.Fprintf(out, "Run: %s\nSandbox: %s\nStatus: %s\nLogs: %s\n",
		firstNonEmptyString(run.GetRunId(), "-"),
		firstNonEmptyString(run.GetSessionId(), "-"),
		runStatusText(run.GetStatus()),
		logsCommand,
	); err != nil {
		return err
	}
	return nil
}

func normalizeComposeRunOptions(cmd *cobra.Command, options composeRunOptions) (composeRunOptions, error) {
	options.SandboxID = strings.TrimSpace(options.SandboxID)
	options.Driver = strings.TrimSpace(options.Driver)
	if options.Driver != "" {
		driver, err := driverpkg.ResolveSessionRuntimeDriver(options.Driver, "")
		if err != nil {
			return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run --driver: %w", err)}
		}
		options.Driver = driver
	}
	if options.SandboxID != "" && options.Driver != "" {
		return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run --driver cannot be combined with --sandbox-id")}
	}
	return options, nil
}

func composeRunAgentSpec(normalized *compose.NormalizedProjectSpec, agentName string) (compose.NormalizedAgentSpec, bool) {
	agentName = strings.TrimSpace(agentName)
	if normalized == nil {
		return compose.NormalizedAgentSpec{}, false
	}
	for _, agent := range normalized.Agents {
		if strings.TrimSpace(agent.Name) == agentName {
			return agent, true
		}
	}
	return compose.NormalizedAgentSpec{}, false
}

func normalizeOptionalRunModeValue(value string) string {
	if value == optionalRunModeFlagNoValue {
		return ""
	}
	return strings.TrimSpace(value)
}

func hideOptionalFlagNoValueInUsage(cmd *cobra.Command, flagNames ...string) {
	usageFunc := cmd.UsageFunc()
	cmd.SetUsageFunc(func(c *cobra.Command) error {
		return withHiddenOptionalFlagNoValue(c, flagNames, func() error {
			return usageFunc(c)
		})
	})
}

func withHiddenOptionalFlagNoValue(cmd *cobra.Command, flagNames []string, fn func() error) error {
	type flagRestore struct {
		name        string
		noOptDefVal string
	}
	var restores []flagRestore
	for _, name := range flagNames {
		flag := cmd.Flags().Lookup(name)
		if flag == nil || flag.NoOptDefVal != optionalRunModeFlagNoValue {
			continue
		}
		restores = append(restores, flagRestore{name: name, noOptDefVal: flag.NoOptDefVal})
		flag.NoOptDefVal = ""
	}
	defer func() {
		for _, restore := range restores {
			if flag := cmd.Flags().Lookup(restore.name); flag != nil {
				flag.NoOptDefVal = restore.noOptDefVal
			}
		}
	}()
	return fn()
}

func validateInteractivePromptProvider(project *compose.NormalizedProjectSpec, agentName string) error {
	provider := "codex"
	for _, agent := range project.Agents {
		if strings.TrimSpace(agent.Name) == strings.TrimSpace(agentName) {
			if normalized := normalizeInteractivePromptProvider(agent.Provider); normalized != "" {
				provider = normalized
			}
			break
		}
	}
	switch provider {
	case "codex", "claude", "opencode":
		return nil
	default:
		return commandExitError{
			Code: exitCodeUnsupported,
			Err:  fmt.Errorf("run -i --prompt is unsupported for provider %s; supported providers: codex, claude, opencode", provider),
		}
	}
}

func normalizeInteractivePromptProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		return ""
	case "claude-code", "claude_code":
		return "claude"
	case "open-code", "open_code":
		return "opencode"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
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
		if normalizedOptions.Follow {
			return followRunLogStream(cmd.Context(), cmd.OutOrStdout(), client, projectID, run.Msg.GetRun().GetSummary(), normalizedOptions)
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
	if strings.TrimSpace(options.SandboxID) != "" {
		options.SessionID = options.SandboxID
	}
	if options.TailLines < -1 {
		return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs --tail must be -1 or greater")}
	}
	return options, nil
}

func composeExecArgs(cmd *cobra.Command, args []string) error {
	if cmd.Flags().Changed("run-id") || cmd.Flags().Changed("agent") {
		return nil
	}
	return cobra.MinimumNArgs(1)(cmd, args)
}

func runComposePSCommand(cmd *cobra.Command, cli cliOptions, options composePSOptions) error {
	composePath, normalized, projectID, err := resolveComposeProject(cli)
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
		return commandExitErrorForComposeProject(fmt.Errorf("get project %s: %w", normalized.Name, err), "ps", normalized.Name, composePath)
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
	if options.Interactive {
		return commandExitError{Code: exitCodeUnsupported, Err: fmt.Errorf("exec -i/--interactive is not supported")}
	}
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
			if err := writeTranscriptOrChunk(cmd.OutOrStdout(), cmd.ErrOrStderr(), event.GetTranscript(), event.GetChunk(), event.GetStream()); err != nil {
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
		return commandExitError{Code: execResultExitCode(result), Err: fmt.Errorf("exec %s in sandbox %s failed: %s", result.GetExecId(), result.GetSessionId(), firstNonEmptyString(result.GetError(), result.GetStderr(), result.GetOutput(), "command failed"))}
	}
	return nil
}

func normalizeComposeExecRequest(cmd *cobra.Command, projectName, projectID string, options composeExecOptions, args []string) (*agentcomposev2.ExecRequest, error) {
	legacyTargetFlags := []string{}
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

func runComposeCacheListCommand(cmd *cobra.Command, cli cliOptions, options composeCacheFilterOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	filter, err := cacheFilterFromOptions(options)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	resp, err := clients.cache.ListCaches(cmd.Context(), connect.NewRequest(&agentcomposev2.ListCachesRequest{
		Filter: filter,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("list caches: %w", err))
	}
	output := composeCacheListOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeCacheListText(cmd.OutOrStdout(), output)
}

func runComposeCacheInspectCommand(cmd *cobra.Command, cli cliOptions, cacheID string) error {
	cacheID = strings.TrimSpace(cacheID)
	if cacheID == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("cache inspect requires a cache id")}
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.cache.InspectCache(cmd.Context(), connect.NewRequest(&agentcomposev2.InspectCacheRequest{
		CacheId: cacheID,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("inspect cache %s: %w", cacheID, err))
	}
	output := composeCacheInspectOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeCacheInspectText(cmd.OutOrStdout(), output)
}

func runComposeCachePruneCommand(cmd *cobra.Command, cli cliOptions, options composeCachePruneOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	filter, err := cacheFilterFromPruneOptions(options)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	resp, err := clients.cache.PruneCaches(cmd.Context(), connect.NewRequest(&agentcomposev2.PruneCachesRequest{
		Filter:            filter,
		IncludeReferenced: options.IncludeReferenced,
		Force:             options.Force,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("prune caches: %w", err))
	}
	output := composeCacheOperationOutputFromPruneResponse(resp.Msg)
	if err := writeCacheOperationOutput(cmd.OutOrStdout(), cli.JSON, output); err != nil {
		return err
	}
	return nil
}

func runComposeCacheRemoveCommand(cmd *cobra.Command, cli cliOptions, options composeCacheRemoveOptions, cacheID string) error {
	cacheID = strings.TrimSpace(cacheID)
	if cacheID == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("cache rm requires a cache id")}
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.cache.RemoveCache(cmd.Context(), connect.NewRequest(&agentcomposev2.RemoveCacheRequest{
		CacheId: cacheID,
		Force:   options.Force,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("remove cache %s: %w", cacheID, err))
	}
	output := composeCacheOperationOutputFromRemoveResponse(resp.Msg)
	if err := writeCacheOperationOutput(cmd.OutOrStdout(), cli.JSON, output); err != nil {
		return err
	}
	if options.Force && len(output.Removed) == 0 && len(output.Skipped) > 0 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("remove cache %s: %s", cacheID, cacheRemoveFailureReason(cacheID, output))}
	}
	return nil
}

func cacheRemoveFailureReason(cacheID string, output composeCacheOperationOutput) string {
	for _, skipped := range output.Skipped {
		if skipped.CacheID != cacheID {
			continue
		}
		if cacheStringListContains(skipped.BlockedReasons, "remove failed") {
			if warning := firstCacheRemoveWarning(cacheID, output.Warnings); warning != "" {
				return warning
			}
			return "remove failed"
		}
		if len(skipped.BlockedReasons) > 0 {
			return strings.Join(skipped.BlockedReasons, "; ")
		}
		if len(skipped.Warnings) > 0 {
			return strings.Join(skipped.Warnings, "; ")
		}
	}
	if warning := firstCacheRemoveWarning(cacheID, output.Warnings); warning != "" {
		return warning
	}
	if len(output.Warnings) > 0 {
		return output.Warnings[0]
	}
	return "cache is protected"
}

func firstCacheRemoveWarning(cacheID string, warnings []string) string {
	for _, warning := range warnings {
		if strings.Contains(warning, cacheID) {
			return warning
		}
	}
	return ""
}

func cacheStringListContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
			if err := writeImagePullText(cmd.OutOrStdout(), item); err != nil {
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
	return writeImagePullText(cmd.OutOrStdout(), output)
}

func runComposeBuildCommand(cmd *cobra.Command, cli cliOptions, options composeImageBuildOptions, args []string) error {
	return runComposeProjectBuildCommand(cmd, cli, options, args)
}

func runComposeProjectBuildCommand(cmd *cobra.Command, cli cliOptions, options composeImageBuildOptions, agentNames []string) error {
	sourcePath, normalized, err := loadNormalizedCompose(cli)
	if err != nil {
		return err
	}
	plans, err := projectImageBuildPlans(sourcePath, normalized, options, agentNames)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	if len(plans) == 0 {
		if cli.JSON {
			data, err := json.MarshalIndent(composeProjectImageBuildOutput{Images: []composeImageBuildOutput{}}, "", "  ")
			if err != nil {
				return err
			}
			return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "No project images configured for build")
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	output := composeProjectImageBuildOutput{Images: make([]composeImageBuildOutput, 0, len(plans))}
	for _, plan := range plans {
		item, err := buildImage(cmd.Context(), cmd.OutOrStdout(), cli.JSON, clients.image, plan)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("build image %s: %w", firstNonEmptyString(plan.GetTags()...), err))
		}
		output.Images = append(output.Images, item)
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

func buildImage(ctx context.Context, out io.Writer, jsonOutput bool, client agentcomposev2connect.ImageServiceClient, req *agentcomposev2.BuildImageRequest) (composeImageBuildOutput, error) {
	stream, err := client.BuildImage(ctx, connect.NewRequest(req))
	if err != nil {
		return composeImageBuildOutput{}, err
	}
	output := composeImageBuildOutput{
		ImageRef: firstNonEmptyString(req.GetTags()...),
		Status:   imageOperationStatusText(agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_RUNNING),
	}
	for stream.Receive() {
		event := stream.Msg()
		if !jsonOutput && strings.TrimSpace(event.GetMessage()) != "" {
			if _, err := fmt.Fprintln(out, strings.TrimSpace(event.GetMessage())); err != nil {
				return output, err
			}
		}
		if event.GetImage() != nil {
			output.Image = composeImageOutputFromProto(event.GetImage())
		}
		if strings.TrimSpace(event.GetImageRef()) != "" {
			output.ImageRef = event.GetImageRef()
		}
		if strings.TrimSpace(event.GetResolvedRef()) != "" {
			output.ResolvedRef = event.GetResolvedRef()
		}
		if event.GetStatus() != agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_UNSPECIFIED {
			output.Status = imageOperationStatusText(event.GetStatus())
		}
		output.Warnings = appendUniqueStrings(output.Warnings, event.GetWarnings()...)
	}
	if err := stream.Err(); err != nil {
		return output, err
	}
	return output, nil
}

func parseBuildArgs(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, argValue, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --build-arg %q: expected KEY=VALUE", value)
		}
		result[key] = argValue
	}
	return result, nil
}

func projectImageBuildPlans(sourcePath string, project *compose.NormalizedProjectSpec, options composeImageBuildOptions, agentNames []string) ([]*agentcomposev2.BuildImageRequest, error) {
	selected := map[string]struct{}{}
	for _, name := range agentNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		selected[name] = struct{}{}
	}
	composeDir := "."
	if strings.TrimSpace(sourcePath) != "" {
		composeDir = filepath.Dir(sourcePath)
	}
	var plans []*agentcomposev2.BuildImageRequest
	for _, agent := range project.Agents {
		if len(selected) > 0 {
			if _, ok := selected[agent.Name]; !ok {
				continue
			}
			delete(selected, agent.Name)
		}
		if agent.Build == nil {
			continue
		}
		req, err := buildImageRequestFromAgent(composeDir, agent, options)
		if err != nil {
			return nil, err
		}
		plans = append(plans, req)
	}
	if len(selected) > 0 {
		missing := make([]string, 0, len(selected))
		for name := range selected {
			missing = append(missing, name)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("unknown build agent(s): %s", strings.Join(missing, ", "))
	}
	return plans, nil
}

func buildImageRequestFromAgent(composeDir string, agent compose.NormalizedAgentSpec, options composeImageBuildOptions) (*agentcomposev2.BuildImageRequest, error) {
	build := agent.Build
	tags := append([]string{}, agent.Image)
	tags = append(tags, build.Tags...)
	tags = append(tags, options.Tags...)
	contextDir := resolveComposeBuildPath(composeDir, build.Context)
	dockerfile := build.Dockerfile
	if strings.TrimSpace(options.Dockerfile) != "" {
		dockerfile = options.Dockerfile
	}
	buildArgs := cloneStringMapForCLI(build.Args)
	cliArgs, err := parseBuildArgs(options.BuildArgs)
	if err != nil {
		return nil, err
	}
	for key, value := range cliArgs {
		if buildArgs == nil {
			buildArgs = map[string]string{}
		}
		buildArgs[key] = value
	}
	platformValue := ""
	if len(build.Platforms) == 1 {
		platformValue = build.Platforms[0]
	}
	if strings.TrimSpace(options.Platform) != "" {
		platformValue = options.Platform
	}
	platform, err := parseImagePlatform(platformValue)
	if err != nil {
		return nil, err
	}
	tags = normalizeCLIStringList(tags)
	if len(tags) == 0 {
		return nil, fmt.Errorf("agent %s build requires image or build.tags", agent.Name)
	}
	return &agentcomposev2.BuildImageRequest{
		ContextDir: contextDir,
		Dockerfile: strings.TrimSpace(dockerfile),
		Tags:       tags,
		BuildArgs:  buildArgs,
		Target:     firstNonEmptyString(options.Target, build.Target),
		Store:      agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
		Platform:   platform,
		NoCache:    options.NoCache || build.NoCache,
		Pull:       options.Pull || build.Pull,
	}, nil
}

func resolveComposeBuildPath(composeDir string, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "."
	}
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(composeDir, value)
}

func normalizeCLIStringList(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
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

func writeImagePullText(out io.Writer, output composeImagePullOutput) error {
	status := "Pulled"
	if imagePullSkipped(output) {
		status = "Skipped"
	}
	if _, err := fmt.Fprintf(out, "%s %s\nResolved: %s\n", status, output.ImageRef, firstNonEmptyString(output.ResolvedRef, "-")); err != nil {
		return err
	}
	for _, warning := range output.Warnings {
		if _, err := fmt.Fprintf(out, "Warning: %s\n", warning); err != nil {
			return err
		}
	}
	return nil
}

func imagePullSkipped(output composeImagePullOutput) bool {
	for _, warning := range output.Warnings {
		normalized := strings.ToLower(strings.TrimSpace(warning))
		if strings.Contains(normalized, "skipped") || strings.Contains(normalized, "already exists") {
			return true
		}
	}
	return false
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
		return commandExitErrorForImageTarget("remove image", strings.TrimSpace(imageRef), err)
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

func commandExitErrorForImageTarget(operation, imageRef string, err error) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return commandExitError{
			Code: exitCodeUsage,
			Err:  fmt.Errorf("image %s does not exist", imageRef),
		}
	}
	return commandExitErrorForConnect(fmt.Errorf("%s %s: %w", operation, imageRef, err))
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
		return commandExitErrorForImageTarget("inspect image", strings.TrimSpace(imageRef), err)
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
	if kind == "cache" {
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect cache requires a cache id")}
		}
		return runComposeCacheInspectCommand(cmd, cli, target)
	}
	composePath, normalized, projectID, err := resolveComposeProject(cli)
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
			return commandExitErrorForComposeProject(fmt.Errorf("inspect project %s: %w", normalized.Name, err), "inspect project", normalized.Name, composePath)
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
			return commandExitErrorForComposeProject(fmt.Errorf("inspect agent %s in project %s: %w", target, normalized.Name, err), "inspect agent", normalized.Name, composePath)
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
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect session requires a sandbox")}
		}
		output, err = composeSandboxInspectOutputFor(cmd.Context(), clients, target)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect sandbox %s: %w", target, err))
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
	ID              string `json:"id"`
	Name            string `json:"name"`
	ConfigFile      string `json:"config_file"`
	ProjectDir      string `json:"project_dir,omitempty"`
	Revision        uint64 `json:"revision"`
	SpecHash        string `json:"spec_hash,omitempty"`
	AgentCount      uint32 `json:"agent_count"`
	SchedulerCount  uint32 `json:"scheduler_count"`
	TriggerCount    uint32 `json:"trigger_count"`
	RunningRunCount uint32 `json:"running_run_count"`
	LatestRunID     string `json:"latest_run_id,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	RemovedAt       string `json:"removed_at,omitempty"`
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
	RunID        string   `json:"run_id"`
	ProjectID    string   `json:"project_id"`
	ProjectName  string   `json:"project_name"`
	AgentName    string   `json:"agent_name"`
	Source       string   `json:"source"`
	Status       string   `json:"status"`
	SessionID    string   `json:"session_id"`
	ExitCode     int32    `json:"exit_code"`
	Error        string   `json:"error,omitempty"`
	StartedAt    string   `json:"started_at,omitempty"`
	CompletedAt  string   `json:"completed_at,omitempty"`
	DurationMs   int64    `json:"duration_ms,omitempty"`
	Prompt       string   `json:"prompt,omitempty"`
	Output       string   `json:"output,omitempty"`
	ResultJSON   string   `json:"result_json,omitempty"`
	LogsPath     string   `json:"logs_path,omitempty"`
	ArtifactsDir string   `json:"artifacts_dir,omitempty"`
	CleanupError string   `json:"cleanup_error,omitempty"`
	Driver       string   `json:"driver,omitempty"`
	ImageRef     string   `json:"image_ref,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
	LogsCommand  string   `json:"logs_command,omitempty"`
}

type composeLogsOutput struct {
	Runs []composeRunOutput `json:"runs"`
}

type cliServiceClients struct {
	project agentcomposev2connect.ProjectServiceClient
	run     agentcomposev2connect.RunServiceClient
	exec    agentcomposev2connect.ExecServiceClient
	image   agentcomposev2connect.ImageServiceClient
	cache   agentcomposev2connect.CacheServiceClient
	sandbox agentcomposev2connect.SandboxServiceClient
	session agentcomposev1connect.SessionServiceClient
	loader  agentcomposev1connect.LoaderServiceClient
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

type composeSandboxPruneOutput struct {
	DryRun   bool                         `json:"dry_run"`
	Matched  []composePSSandboxOutput     `json:"matched"`
	Removed  []string                     `json:"removed"`
	Skipped  []composeSandboxPruneSkipped `json:"skipped"`
	Warnings []string                     `json:"warnings,omitempty"`
}

type composeSandboxPruneSkipped struct {
	Sandbox   string `json:"sandbox"`
	Agent     string `json:"agent,omitempty"`
	Status    string `json:"status,omitempty"`
	Driver    string `json:"driver,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Reason    string `json:"reason"`
}

type composeStatsOutput struct {
	Sandbox          string              `json:"sandbox"`
	Driver           string              `json:"driver"`
	SampledAt        string              `json:"sampled_at"`
	CPUPercent       composeMetricOutput `json:"cpu_percent"`
	MemoryUsageBytes composeMetricOutput `json:"memory_usage_bytes"`
	MemoryLimitBytes composeMetricOutput `json:"memory_limit_bytes"`
	MemoryPercent    composeMetricOutput `json:"memory_percent"`
	NetworkRxBytes   composeMetricOutput `json:"network_rx_bytes"`
	NetworkTxBytes   composeMetricOutput `json:"network_tx_bytes"`
	BlockReadBytes   composeMetricOutput `json:"block_read_bytes"`
	BlockWriteBytes  composeMetricOutput `json:"block_write_bytes"`
	UptimeSeconds    composeMetricOutput `json:"uptime_seconds"`
}

type composeProjectStatsOutput struct {
	Project composeUpProjectOutput `json:"project"`
	Stats   []composeStatsOutput   `json:"stats"`
}

type composeMetricOutput struct {
	Value   *float64 `json:"value"`
	Unit    string   `json:"unit"`
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
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

type composeSchedulerListOutput struct {
	Project  composeUpProjectOutput        `json:"project"`
	Triggers []composeSchedulerTriggerItem `json:"triggers"`
}

type composeSchedulerInspectOutput struct {
	Project    composeUpProjectOutput      `json:"project"`
	Source     string                      `json:"source"`
	AgentName  string                      `json:"agent_name"`
	Trigger    composeSchedulerTriggerItem `json:"trigger"`
	Definition map[string]any              `json:"definition,omitempty"`
	Registered map[string]any              `json:"registered,omitempty"`
}

type composeSchedulerTriggerItem struct {
	AgentName        string `json:"agent_name"`
	Name             string `json:"name,omitempty"`
	TriggerID        string `json:"trigger_id"`
	Kind             string `json:"kind"`
	Source           string `json:"source"`
	SchedulerID      string `json:"scheduler_id,omitempty"`
	ManagedLoaderID  string `json:"managed_loader_id,omitempty"`
	SchedulerEnabled bool   `json:"scheduler_enabled"`
	TriggerEnabled   bool   `json:"trigger_enabled"`
	Topic            string `json:"topic,omitempty"`
	IntervalMs       int64  `json:"interval_ms,omitempty"`
	SpecJSON         string `json:"spec_json,omitempty"`
	NextFireAt       string `json:"next_fire_at,omitempty"`
	LastFiredAt      string `json:"last_fired_at,omitempty"`
	declarative      *agentcomposev2.TriggerSpec
	registered       *agentcomposev1.LoaderTrigger
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

type composeCacheListOutput struct {
	Caches   []composeCacheOutput `json:"caches"`
	Warnings []string             `json:"warnings,omitempty"`
}

type composeCacheInspectOutput struct {
	Cache    composeCacheOutput `json:"cache"`
	Warnings []string           `json:"warnings,omitempty"`
}

type composeCacheOperationOutput struct {
	DryRun   bool                 `json:"dry_run"`
	Matched  []composeCacheOutput `json:"matched"`
	Removed  []string             `json:"removed"`
	Skipped  []composeCacheOutput `json:"skipped"`
	Warnings []string             `json:"warnings,omitempty"`
}

type composeCacheOutput struct {
	CacheID        string                        `json:"cache_id"`
	Domain         string                        `json:"domain"`
	Type           string                        `json:"type"`
	Driver         string                        `json:"driver"`
	Kind           string                        `json:"kind"`
	Path           string                        `json:"path,omitempty"`
	SizeBytes      uint64                        `json:"size_bytes"`
	ImageID        string                        `json:"image_id,omitempty"`
	ImageRef       string                        `json:"image_ref,omitempty"`
	ResolvedRef    string                        `json:"resolved_ref,omitempty"`
	SessionID      string                        `json:"session_id,omitempty"`
	SandboxID      string                        `json:"sandbox_id,omitempty"`
	Status         string                        `json:"status"`
	Removable      bool                          `json:"removable"`
	BlockedReasons []string                      `json:"blocked_reasons,omitempty"`
	LastUsedAt     string                        `json:"last_used_at,omitempty"`
	LastUsedSource string                        `json:"last_used_source,omitempty"`
	References     []composeCacheReferenceOutput `json:"references,omitempty"`
	Warnings       []string                      `json:"warnings,omitempty"`
}

type composeCacheReferenceOutput struct {
	Type        string `json:"type,omitempty"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Path        string `json:"path,omitempty"`
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
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

type composeImageBuildOutput struct {
	ImageRef    string             `json:"image_ref"`
	ResolvedRef string             `json:"resolved_ref,omitempty"`
	Status      string             `json:"status"`
	Image       composeImageOutput `json:"image"`
	Warnings    []string           `json:"warnings,omitempty"`
}

type composeProjectImageBuildOutput struct {
	Images []composeImageBuildOutput `json:"images"`
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
		TriggerCount:    summary.GetTriggerCount(),
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
		if _, err := fmt.Fprintln(tw, "PROJECT\tCONFIG FILE\tREVISION\tAGENTS\tSCHEDULERS\tTRIGGERS\tPROJECT ID\tPROJECT DIR\tSPEC HASH\tUPDATED\tSTATUS"); err != nil {
			return err
		}
		for _, project := range projects {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\n",
				project.Name,
				firstNonEmptyString(project.ConfigFile, "-"),
				project.Revision,
				project.AgentCount,
				project.SchedulerCount,
				project.TriggerCount,
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
	if _, err := fmt.Fprintln(tw, "PROJECT\tCONFIG FILE\tREVISION\tAGENTS\tSCHEDULERS\tTRIGGERS"); err != nil {
		return err
	}
	for _, project := range projects {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\n",
			project.Name,
			firstNonEmptyString(project.ConfigFile, "-"),
			project.Revision,
			project.AgentCount,
			project.SchedulerCount,
			project.TriggerCount,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func projectListStatus(project composeProjectListItem) string {
	if project.RemovedAt != "" {
		return "removed"
	}
	return "active"
}

func writeComposeDownText(out io.Writer, output composeDownOutput) error {
	if _, err := fmt.Fprintf(out, "Project: %s\nID: %s\nStatus: %s\nFailed sandbox stops: %d\n\n",
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
		cache:   agentcomposev2connect.NewCacheServiceClient(httpClient, clientConfig.BaseURL),
		sandbox: agentcomposev2connect.NewSandboxServiceClient(httpClient, clientConfig.BaseURL),
		session: agentcomposev1connect.NewSessionServiceClient(httpClient, clientConfig.BaseURL),
		loader:  agentcomposev1connect.NewLoaderServiceClient(httpClient, clientConfig.BaseURL),
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

func composeStatsOutputFromProto(stats *agentcomposev2.SandboxStats) composeStatsOutput {
	if stats == nil {
		return composeStatsOutput{}
	}
	return composeStatsOutput{
		Sandbox:          stats.GetSandboxId(),
		Driver:           stats.GetDriver(),
		SampledAt:        stats.GetSampledAt(),
		CPUPercent:       composeMetricOutputFromProto(stats.GetCpuPercent()),
		MemoryUsageBytes: composeMetricOutputFromProto(stats.GetMemoryUsageBytes()),
		MemoryLimitBytes: composeMetricOutputFromProto(stats.GetMemoryLimitBytes()),
		MemoryPercent:    composeMetricOutputFromProto(stats.GetMemoryPercent()),
		NetworkRxBytes:   composeMetricOutputFromProto(stats.GetNetworkRxBytes()),
		NetworkTxBytes:   composeMetricOutputFromProto(stats.GetNetworkTxBytes()),
		BlockReadBytes:   composeMetricOutputFromProto(stats.GetBlockReadBytes()),
		BlockWriteBytes:  composeMetricOutputFromProto(stats.GetBlockWriteBytes()),
		UptimeSeconds:    composeMetricOutputFromProto(stats.GetUptimeSeconds()),
	}
}

func composeStatsOutputForSandbox(ctx context.Context, client agentcomposev2connect.SandboxServiceClient, sandboxID string) (composeStatsOutput, error) {
	resp, err := client.GetSandboxStats(ctx, connect.NewRequest(&agentcomposev2.GetSandboxStatsRequest{SandboxId: sandboxID}))
	if err != nil {
		return composeStatsOutput{}, err
	}
	return composeStatsOutputFromProto(resp.Msg.GetStats()), nil
}

func composeProjectStatsOutputFromProject(ctx context.Context, clients cliServiceClients, project *agentcomposev2.Project) (composeProjectStatsOutput, error) {
	output := composeProjectStatsOutput{
		Project: composeProjectSummaryOutput(project.GetSummary()),
	}
	psOutput, err := composePSOutputFromProject(ctx, clients, project, composePSOptions{})
	if err != nil {
		return composeProjectStatsOutput{}, err
	}
	output.Stats = make([]composeStatsOutput, 0, len(psOutput.Sandboxes))
	for _, sandbox := range psOutput.Sandboxes {
		stats, err := composeStatsOutputForSandbox(ctx, clients.sandbox, sandbox.Sandbox)
		if err != nil {
			return composeProjectStatsOutput{}, fmt.Errorf("get sandbox %s stats: %w", sandbox.Sandbox, err)
		}
		output.Stats = append(output.Stats, stats)
	}
	return output, nil
}

func composeMetricOutputFromProto(metric *agentcomposev2.MetricValue) composeMetricOutput {
	if metric == nil {
		return composeMetricOutput{Status: "unknown"}
	}
	return composeMetricOutput{
		Value:   metric.Value,
		Unit:    metric.GetUnit(),
		Status:  metricStatusText(metric.GetStatus()),
		Message: metric.GetMessage(),
	}
}

func metricStatusText(status agentcomposev2.MetricStatus) string {
	switch status {
	case agentcomposev2.MetricStatus_METRIC_STATUS_OK:
		return "ok"
	case agentcomposev2.MetricStatus_METRIC_STATUS_UNAVAILABLE:
		return "unavailable"
	case agentcomposev2.MetricStatus_METRIC_STATUS_UNKNOWN, agentcomposev2.MetricStatus_METRIC_STATUS_UNSPECIFIED:
		fallthrough
	default:
		return "unknown"
	}
}

func writeStatsText(out io.Writer, stats []composeStatsOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SANDBOX\tDRIVER\tCPU%\tMEM\tMEM_LIMIT\tMEM%\tNET_RX\tNET_TX\tBLOCK_READ\tBLOCK_WRITE\tUPTIME"); err != nil {
		return err
	}
	for _, output := range stats {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			output.Sandbox,
			firstNonEmptyString(output.Driver, "-"),
			formatMetricForText(output.CPUPercent),
			formatMetricForText(output.MemoryUsageBytes),
			formatMetricForText(output.MemoryLimitBytes),
			formatMetricForText(output.MemoryPercent),
			formatMetricForText(output.NetworkRxBytes),
			formatMetricForText(output.NetworkTxBytes),
			formatMetricForText(output.BlockReadBytes),
			formatMetricForText(output.BlockWriteBytes),
			formatMetricForText(output.UptimeSeconds),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func formatMetricForText(metric composeMetricOutput) string {
	if metric.Status != "ok" || metric.Value == nil {
		return "-"
	}
	switch metric.Unit {
	case "percent":
		return fmt.Sprintf("%.2f", *metric.Value)
	case "seconds":
		return fmt.Sprintf("%.0fs", *metric.Value)
	default:
		return fmt.Sprintf("%.0f", *metric.Value)
	}
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
		return composeAgentInspectOutput{}, commandExitErrorForConnect(fmt.Errorf("list running sandbox for agent %s: %w", agentName, err))
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

func composeRunOutputFromSummary(run *agentcomposev2.RunSummary, projectName, logsCommand string) composeRunOutput {
	return composeRunOutput{
		RunID:       run.GetRunId(),
		ProjectID:   run.GetProjectId(),
		ProjectName: firstNonEmptyString(run.GetProjectName(), projectName),
		AgentName:   run.GetAgentName(),
		Source:      runSourceText(run.GetSource()),
		Status:      runStatusText(run.GetStatus()),
		SessionID:   run.GetSessionId(),
		ExitCode:    run.GetExitCode(),
		Error:       run.GetError(),
		StartedAt:   run.GetStartedAt(),
		CompletedAt: run.GetCompletedAt(),
		DurationMs:  run.GetDurationMs(),
		Warnings:    appendUniqueStrings(nil, run.GetWarnings()...),
		LogsCommand: logsCommand,
	}
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
		Warnings:     appendUniqueStrings(append([]string(nil), summary.GetWarnings()...), run.GetWarnings()...),
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

func composeCacheListOutputFromResponse(resp *agentcomposev2.ListCachesResponse) composeCacheListOutput {
	output := composeCacheListOutput{
		Caches:   make([]composeCacheOutput, 0, len(resp.GetCaches())),
		Warnings: append([]string(nil), resp.GetWarnings()...),
	}
	for _, cache := range resp.GetCaches() {
		output.Caches = append(output.Caches, composeCacheOutputFromProto(cache))
	}
	return output
}

func composeCacheInspectOutputFromResponse(resp *agentcomposev2.InspectCacheResponse) composeCacheInspectOutput {
	return composeCacheInspectOutput{
		Cache:    composeCacheOutputFromProto(resp.GetCache()),
		Warnings: append([]string(nil), resp.GetWarnings()...),
	}
}

func composeCacheOperationOutputFromPruneResponse(resp *agentcomposev2.PruneCachesResponse) composeCacheOperationOutput {
	output := composeCacheOperationOutput{
		DryRun:   resp.GetDryRun(),
		Matched:  make([]composeCacheOutput, 0, len(resp.GetMatched())),
		Removed:  append([]string(nil), resp.GetRemoved()...),
		Skipped:  make([]composeCacheOutput, 0, len(resp.GetSkipped())),
		Warnings: append([]string(nil), resp.GetWarnings()...),
	}
	for _, cache := range resp.GetMatched() {
		output.Matched = append(output.Matched, composeCacheOutputFromProto(cache))
	}
	for _, cache := range resp.GetSkipped() {
		output.Skipped = append(output.Skipped, composeCacheOutputFromProto(cache))
	}
	return output
}

func composeCacheOperationOutputFromRemoveResponse(resp *agentcomposev2.RemoveCacheResponse) composeCacheOperationOutput {
	output := composeCacheOperationOutput{
		DryRun:   resp.GetDryRun(),
		Matched:  make([]composeCacheOutput, 0, len(resp.GetMatched())),
		Removed:  append([]string(nil), resp.GetRemoved()...),
		Skipped:  make([]composeCacheOutput, 0, len(resp.GetSkipped())),
		Warnings: append([]string(nil), resp.GetWarnings()...),
	}
	for _, cache := range resp.GetMatched() {
		output.Matched = append(output.Matched, composeCacheOutputFromProto(cache))
	}
	for _, cache := range resp.GetSkipped() {
		output.Skipped = append(output.Skipped, composeCacheOutputFromProto(cache))
	}
	return output
}

func composeImageRemoveOutputFromResponse(resp *agentcomposev2.RemoveImageResponse) composeImageRemoveOutput {
	return composeImageRemoveOutput{
		ImageRef:     resp.GetImageRef(),
		UntaggedRefs: append([]string(nil), resp.GetUntaggedRefs()...),
		DeletedIDs:   append([]string(nil), resp.GetDeletedIds()...),
		Warnings:     append([]string(nil), resp.GetWarnings()...),
	}
}

func composeCacheOutputFromProto(cache *agentcomposev2.CacheItem) composeCacheOutput {
	if cache == nil {
		return composeCacheOutput{}
	}
	refs := make([]composeCacheReferenceOutput, 0, len(cache.GetReferences()))
	for _, ref := range cache.GetReferences() {
		refs = append(refs, composeCacheReferenceOutput{
			Type:        ref.GetType(),
			ID:          ref.GetId(),
			Name:        ref.GetName(),
			Path:        ref.GetPath(),
			Status:      ref.GetStatus(),
			Description: ref.GetDescription(),
		})
	}
	return composeCacheOutput{
		CacheID:        cache.GetCacheId(),
		Domain:         cacheDomainText(cache.GetDomain()),
		Type:           cacheTypeText(cache.GetDomain()),
		Driver:         cache.GetDriver(),
		Kind:           cache.GetKind(),
		Path:           cache.GetPath(),
		SizeBytes:      cache.GetSizeBytes(),
		ImageID:        cache.GetImageId(),
		ImageRef:       cache.GetImageRef(),
		ResolvedRef:    cache.GetResolvedRef(),
		SessionID:      cache.GetSessionId(),
		SandboxID:      cache.GetSandboxId(),
		Status:         cacheStatusText(cache.GetStatus()),
		Removable:      cache.GetRemovable(),
		BlockedReasons: append([]string(nil), cache.GetBlockedReasons()...),
		LastUsedAt:     cache.GetLastUsedAt(),
		LastUsedSource: cache.GetLastUsedSource(),
		References:     refs,
		Warnings:       append([]string(nil), cache.GetWarnings()...),
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

func writeCacheListText(out io.Writer, output composeCacheListOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "CACHE ID\tDRIVER\tTYPE\tSTATUS\tREMOVABLE\tSIZE\tREF/SESSION\tPATH"); err != nil {
		return err
	}
	for _, cache := range output.Caches {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			cache.CacheID,
			firstNonEmptyString(cache.Driver, "-"),
			firstNonEmptyString(cache.Type, "-"),
			firstNonEmptyString(cache.Status, "-"),
			strconv.FormatBool(cache.Removable),
			cache.SizeBytes,
			cacheRefSessionText(cache),
			firstNonEmptyString(cache.Path, "-"),
		); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return writeStringListSection(out, "Warnings", output.Warnings)
}

func writeCacheInspectText(out io.Writer, output composeCacheInspectOutput) error {
	cache := output.Cache
	if _, err := fmt.Fprintf(out, "Cache ID: %s\nDomain: %s\nType: %s\nDriver: %s\nKind: %s\nStatus: %s\nRemovable: %t\nSize: %d\nPath: %s\n",
		firstNonEmptyString(cache.CacheID, "-"),
		firstNonEmptyString(cache.Domain, "-"),
		firstNonEmptyString(cache.Type, "-"),
		firstNonEmptyString(cache.Driver, "-"),
		firstNonEmptyString(cache.Kind, "-"),
		firstNonEmptyString(cache.Status, "-"),
		cache.Removable,
		cache.SizeBytes,
		firstNonEmptyString(cache.Path, "-"),
	); err != nil {
		return err
	}
	if cache.ImageID != "" || cache.ImageRef != "" || cache.ResolvedRef != "" {
		if _, err := fmt.Fprintf(out, "Image: %s\nResolved: %s\nImage ID: %s\n",
			firstNonEmptyString(cache.ImageRef, "-"),
			firstNonEmptyString(cache.ResolvedRef, "-"),
			firstNonEmptyString(cache.ImageID, "-"),
		); err != nil {
			return err
		}
	}
	if cache.SessionID != "" || cache.SandboxID != "" {
		if _, err := fmt.Fprintf(out, "Sandbox: %s\n",
			firstNonEmptyString(cache.SandboxID, cache.SessionID, "-"),
		); err != nil {
			return err
		}
	}
	if cache.LastUsedAt != "" || cache.LastUsedSource != "" {
		if _, err := fmt.Fprintf(out, "Last used: %s (%s)\n",
			firstNonEmptyString(cache.LastUsedAt, "-"),
			firstNonEmptyString(cache.LastUsedSource, "-"),
		); err != nil {
			return err
		}
	}
	if err := writeStringListSection(out, "Blocked reasons", cache.BlockedReasons); err != nil {
		return err
	}
	if err := writeCacheReferencesSection(out, cache.References); err != nil {
		return err
	}
	if err := writeStringListSection(out, "Warnings", append(append([]string(nil), output.Warnings...), cache.Warnings...)); err != nil {
		return err
	}
	return nil
}

func writeCacheOperationOutput(out io.Writer, asJSON bool, output composeCacheOperationOutput) error {
	if asJSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(out, append(data, '\n'))
	}
	if output.DryRun {
		if _, err := fmt.Fprintf(out, "Dry-run: %d matched, %d skipped, %d would be removed.\n", len(output.Matched), len(output.Skipped), len(output.Matched)-len(output.Skipped)); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(out, "Removed %d cache item(s); %d matched, %d skipped.\n", len(output.Removed), len(output.Matched), len(output.Skipped)); err != nil {
			return err
		}
	}
	if len(output.Removed) > 0 {
		if err := writeStringListSection(out, "Removed", output.Removed); err != nil {
			return err
		}
	}
	if len(output.Matched) > 0 {
		if _, err := fmt.Fprintln(out, "Matched:"); err != nil {
			return err
		}
		if err := writeCacheOperationTable(out, output.Matched); err != nil {
			return err
		}
	}
	if len(output.Skipped) > 0 {
		if _, err := fmt.Fprintln(out, "Skipped:"); err != nil {
			return err
		}
		if err := writeCacheOperationTable(out, output.Skipped); err != nil {
			return err
		}
	}
	return writeStringListSection(out, "Warnings", output.Warnings)
}

func writeSandboxPruneOutput(out io.Writer, asJSON bool, output composeSandboxPruneOutput) error {
	if asJSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(out, append(data, '\n'))
	}
	if output.DryRun {
		if _, err := fmt.Fprintf(out, "Dry-run: %d matched, %d skipped, %d would be removed.\n", len(output.Matched), len(output.Skipped), len(output.Matched)); err != nil {
			return err
		}
		if len(output.Matched) > 0 {
			if _, err := fmt.Fprintln(out, "Use --force to remove matched sandboxes."); err != nil {
				return err
			}
		}
	} else {
		if _, err := fmt.Fprintf(out, "Removed %d sandbox(es); %d matched, %d skipped.\n", len(output.Removed), len(output.Matched), len(output.Skipped)); err != nil {
			return err
		}
	}
	if len(output.Removed) > 0 {
		if err := writeStringListSection(out, "Removed", output.Removed); err != nil {
			return err
		}
	}
	if len(output.Matched) > 0 {
		if _, err := fmt.Fprintln(out, "Matched:"); err != nil {
			return err
		}
		reason := "matched"
		if output.DryRun {
			reason = "would remove"
		}
		if err := writeSandboxPruneMatchedTable(out, output.Matched, reason); err != nil {
			return err
		}
	}
	if len(output.Skipped) > 0 {
		if _, err := fmt.Fprintln(out, "Skipped:"); err != nil {
			return err
		}
		if err := writeSandboxPruneSkippedTable(out, output.Skipped); err != nil {
			return err
		}
	}
	return writeStringListSection(out, "Warnings", output.Warnings)
}

func writeSandboxPruneMatchedTable(out io.Writer, sandboxes []composePSSandboxOutput, reason string) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SANDBOX\tAGENT\tSTATUS\tDRIVER\tUPDATED\tREASON"); err != nil {
		return err
	}
	for _, sandbox := range sandboxes {
		updated := firstNonEmptyString(sandbox.UpdatedAt, sandbox.CreatedAt)
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			firstNonEmptyString(sandbox.Sandbox, "-"),
			firstNonEmptyString(sandbox.Agent, "-"),
			firstNonEmptyString(sandbox.Status, "-"),
			firstNonEmptyString(sandbox.Driver, "-"),
			firstNonEmptyString(updated, "-"),
			reason,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeSandboxPruneSkippedTable(out io.Writer, skipped []composeSandboxPruneSkipped) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SANDBOX\tAGENT\tSTATUS\tDRIVER\tUPDATED\tREASON"); err != nil {
		return err
	}
	for _, item := range skipped {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			firstNonEmptyString(item.Sandbox, "-"),
			firstNonEmptyString(item.Agent, "-"),
			firstNonEmptyString(item.Status, "-"),
			firstNonEmptyString(item.Driver, "-"),
			firstNonEmptyString(item.UpdatedAt, "-"),
			firstNonEmptyString(item.Reason, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeCacheOperationTable(out io.Writer, caches []composeCacheOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "CACHE ID\tDRIVER\tTYPE\tSTATUS\tREMOVABLE\tREASON"); err != nil {
		return err
	}
	for _, cache := range caches {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			firstNonEmptyString(cache.CacheID, "-"),
			firstNonEmptyString(cache.Driver, "-"),
			firstNonEmptyString(cache.Type, "-"),
			firstNonEmptyString(cache.Status, "-"),
			strconv.FormatBool(cache.Removable),
			firstNonEmptyString(strings.Join(cache.BlockedReasons, "; "), "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeStringListSection(out io.Writer, title string, values []string) error {
	if len(values) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(out, "%s:\n", title); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := fmt.Fprintf(out, "- %s\n", value); err != nil {
			return err
		}
	}
	return nil
}

func writeCacheReferencesSection(out io.Writer, refs []composeCacheReferenceOutput) error {
	if len(refs) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(out, "References:"); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "TYPE\tID\tNAME\tSTATUS\tPATH\tDESCRIPTION"); err != nil {
		return err
	}
	for _, ref := range refs {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			firstNonEmptyString(ref.Type, "-"),
			firstNonEmptyString(ref.ID, "-"),
			firstNonEmptyString(ref.Name, "-"),
			firstNonEmptyString(ref.Status, "-"),
			firstNonEmptyString(ref.Path, "-"),
			firstNonEmptyString(ref.Description, "-"),
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
	if options.Follow && !cli.JSON {
		runs, err := listLogRuns(cmd.Context(), client, projectID, options)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("list logs for project %s: %w", projectName, err))
		}
		for _, summary := range runs {
			if err := followRunLogStream(cmd.Context(), cmd.OutOrStdout(), client, projectID, summary, options); err != nil {
				return err
			}
		}
		return nil
	}
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

func followRunLogStream(ctx context.Context, out io.Writer, client agentcomposev2connect.RunServiceClient, projectID string, summary *agentcomposev2.RunSummary, options composeLogsOptions) error {
	if summary == nil {
		return nil
	}
	tailLines := uint32(0)
	if options.TailLines > 0 {
		tailLines = uint32(options.TailLines)
	}
	stream, err := client.FollowRunLogs(ctx, connect.NewRequest(&agentcomposev2.FollowRunLogsRequest{
		ProjectId: strings.TrimSpace(projectID),
		RunId:     summary.GetRunId(),
		TailLines: tailLines,
		Follow:    true,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("follow run %s logs: %w", summary.GetRunId(), err))
	}
	for stream.Receive() {
		chunk := stream.Msg()
		if chunk.GetData() != "" {
			if err := writePrefixedRunOutputWithTimestamp(out, summary, chunk.GetData(), options.Timestamp, chunk.GetCreatedAt()); err != nil {
				return err
			}
		}
		if chunk.GetIsFinal() {
			return nil
		}
	}
	if err := stream.Err(); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("follow run %s logs: %w", summary.GetRunId(), err))
	}
	return nil
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
	return writePrefixedRunOutputWithTimestamp(out, summary, output, timestamp, runLogTimestamp(summary))
}

func writePrefixedRunOutputWithTimestamp(out io.Writer, summary *agentcomposev2.RunSummary, output string, timestamp bool, timestampValue string) error {
	if output == "" {
		return nil
	}
	prefix := runLogPrefix(summary)
	runTime := formatComposeLogTimestamp(timestampValue)
	for len(output) > 0 {
		line := output
		rest := ""
		if idx := strings.IndexByte(output, '\n'); idx >= 0 {
			line = output[:idx+1]
			rest = output[idx+1:]
		}
		if _, err := fmt.Fprintf(out, "%s | ", prefix); err != nil {
			return err
		}
		if timestamp && runTime != "" {
			if _, err := fmt.Fprintf(out, "time=%s ", runTime); err != nil {
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

func runLogPrefix(summary *agentcomposev2.RunSummary) string {
	runID := strings.TrimSpace(summary.GetRunId())
	agentName := strings.TrimSpace(summary.GetAgentName())
	if agentName == "" {
		return firstNonEmptyString(runID, "-")
	}
	if runID == "" || runID == agentName {
		return agentName
	}
	return agentName + "-" + shortRunID(runID)
}

func shortRunID(runID string) string {
	runID = strings.TrimSpace(runID)
	if len(runID) <= 8 {
		return runID
	}
	return runID[:8]
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

func formatComposeLogTimestamp(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format("2006-01-02T15:04:05.000Z")
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

func cacheFilterFromOptions(options composeCacheFilterOptions) (*agentcomposev2.CacheFilter, error) {
	driver, err := cacheDriverFilterValue(options.Driver)
	if err != nil {
		return nil, err
	}
	cacheType, err := cacheTypeFilterValue(options.Type)
	if err != nil {
		return nil, err
	}
	status, err := cacheStatusFilterValue(options.Status)
	if err != nil {
		return nil, err
	}
	if driver == "" && cacheType == "" && status == agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED {
		return nil, nil
	}
	return &agentcomposev2.CacheFilter{
		Driver: driver,
		Type:   cacheType,
		Status: status,
	}, nil
}

func cacheFilterFromPruneOptions(options composeCachePruneOptions) (*agentcomposev2.CacheFilter, error) {
	base, err := cacheFilterFromOptions(options.composeCacheFilterOptions)
	if err != nil {
		return nil, err
	}
	status, err := cachePruneShortcutStatus(options)
	if err != nil {
		return nil, err
	}
	if status != agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED {
		if base == nil {
			base = &agentcomposev2.CacheFilter{}
		}
		base.Status = status
	}
	if strings.TrimSpace(options.OlderThan) != "" {
		seconds, err := parseOlderThanSeconds(options.OlderThan)
		if err != nil {
			return nil, err
		}
		if base == nil {
			base = &agentcomposev2.CacheFilter{}
		}
		base.OlderThanSeconds = seconds
	}
	return base, nil
}

func cachePruneShortcutStatus(options composeCachePruneOptions) (agentcomposev2.CacheStatus, error) {
	var selected []string
	var status agentcomposev2.CacheStatus
	if options.Unused {
		selected = append(selected, "--unused")
		status = agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED
	}
	if options.Orphaned {
		selected = append(selected, "--orphaned")
		status = agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED
	}
	if options.Expired {
		selected = append(selected, "--expired")
		status = agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED
	}
	if len(selected) > 1 {
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED, fmt.Errorf("%s are mutually exclusive", strings.Join(selected, ", "))
	}
	if len(selected) == 1 && strings.TrimSpace(options.Status) != "" {
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED, fmt.Errorf("%s cannot be combined with --status", selected[0])
	}
	return status, nil
}

func parseOlderThanSeconds(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	var duration time.Duration
	var err error
	if strings.HasSuffix(value, "d") {
		daysText := strings.TrimSpace(strings.TrimSuffix(value, "d"))
		days, parseErr := strconv.ParseFloat(daysText, 64)
		if parseErr != nil {
			err = parseErr
		} else {
			duration = time.Duration(days * float64(24*time.Hour))
		}
	} else {
		duration, err = time.ParseDuration(value)
	}
	if err != nil {
		return 0, fmt.Errorf("invalid --older-than %q: expected a positive duration such as 7d or 24h", value)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("invalid --older-than %q: duration must be positive", value)
	}
	if duration < time.Second {
		return 0, fmt.Errorf("invalid --older-than %q: duration must be at least 1s", value)
	}
	return uint64(duration / time.Second), nil
}

func cacheDriverFilterValue(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "docker", "boxlite", "microsandbox", "all":
		return strings.ToLower(strings.TrimSpace(value)), nil
	default:
		return "", fmt.Errorf("invalid --driver %q: expected docker, boxlite, microsandbox, or all", value)
	}
}

func cacheTypeFilterValue(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "oci", "materialized", "runtime", "session":
		return strings.ToLower(strings.TrimSpace(value)), nil
	case "sandbox":
		return "session", nil
	default:
		return "", fmt.Errorf("invalid --type %q: expected oci, materialized, runtime, or sandbox", value)
	}
}

func cacheStatusFilterValue(value string) (agentcomposev2.CacheStatus, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED, nil
	case "active":
		return agentcomposev2.CacheStatus_CACHE_STATUS_ACTIVE, nil
	case "referenced":
		return agentcomposev2.CacheStatus_CACHE_STATUS_REFERENCED, nil
	case "unused":
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED, nil
	case "expired":
		return agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED, nil
	case "orphaned":
		return agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED, nil
	case "unknown":
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNKNOWN, nil
	default:
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED, fmt.Errorf("invalid --status %q: expected active, referenced, unused, expired, orphaned, or unknown", value)
	}
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

func cacheDomainText(domain agentcomposev2.CacheDomain) string {
	switch domain {
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_OCI_IMAGE_STORE:
		return "oci-image-store"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_MATERIALIZED_IMAGE_CACHE:
		return "materialized-image-cache"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_RUNTIME_DERIVED_CACHE:
		return "runtime-derived-cache"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_SESSION_EPHEMERAL_STATE:
		return "sandbox-ephemeral-state"
	default:
		return "unspecified"
	}
}

func cacheTypeText(domain agentcomposev2.CacheDomain) string {
	switch domain {
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_OCI_IMAGE_STORE:
		return "oci"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_MATERIALIZED_IMAGE_CACHE:
		return "materialized"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_RUNTIME_DERIVED_CACHE:
		return "runtime"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_SESSION_EPHEMERAL_STATE:
		return "sandbox"
	default:
		return "unspecified"
	}
}

func cacheStatusText(status agentcomposev2.CacheStatus) string {
	switch status {
	case agentcomposev2.CacheStatus_CACHE_STATUS_ACTIVE:
		return "active"
	case agentcomposev2.CacheStatus_CACHE_STATUS_REFERENCED:
		return "referenced"
	case agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED:
		return "unused"
	case agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED:
		return "expired"
	case agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED:
		return "orphaned"
	case agentcomposev2.CacheStatus_CACHE_STATUS_UNKNOWN:
		return "unknown"
	default:
		return "unspecified"
	}
}

func cacheRefSessionText(cache composeCacheOutput) string {
	return firstNonEmptyString(cache.SessionID, cache.SandboxID, cache.ImageRef, cache.ImageID, cache.ResolvedRef, "-")
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

func detachedRunLogsCommand(cli cliOptions, runID string) string {
	parts := []string{"agent-compose"}
	if value := strings.TrimSpace(cli.Host); value != "" {
		parts = append(parts, "--host", value)
	}
	if value := strings.TrimSpace(cli.ComposeFile); value != "" {
		parts = append(parts, "--file", value)
	}
	if value := strings.TrimSpace(cli.ProjectName); value != "" {
		parts = append(parts, "--project-name", value)
	}
	parts = append(parts, "logs", "--run-id", strings.TrimSpace(runID), "--follow")
	for i, part := range parts {
		parts[i] = shellQuoteCLIArg(part)
	}
	return strings.Join(parts, " ")
}

func shellQuoteCLIArg(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`!*?[]{}();&|<>#") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	result := make([]string, 0, len(values)+len(additions))
	for _, value := range append(values, additions...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
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

func commandExitErrorForComposeProject(err error, command, projectName, composePath string) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return commandExitError{
			Code: exitCodeUsage,
			Err: fmt.Errorf(
				"project %q has not been started on this daemon or was removed by `agent-compose down`; run `agent-compose up --file %s` before `agent-compose %s`",
				projectName,
				composePath,
				command,
			),
		}
	}
	return commandExitErrorForConnect(err)
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
