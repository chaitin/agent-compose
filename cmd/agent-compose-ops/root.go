// Command agent-compose-ops is the read-only operations helper for an
// agent-compose deployment. Its first job is to answer "is this a good time to
// restart the daemon?" without touching the running daemon: it reads the
// persisted SQLite state directly and never opens it for writing.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/storage/sqlite"
	"github.com/chaitin/agent-compose/pkg/upgradeadvisor"
)

func main() {
	os.Exit(execute(os.Stdout, os.Stderr, os.Args[1:]))
}

func execute(out, errOut io.Writer, args []string) int {
	root := newRootCommand(out, errOut)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintln(errOut, "agent-compose-ops:", err)
		return 1
	}
	return 0
}

type cliOptions struct {
	DataRoot string
	Database string
	Timezone string
	JSON     bool
}

func newRootCommand(out, errOut io.Writer) *cobra.Command {
	options := cliOptions{}
	root := &cobra.Command{
		Use:           "agent-compose-ops",
		Short:         "Read-only operations helper for an agent-compose deployment",
		Long:          "agent-compose-ops inspects a running agent-compose deployment without changing it.\nIt reads the daemon's SQLite database in read-only mode to report current work and to recommend daemon upgrade windows.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetOut(out)
	root.SetErr(errOut)
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&options.DataRoot, "data-root", "", "Agent-compose data directory (defaults to DATA_ROOT or the platform data dir)")
	root.PersistentFlags().StringVar(&options.Database, "db", "", "Path to data.db; overrides --data-root")
	root.PersistentFlags().StringVar(&options.Timezone, "tz", "Local", "Timezone for report timestamps")
	root.PersistentFlags().BoolVar(&options.JSON, "json", false, "Print machine-readable JSON")

	upgrade := &cobra.Command{
		Use:   "upgrade",
		Short: "Analyze the work a daemon upgrade would interrupt",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	upgrade.AddCommand(
		newStatusCommand(&options),
		newStatsCommand(&options),
		newUpcomingCommand(&options),
		newWindowsCommand(&options),
		newForecastCommand(&options),
	)
	root.AddCommand(upgrade, newVersionCommand())
	return root
}

func (o *cliOptions) location() (*time.Location, error) {
	timezone := strings.TrimSpace(o.Timezone)
	if timezone == "" || strings.EqualFold(timezone, "local") {
		return time.Local, nil
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", timezone, err)
	}
	return location, nil
}

func (o *cliOptions) databasePath() (string, error) {
	if path := strings.TrimSpace(o.Database); path != "" {
		return filepath.Abs(path)
	}
	dataRoot := strings.TrimSpace(o.DataRoot)
	if dataRoot == "" {
		dataRoot = appconfig.DefaultDataRoot()
	}
	absolute, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", fmt.Errorf("resolve data root %q: %w", dataRoot, err)
	}
	return filepath.Join(absolute, "data.db"), nil
}

func (o *cliOptions) openAdvisor(options upgradeadvisor.Options) (*upgradeadvisor.Advisor, func(), error) {
	location, err := o.location()
	if err != nil {
		return nil, nil, err
	}
	path, err := o.databasePath()
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, nil, fmt.Errorf("data.db not found at %s: %w; pass --data-root or --db, or run inside the daemon container with `docker exec agent-compose agent-compose-ops ...`", path, err)
	}
	database, err := sqlite.OpenReadOnly(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s read-only: %w", path, err)
	}
	options.Now = options.Now.UTC()
	options.Location = location
	advisor, err := upgradeadvisor.New(database.DB(), options)
	if err != nil {
		_ = database.Close()
		return nil, nil, err
	}
	return advisor, func() { _ = database.Close() }, nil
}
