//go:build linux

// Command ac-supervisor is the per-sandbox egress supervisor.
//
// It runs as its own process outside the workload container. The sandbox init
// process reaches it over a host-mounted Unix domain socket; the workload
// container itself can therefore run with no external network interface.
//
// The supervisor's secret inputs arrive through the environment and are removed
// before the server starts, so a later /proc self-inspection by an untrusted
// process in this process's namespace cannot recover them. Ordinary egress is
// forwarded through the daemon forward proxy, which stays the policy decision
// point; engine-owned endpoints are connected directly.
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

	"github.com/chaitin/agent-compose/pkg/sandboxsupervisor"
)

func main() {
	var (
		socketPath = flag.String("socket", "", "Unix domain socket the sandbox init process connects to")
		sandboxID  = flag.String("sandbox-id", "", "sandbox this supervisor serves")
		logLevel   = flag.String("log-level", "info", "log level (debug, info, warn, error)")
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

	token := strings.TrimSpace(os.Getenv(sandboxsupervisor.EnvToken))
	proxyURL := strings.TrimSpace(os.Getenv(sandboxsupervisor.EnvProxy))
	direct := splitEndpoints(os.Getenv(sandboxsupervisor.EnvDirectEndpoints))
	for _, name := range []string{sandboxsupervisor.EnvToken, sandboxsupervisor.EnvProxy, sandboxsupervisor.EnvDirectEndpoints} {
		_ = os.Unsetenv(name)
	}

	server, err := sandboxsupervisor.NewServer(sandboxsupervisor.ServerOptions{
		SocketPath:      *socketPath,
		Token:           token,
		SandboxID:       *sandboxID,
		ProxyURL:        proxyURL,
		DirectEndpoints: direct,
	})
	if err != nil {
		slog.Error("sandbox supervisor configuration is invalid", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := server.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("sandbox supervisor stopped", "sandbox_id", *sandboxID, "error", err)
		os.Exit(1)
	}
}

func splitEndpoints(raw string) []string {
	parts := strings.Split(raw, ",")
	endpoints := make([]string, 0, len(parts))
	for _, part := range parts {
		if endpoint := strings.TrimSpace(part); endpoint != "" {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints
}
