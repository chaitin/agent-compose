package main

import (
	"context"
	"io"

	"github.com/spf13/cobra"

	"agent-compose/internal/cli"
	"agent-compose/internal/daemon"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type daemonRunner = cli.Runner
type cliClientConfig = cli.ClientConfig
type composeUpOutput = cli.UpOutput
type composeUpChangeOutput = cli.UpChangeOutput
type composeRunOutput = cli.RunOutput
type composeLogsOutput = cli.LogsOutput
type composePSOutput = cli.PSOutput
type composeProjectOutput = cli.ProjectOutput
type composeAgentInspectOutput = cli.AgentInspectOutput
type composeSessionOutput = cli.SessionOutput
type composeExecOutput = cli.ExecOutput
type composeImageListOutput = cli.ImageListOutput
type composeImageInspectOutput = cli.ImageInspectOutput
type composeImagePullOutput = cli.ImagePullOutput
type composeImageRemoveOutput = cli.ImageRemoveOutput
type composeImageOutput = cli.ImageOutput

const (
	exitCodeGeneral     = cli.ExitCodeGeneral
	exitCodeUsage       = cli.ExitCodeUsage
	exitCodeUnavailable = cli.ExitCodeUnavailable
)

func executeCLI(ctx context.Context, out, errOut io.Writer, args []string, runDaemon daemonRunner) int {
	return cli.Execute(ctx, out, errOut, args, runDaemon)
}

func newRootCommand(out, errOut io.Writer, runDaemon daemonRunner) *cobra.Command {
	return cli.NewRootCommand(out, errOut, runDaemon)
}

func resolveCLIClientConfig(hostFlag string) (cliClientConfig, error) {
	return cli.ResolveClientConfig(hostFlag)
}

func composeImageOutputFromProto(image *agentcomposev2.Image) composeImageOutput {
	return cli.ImageOutputFromProto(image)
}

type DaemonOptions = daemon.Options
type DaemonApp = daemon.App

func NewDaemonApp(ctx context.Context, opts DaemonOptions) (*DaemonApp, error) {
	return daemon.NewApp(ctx, opts)
}
