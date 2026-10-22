package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"

	"github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
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
	if err := godotenv.Load(); err != nil {
		// The parser embeds the unparsed remainder of the file in its message,
		// which can include provider credentials.
		log.Printf("dotenv load skipped: %v", sanitizeDotenvError(err))
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
