//go:build linux

// Command ac-init is the stage-1 sandbox init process.
//
// It runs as PID 1, owns every workload process, reaps orphaned descendants,
// and serves the daemon's control socket so commands no longer need docker
// exec. The same binary is the extension point for future seccomp/Landlock
// hooks: today it only passes a Hooks value through to the process manager.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

const defaultControlSocket = "/run/agent-compose/init.sock"

func main() {
	var (
		controlSocket = flag.String("control-socket", defaultControlSocket, "Unix socket the daemon uses to control this init process")
		mainShell     = flag.String("main-shell", "", "Shell command to run as the sandbox main process (via /bin/sh -lc)")
		clientMode    = flag.Bool("client", false, "Bridge stdin/stdout to the control socket instead of running as init")
		execChild     = flag.Bool("exec-child", false, "Install the egress filter and exec the command after -- (internal)")
		logLevel      = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	)
	flag.Parse()

	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(*logLevel)) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if *execChild {
		if err := sandboxinit.RunExecChild(flag.Args()); err != nil {
			slog.Error("sandbox init exec child failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if *clientMode {
		if err := sandboxinit.Bridge(ctx, *controlSocket, os.Stdin, os.Stdout); err != nil {
			slog.Error("sandbox init client stopped", "error", err)
			os.Exit(1)
		}
		return
	}

	egressMode, err := sandboxinit.ParseEgressMode(os.Getenv(sandboxinit.EgressModeEnvVar))
	if err != nil {
		slog.Error("sandbox init egress configuration is invalid", "error", err)
		os.Exit(1)
	}
	landlockMode, err := sandboxinit.ParseLandlockMode(os.Getenv(sandboxinit.LandlockModeEnvVar))
	if err != nil {
		slog.Error("sandbox init Landlock configuration is invalid", "error", err)
		os.Exit(1)
	}
	landlockFallback, err := sandboxinit.ParseLandlockFallback(os.Getenv(sandboxinit.LandlockFallbackEnvVar))
	if err != nil {
		slog.Error("sandbox init Landlock fallback configuration is invalid", "error", err)
		os.Exit(1)
	}
	err = sandboxinit.Run(ctx, sandboxinit.Options{
		ControlSocket: *controlSocket,
		MainShell:     *mainShell,
		// The drivers already export SANDBOX_ID into the sandbox environment.
		// Reading it here, rather than adding a flag, keeps an older guest init
		// working when a newer daemon starts it.
		SandboxID:  strings.TrimSpace(os.Getenv(sandboxinit.SandboxIDEnvVar)),
		EgressMode: egressMode,
		Landlock: sandboxinit.LandlockOptions{
			Mode:     landlockMode,
			Fallback: landlockFallback,
		},
	})
	if err == nil {
		return
	}
	var exitErr *sandboxinit.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.Code)
	}
	slog.Error("sandbox init stopped", "error", err)
	os.Exit(1)
}
