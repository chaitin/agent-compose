package main

import (
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/identity"
	"github.com/spf13/cobra"
)

func newCLIInspectCommand(cli *cliOptions) *cobra.Command {
	var omitScripts bool
	cmd := &cobra.Command{
		Use:   "inspect <id>|<project|agent|run|sandbox|image|cache|volume> [name-or-id]",
		Short: "Inspect project, agent, run, sandbox, image, cache, or volume details",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := strings.ToLower(strings.TrimSpace(args[0]))
			if omitScripts && kind != "project" && kind != "agent" && (len(args) != 1 || !identity.IsIDPrefix(kind)) {
				return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("--omit-scripts requires a project or agent resource")}
			}
			return runComposeInspectCommand(cmd, *cli, args)
		},
	}
	cmd.Flags().BoolVar(&omitScripts, "omit-scripts", false, "Omit project and agent script bodies and report their size and SHA-256")
	return cmd
}
