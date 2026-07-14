package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"agent-compose/pkg/compose"
	"agent-compose/pkg/identity"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func runComposeLogsForScope(cmd *cobra.Command, cli cliOptions, client agentcomposev2connect.RunServiceClient, projectID, projectName string, options composeLogsOptions) error {
	if strings.TrimSpace(options.RunID) != "" {
		run, err := getRunDetail(cmd.Context(), client, projectID, options.RunID)
		if err != nil {
			project := firstNonEmptyString(projectName, shortOpaqueID(projectID), "-")
			return commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", strings.TrimSpace(options.RunID), project, err))
		}
		if options.Follow {
			return followRunLogStream(cmd.Context(), cmd.OutOrStdout(), client, projectID, run.Msg.GetRun().GetSummary(), options)
		}
		return writeLogsForRun(cmd.OutOrStdout(), run.Msg.GetRun(), cli.JSON, options)
	}
	return followOrPrintProjectLogs(cmd, cli, client, projectID, projectName, options)
}

func resolveComposeLogRefs(ctx context.Context, client agentcomposev2connect.RunServiceClient, normalized *compose.NormalizedProjectSpec, projectID string, options composeLogsOptions) (composeLogsOptions, error) {
	if strings.TrimSpace(options.AgentName) != "" {
		agentName, err := resolveComposeAgentNameFromSpec(normalized, projectID, options.AgentName)
		if err != nil {
			return options, err
		}
		options.AgentName = agentName
	}
	return resolveComposeLogIDRefs(ctx, client, projectID, options)
}

func resolveComposeLogIDRefs(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID string, options composeLogsOptions) (composeLogsOptions, error) {
	if shouldResolveComposeLogResourceRef(options.RunID) {
		runID, err := resolveComposeRunIDRef(ctx, client, projectID, options.AgentName, options.RunID)
		if err != nil {
			return options, err
		}
		options.RunID = runID
	}
	if shouldResolveComposeLogResourceRef(options.SandboxID) {
		sandboxID, err := resolveComposeSandboxIDRefFromRuns(ctx, client, projectID, options.AgentName, options.SandboxID)
		if err != nil {
			return options, err
		}
		options.SandboxID = sandboxID
	}
	return options, nil
}

func resolveComposeLogResourceRef(cmd *cobra.Command, client agentcomposev2connect.ResourceServiceClient, options composeLogsOptions) (string, string, composeLogsOptions, error) {
	ref := strings.TrimSpace(options.ResourceRef)
	resource, err := resolveCLIResourceRef(cmd, client, ref, []agentcomposev2.ResourceKind{
		agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT,
		agentcomposev2.ResourceKind_RESOURCE_KIND_AGENT,
		agentcomposev2.ResourceKind_RESOURCE_KIND_RUN,
		agentcomposev2.ResourceKind_RESOURCE_KIND_SANDBOX,
	}, "use a full ID or an explicit logs filter to select one")
	if err != nil {
		return "", "", options, err
	}

	projectID := strings.TrimSpace(resource.GetProjectId())
	projectName := strings.TrimSpace(resource.GetProjectName())
	switch resource.GetKind() {
	case agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT:
		projectID = firstNonEmptyString(resource.GetId(), resource.GetProjectId())
		projectName = firstNonEmptyString(resource.GetName(), resource.GetProjectName())
		if projectID == "" {
			return "", "", options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolved project %q has no id", ref)}
		}
	case agentcomposev2.ResourceKind_RESOURCE_KIND_AGENT:
		options.AgentName = firstNonEmptyString(resource.GetName(), resource.GetInspectRef())
		if projectID == "" || options.AgentName == "" {
			return "", "", options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolved agent %q is missing its project or name", ref)}
		}
	case agentcomposev2.ResourceKind_RESOURCE_KIND_RUN:
		if options.RunID != "" || options.SandboxID != "" {
			return "", "", options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs run ref cannot be combined with --run or --sandbox")}
		}
		options.RunID = firstNonEmptyString(resource.GetId(), resource.GetInspectRef())
		if options.RunID == "" {
			return "", "", options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolved run %q has no id", ref)}
		}
	case agentcomposev2.ResourceKind_RESOURCE_KIND_SANDBOX:
		if options.RunID != "" || options.SandboxID != "" {
			return "", "", options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs sandbox ref cannot be combined with --run or --sandbox")}
		}
		options.SandboxID = firstNonEmptyString(resource.GetId(), resource.GetInspectRef())
		if options.SandboxID == "" {
			return "", "", options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolved sandbox %q has no id", ref)}
		}
	default:
		return "", "", options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resource %q cannot be used with logs", ref)}
	}
	return projectID, projectName, options, nil
}

func shouldResolveComposeLogResourceRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	return identity.IsID(ref) || identity.IsIDPrefix(ref)
}
