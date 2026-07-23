package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newCLIProjectCommand(options *cliOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newCLIProjectListCommand(options), newCLIProjectUpCommand(options), newCLIProjectDownCommand(options))
	return cmd
}

func newCLIProjectListCommand(options *cliOptions) *cobra.Command {
	listOptions := composeListProjectsOptions{}
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List daemon projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runComposeListProjectsCommand(cmd, *options, listOptions)
		},
	}
	cmd.Flags().BoolVar(&listOptions.Verbose, "verbose", false, "Show more project details")
	cmd.Flags().Uint32Var(&listOptions.Limit, "limit", 0, "Maximum number of projects to return")
	cmd.Flags().Uint32Var(&listOptions.Offset, "offset", 0, "Project list offset")
	return cmd
}

func newCLIProjectUpCommand(options *cliOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Apply the current compose project to the daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runComposeUpCommand(cmd, *options)
		},
	}
}

func newCLIProjectDownCommand(options *cliOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Stop project schedulers and running sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runComposeDownCommand(cmd, *options)
		},
	}
}

func newCLIAgentCommand(options *cliOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage project agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newCLIAgentListCommand(options))
	return cmd
}

func newCLIAgentListCommand(options *cliOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List current project agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runComposeListAgentsCommand(cmd, *options)
		},
	}
}

type composeAgentListOutput struct {
	Project composeUpProjectOutput      `json:"project"`
	Agents  []composeProjectAgentOutput `json:"agents"`
}

func runComposeListAgentsCommand(cmd *cobra.Command, options cliOptions) error {
	clients, err := newCLIServiceClients(options)
	if err != nil {
		return err
	}
	runtimeProject, err := resolveComposeRuntimeProject(cmd.Context(), clients.project, options, "agent ls", runtimeProjectWithState)
	if err != nil {
		return err
	}
	project := runtimeProject.project
	output := composeAgentListOutput{
		Project: composeProjectSummaryOutput(project.GetSummary()),
		Agents:  make([]composeProjectAgentOutput, 0, len(project.GetAgents())),
	}
	for _, agent := range project.GetAgents() {
		output.Agents = append(output.Agents, composeProjectAgentOutputFromProto(agent))
	}
	if options.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal agent list output: %w", err)
		}
		return writeCommandOutput(cmd.OutOrStdout(), data)
	}
	return writeAgentListText(cmd.OutOrStdout(), output.Agents)
}

func writeAgentListText(out io.Writer, agents []composeProjectAgentOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "AGENT\tPROVIDER\tMODEL\tIMAGE\tDRIVER\tSCHEDULER"); err != nil {
		return err
	}
	for _, agent := range agents {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%t\n", agent.Name, agent.Provider, agent.Model, agent.Image, agent.Driver, agent.SchedulerEnabled); err != nil {
			return err
		}
	}
	return tw.Flush()
}
