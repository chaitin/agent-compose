package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chaitin/agent-compose/cmd/installer/internal/core"
	installertui "github.com/chaitin/agent-compose/cmd/installer/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type commandOptions struct {
	core.Options
	yes           bool
	legacyUpgrade bool
}

func newRootCommand(out, errOut io.Writer) *cobra.Command {
	defaults := core.DefaultOptions()
	defaults.InstallDir = envOrDefault("AGENT_COMPOSE_INSTALL_DIR", defaults.InstallDir)
	defaults.Repository = envOrDefault("AGENT_COMPOSE_REPO", defaults.Repository)
	defaults.ReleaseBaseURL = strings.TrimSpace(os.Getenv("AGENT_COMPOSE_RELEASE_BASE_URL"))
	defaults.FrontendVersion = envOrDefault("AGENT_COMPOSE_FRONTEND_VERSION", defaults.FrontendVersion)
	defaults.KVMPath = envOrDefault("AGENT_COMPOSE_KVM_DETECT_PATH", defaults.KVMPath)
	options := &commandOptions{Options: defaults, yes: truthy(os.Getenv("AGENT_COMPOSE_YES"))}
	root := &cobra.Command{
		Use:           "agent-compose-installer",
		Short:         "Install, upgrade, or uninstall agent-compose",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.PortSet = cmd.Flags().Changed("port")
			options.WithUISet = cmd.Flags().Changed("with-ui")
			if options.legacyUpgrade || options.yes || hasInstallerFlags(cmd) {
				operation := core.OperationInstall
				if options.legacyUpgrade {
					operation = core.OperationUpgrade
				}
				return executeOperation(cmd.Context(), operation, options, out, errOut)
			}
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
				return fmt.Errorf("interactive mode requires a TTY; use install, upgrade, or uninstall with --yes")
			}
			executable, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate installer executable: %w", err)
			}
			service := core.Service{Runner: core.ExecRunner{Output: errOut}}
			return installertui.Run(service, options.Options, executable)
		},
	}
	root.SetOut(out)
	root.SetErr(errOut)
	flags := root.PersistentFlags()
	flags.StringVar(&options.InstallDir, "dir", options.InstallDir, "installation directory")
	flags.IntVar(&options.Port, "port", options.Port, "web UI host port")
	flags.BoolVar(&options.WithUI, "with-ui", options.WithUI, "also publish the web UI")
	flags.BoolVar(&options.SkipGuestPull, "skip-guest-pull", false, "do not pre-pull the sandbox guest image")
	flags.StringVar(&options.Version, "version", options.Version, "application release version")
	flags.StringVar(&options.ImagePrefix, "image-prefix", "", "image registry prefix")
	flags.BoolVar(&options.NoStart, "no-start", false, "prepare files without starting services")
	flags.BoolVarP(&options.yes, "yes", "y", options.yes, "skip confirmation prompts")
	flags.BoolVar(&options.legacyUpgrade, "upgrade", false, "upgrade an existing installation (legacy form)")
	_ = flags.MarkHidden("upgrade")

	root.AddCommand(newOperationCommand(core.OperationInstall, options, out, errOut))
	root.AddCommand(newOperationCommand(core.OperationUpgrade, options, out, errOut))
	root.AddCommand(&cobra.Command{
		Use:   "installer-version",
		Short: "Print the installer binary version",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(out, version)
			return err
		},
	})
	uninstall := newOperationCommand(core.OperationUninstall, options, out, errOut)
	uninstall.Flags().BoolVar(&options.Purge, "purge", false, "also remove configuration and persistent data")
	root.AddCommand(uninstall)
	return root
}

func newOperationCommand(operation core.Operation, options *commandOptions, out, errOut io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   string(operation),
		Short: strings.ToUpper(string(operation[:1])) + string(operation[1:]) + " agent-compose",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.PortSet = cmd.Flags().Changed("port")
			options.WithUISet = cmd.Flags().Changed("with-ui")
			return executeOperation(cmd.Context(), operation, options, out, errOut)
		},
	}
}

func executeOperation(ctx context.Context, operation core.Operation, options *commandOptions, out, errOut io.Writer) error {
	if !options.yes {
		confirmed, err := confirmOperation(operation, options.Options)
		if err != nil {
			return err
		}
		if !confirmed {
			return fmt.Errorf("%s cancelled", operation)
		}
	}
	if operation != core.OperationUninstall {
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate installer executable: %w", err)
		}
		options.InstallerPath = executable
	}
	service := core.Service{
		Runner: core.ExecRunner{Output: errOut},
		Reporter: core.ReporterFunc(func(event core.Event) {
			// Command execution reports its own output errors; progress output is best effort.
			_, _ = fmt.Fprintf(errOut, "[%s] %s\n", event.Kind, event.Message)
		}),
	}
	result, err := service.Apply(ctx, operation, options.Options)
	if err != nil {
		return err
	}
	return writeResult(out, operation, result, options.Purge)
}

func confirmOperation(operation core.Operation, options core.Options) (bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("confirmation requires a TTY; rerun with --yes: %w", err)
	}
	// A close failure cannot affect a completed terminal interaction.
	defer func() { _ = tty.Close() }()
	return confirmOperationOnTTY(operation, options, tty)
}

func confirmOperationOnTTY(operation core.Operation, options core.Options, tty io.ReadWriter) (bool, error) {
	if _, err := fmt.Fprintf(tty, "%s agent-compose in %s", operation, options.InstallDir); err != nil {
		return false, err
	}
	if operation == core.OperationUninstall && options.Purge {
		if _, err := fmt.Fprint(tty, " and permanently delete its configuration and data"); err != nil {
			return false, err
		}
	}
	if _, err := fmt.Fprint(tty, "? [y/N] "); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func writeResult(out io.Writer, operation core.Operation, result core.Result, purged bool) error {
	if operation == core.OperationUninstall {
		if _, err := fmt.Fprintf(out, "agent-compose uninstalled from %s\n", result.InstallDir); err != nil {
			return err
		}
		if !purged {
			if _, err := fmt.Fprintln(out, "Configuration and persistent data were preserved."); err != nil {
				return err
			}
		}
		if len(result.RetainedFiles) > 0 {
			if _, err := fmt.Fprintf(out, "Unknown files retained: %s\n", strings.Join(result.RetainedFiles, ", ")); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := fmt.Fprintf(out, "agent-compose %s complete\n", operation); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Directory: %s\nCompose: %s\n", result.InstallDir, result.ComposeFiles); err != nil {
		return err
	}
	if result.URL != "" {
		if _, err := fmt.Fprintf(out, "URL: %s\n", result.URL); err != nil {
			return err
		}
	}
	if result.GeneratedPassword != "" {
		if _, err := fmt.Fprintf(out, "Username: %s\nPassword: %s\n", result.Username, result.GeneratedPassword); err != nil {
			return err
		}
	}
	if !result.WithUI() {
		if _, err := fmt.Fprintf(out, "Web UI not installed. Enable it with:\n  cd %s && docker compose --profile %s up -d\n", result.InstallDir, "with-ui"); err != nil {
			return err
		}
	}
	return nil
}

func hasInstallerFlags(cmd *cobra.Command) bool {
	for _, name := range []string{"dir", "port", "version", "image-prefix", "no-start", "yes", "with-ui", "skip-guest-pull"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y":
		return true
	default:
		return false
	}
}
