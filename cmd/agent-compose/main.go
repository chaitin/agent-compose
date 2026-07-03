package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"agent-compose/internal/cli"
	"agent-compose/internal/daemon"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Execute(ctx, os.Stdout, os.Stderr, os.Args[1:], daemon.Run))
}
