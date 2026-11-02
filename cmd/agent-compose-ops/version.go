package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/chaitin/agent-compose/pkg/config"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print agent-compose-ops version and platform",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "agent-compose-ops %s %s/%s\n", config.BuildVersion, runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}
