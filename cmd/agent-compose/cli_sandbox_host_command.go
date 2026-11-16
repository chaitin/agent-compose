package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/fxgo/utils"
	"github.com/joho/godotenv"
	"github.com/samber/do/v2"
	"github.com/spf13/cobra"
)

func newCLISandboxHostCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sandbox-host",
		Short: "Run the long-lived sandbox-host process that holds VM driver state",
		Long: "Run the sandbox-host process that owns microsandbox and BoxLite VMs so the daemon can be " +
			"upgraded without tearing them down. Normally started by the daemon or by compose; see " +
			"docs/design/daemon-upgrade-sandbox-survival.zh-CN.md.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSandboxHost(cmd.Context())
		},
	}
}

func runSandboxHost(ctx context.Context) error {
	// First, so no write the host makes, including the log lines below, can end
	// it with SIGPIPE.
	notifySIGPIPE()
	if err := godotenv.Load(); err != nil {
		// The parser embeds the unparsed remainder of the file in its message,
		// which can include provider credentials.
		log.Printf("dotenv load skipped: %v", sanitizeDotenvError(err))
	}
	// The host owns the VM processes, which inherit its file-descriptor limit;
	// raise it the same way the daemon does so a VM's open files are not capped
	// by whatever limit the host process inherited.
	if err := utils.SetRlimitNoFile(); err != nil {
		log.Printf("Warning: Failed to set RLIMIT_NOFILE: %v", err)
	}

	di := do.New()
	do.ProvideValue(di, ctx)
	do.Provide(di, NewLogger)
	config.Setup(di)
	conf := do.MustInvoke[*config.Config](di)

	lockFile, err := acquireSandboxHostLock(conf.DataRoot)
	if err != nil {
		return err
	}
	defer func() { _ = lockFile.Close() }()

	return driverpkg.ServeSandboxHost(ctx, conf)
}

// notifySIGPIPE keeps a write to a closed pipe or socket from terminating the
// host and every VM it owns. The microsandbox and BoxLite drivers run native
// code on threads the Go runtime did not create, and Go ends the process when
// SIGPIPE arrives on such a thread unless the program has registered for it
// (os/signal, "Go programs that use cgo or SWIG"); the same registration also
// covers writes to a closed stdout or stderr. With it, those writes fail with
// EPIPE where they happen. Notify is used rather than Ignore because an
// ignored SIGPIPE would be inherited by the child processes the host starts.
func notifySIGPIPE() {
	// Nothing reads the channel: delivery to it never blocks, and the
	// registration alone changes how the runtime handles the signal.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
}

// acquireSandboxHostLock takes an exclusive, non-blocking advisory lock so only
// one sandbox-host runs per data root. A daemon that spawns a host while one is
// already holding the lock will see the new host exit here.
func acquireSandboxHostLock(dataRoot string) (*os.File, error) {
	lockPath := filepath.Join(dataRoot, "sandbox-host.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open sandbox-host lock %q: %w", lockPath, err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("sandbox-host is already running (lock %q is held): %w", lockPath, err)
	}
	return lockFile, nil
}
