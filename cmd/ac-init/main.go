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

	if *clientMode {
		if err := sandboxinit.Bridge(ctx, *controlSocket, os.Stdin, os.Stdout); err != nil {
			slog.Error("sandbox init client stopped", "error", err)
			os.Exit(1)
		}
		return
	}

	err := sandboxinit.Run(ctx, sandboxinit.Options{
		ControlSocket: *controlSocket,
		MainShell:     *mainShell,
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
