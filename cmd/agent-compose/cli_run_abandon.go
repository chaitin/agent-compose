package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// abandonCompletionOptions configures `agent-compose abandon-completion`.
type abandonCompletionOptions struct {
	Reason string
}

// newCLIAbandonCompletionCommand is the operator's way out of a run whose
// sandbox cleanup will not converge. It is deliberately separate from `stop`:
// stop asks a run that is still executing to finish and still cleans up its
// runtime, while this gives up on that cleanup and says so in the record.
func newCLIAbandonCompletionCommand(cli *cliOptions) *cobra.Command {
	options := abandonCompletionOptions{}
	cmd := &cobra.Command{
		Use:   "abandon-completion <run>",
		Short: "End a run whose sandbox cleanup cannot converge",
		Args:  abandonCompletionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeAbandonCompletionCommand(cmd, *cli, options, args)
		},
	}
	cmd.Flags().StringVar(&options.Reason, "reason", "", "Operator note recorded with the abandoned cleanup")
	return cmd
}

// abandonCompletionArgs reports misuse as a usage error, the way the other
// commands do, instead of inheriting cobra's generic exit code.
func abandonCompletionArgs(_ *cobra.Command, args []string) error {
	if len(args) != 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("abandon-completion accepts 1 arg(s), received %d", len(args))}
	}
	return nil
}

func runComposeAbandonCompletionCommand(cmd *cobra.Command, cli cliOptions, options abandonCompletionOptions, args []string) error {
	runID := strings.TrimSpace(args[0])
	if runID == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run id is required")}
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	response, err := clients.run.AbandonRunCompletion(cmd.Context(), connect.NewRequest(&agentcomposev2.AbandonRunCompletionRequest{
		RunId:  runID,
		Reason: options.Reason,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("abandon run completion: %w", err))
	}
	run := composeRunOutputFromDetail(response.Msg.GetRun())
	if cli.JSON {
		data, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return commandExitError{Code: exitCodeGeneral, Err: err}
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	outcome := "already terminal; nothing was abandoned"
	if response.Msg.GetAbandoned() {
		outcome = "cleanup abandoned"
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Run %s is %s (%s).\n", run.ID, run.Status, outcome); err != nil {
		return commandExitError{Code: exitCodeGeneral, Err: err}
	}
	if !response.Msg.GetAbandoned() {
		return nil
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Cleanup error: %s\n", cleanupErrorText(run.CleanupError)); err != nil {
		return commandExitError{Code: exitCodeGeneral, Err: err}
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), "The sandbox runtime may still exist; this command did not remove it."); err != nil {
		return commandExitError{Code: exitCodeGeneral, Err: err}
	}
	return nil
}

// cleanupErrorText keeps the operator-facing line readable when a run has no
// recorded cleanup failure.
func cleanupErrorText(recorded string) string {
	if text := strings.TrimSpace(recorded); text != "" {
		return text
	}
	return "no cleanup failure recorded"
}
