package main

import (
	"errors"
	"fmt"
	"github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"io"
	"runtime"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

type buildInfo struct {
	Version         string   `json:"version"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	CompiledDrivers []string `json:"compiled_drivers"`
}

func buildInfoForVersion(version string) buildInfo {
	return buildInfo{
		Version:         version,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		CompiledDrivers: driverpkg.CompiledRuntimeDrivers(),
	}
}

func currentBuildInfo() buildInfo {
	return buildInfoForVersion(config.BuildVersion)
}

func newRootCommand(out, errOut io.Writer, runDaemon daemonRunner) *cobra.Command {
	options := cliOptions{}
	root := &cobra.Command{
		Use:           "agent-compose",
		Short:         "agent-compose daemon and CLI",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if options.Timeout < 0 {
				return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("--timeout must not be negative")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return commandExitError{Code: exitCodeUsage, Err: err}
	})
	root.CompletionOptions.DisableDefaultCmd = true

	root.PersistentFlags().StringVar(&options.Host, "host", "", "Daemon HTTP endpoint")
	root.PersistentFlags().StringVarP(&options.ComposeFile, "file", "f", "", "Path to agent-compose.yml")
	root.PersistentFlags().StringVarP(&options.ProjectName, "project-name", "p", "", "Select a deployed project by name (not supported by up or project up)")
	root.PersistentFlags().BoolVar(&options.JSON, "json", false, "Print machine-readable JSON")
	root.PersistentFlags().DurationVar(&options.Timeout, "timeout", 0, "Maximum duration of each daemon RPC, including streams; 0 disables the timeout")

	commands := []*cobra.Command{
		newCLIDaemonCommand(runDaemon),
		newCLIVersionCommand(&options),
		newCLIStatusCommand(&options),
		newCLIAuthCommand(&options),
		newCLIConfigCommand(&options),
		newCLIProjectCommand(&options),
		newCLIAgentCommand(&options),
		newCLIAgentListCommand(&options),
		newCLIProjectUpCommand(&options),
		newCLIProjectDownCommand(&options),
		newCLIRunCommand(&options),
		newCLISchedulerCommand(&options),
		newCLILogsCommand(&options),
		newCLIPSCommand(&options),
		newCLIStatsCommand(&options),
		newCLISandboxCommand(&options),
	}
	commands = append(commands, newCLILegacySandboxCommands(&options)...)
	commands = append(commands,
		newCLIExecCommand(&options),
		newCLIImagesCommand(&options),
		newCLICacheCommand(&options),
		newCLIVolumeCommand(&options),
		newCLILLMCommand(&options),
		newCLIImageCommand(&options),
	)
	commands = append(commands, newCLILegacyImageCommands(&options)...)
	commands = append(commands, newCLIInspectCommand(&options))
	root.AddCommand(commands...)
	return root
}

type cliOptions struct {
	Host        string
	ComposeFile string
	ProjectName string
	JSON        bool
	Timeout     time.Duration
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

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr commandExitError
	if errors.As(err, &exitErr) && exitErr.Code > 0 {
		return exitErr.Code
	}
	if isSchedulerResourceNotFound(err) {
		return exitCodeUsage
	}
	return exitCodeGeneral
}

func commandExitErrorForConnect(err error) error {
	if isAttachHTTP2TransportMismatch(err) {
		return commandExitError{
			Code: exitCodeUnavailable,
			Err:  fmt.Errorf("%w; attach RPCs require HTTP/2 h2c, restart the agent-compose daemon with a matching build or connect directly without an HTTP/1 proxy", err),
		}
	}
	switch connect.CodeOf(err) {
	case connect.CodeDeadlineExceeded:
		return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("%w; increase --timeout or set --timeout 0, and check daemon-side operation timeouts", err)}
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
