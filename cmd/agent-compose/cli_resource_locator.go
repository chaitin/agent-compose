package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func runComposeIDInspectCommand(cmd *cobra.Command, cli cliOptions, id string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	target, err := resolveCLIResourceID(cmd, clients.resource, id, nil)
	if err != nil {
		return err
	}
	return inspectResolvedTarget(cmd, cli, clients, target)
}

func resolveCLIResourceID(cmd *cobra.Command, client agentcomposev2connect.ResourceServiceClient, id string, kinds []agentcomposev2.ResourceKind) (*agentcomposev2.ResourceTarget, error) {
	id = strings.TrimSpace(id)
	response, err := client.ResolveID(cmd.Context(), connect.NewRequest(&agentcomposev2.ResolveResourceIDRequest{Id: id, Kinds: kinds}))
	if err != nil {
		return nil, commandExitErrorForConnect(fmt.Errorf("resolve resource id %s: %w", id, err))
	}
	for _, warning := range response.Msg.GetWarnings() {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", warning); err != nil {
			return nil, err
		}
	}
	targets := response.Msg.GetTargets()
	if len(targets) == 0 {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resource id %q not found", id)}
	}
	if len(targets) > 1 {
		matches := make([]string, 0, len(targets))
		for _, target := range targets {
			matches = append(matches, describeResourceTarget(target))
		}
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resource id %q is ambiguous; matches: %s; use inspect <type> %s to select one", id, strings.Join(matches, ", "), id)}
	}
	return targets[0], nil
}

func inspectResolvedTarget(cmd *cobra.Command, cli cliOptions, clients cliServiceClients, target *agentcomposev2.ResourceTarget) error {
	if target == nil {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolved resource target is empty")}
	}
	if flag := cmd.Flags().Lookup("omit-scripts"); flag != nil && flag.Value.String() == "true" && target.GetKind() != agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT && target.GetKind() != agentcomposev2.ResourceKind_RESOURCE_KIND_AGENT {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("--omit-scripts requires a project or agent resource")}
	}
	switch target.GetKind() {
	case agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT:
		project, err := clients.project.GetProject(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: target.GetId()}}, IncludeSpec: true}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect project %s: %w", target.GetId(), err))
		}
		return writeComposeProjectInspectOutput(cmd, project.Msg.GetProject())
	case agentcomposev2.ResourceKind_RESOURCE_KIND_AGENT:
		project, err := clients.project.GetProject(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: target.GetProjectId()}}, IncludeSpec: true}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect agent %s: %w", target.GetAgentName(), err))
		}
		agent, err := composeAgentInspectOutputFor(cmd.Context(), clients, project.Msg.GetProject(), target.GetAgentName())
		if err != nil {
			return err
		}
		return writeComposeInspectOutput(cmd, agent)
	case agentcomposev2.ResourceKind_RESOURCE_KIND_RUN:
		run, err := getRunDetail(cmd.Context(), clients.run, target.GetProjectId(), target.GetId())
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect run %s: %w", target.GetId(), err))
		}
		return writeComposeInspectOutput(cmd, composeRunOutputFromDetail(run.Msg.GetRun()))
	case agentcomposev2.ResourceKind_RESOURCE_KIND_SANDBOX:
		output, err := composeSandboxInspectOutputFor(cmd.Context(), clients, target.GetId())
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect sandbox %s: %w", target.GetId(), err))
		}
		return writeComposeInspectOutput(cmd, output)
	case agentcomposev2.ResourceKind_RESOURCE_KIND_IMAGE:
		return runComposeImageInspectCommand(cmd, cli, target.GetId())
	case agentcomposev2.ResourceKind_RESOURCE_KIND_CACHE:
		return runComposeCacheInspectCommand(cmd, cli, target.GetId())
	default:
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("unsupported resolved resource type %s", target.GetKind().String())}
	}
}

func describeResourceTarget(target *agentcomposev2.ResourceTarget) string {
	if target == nil {
		return "unknown"
	}
	kind := strings.TrimPrefix(strings.ToLower(target.GetKind().String()), "resource_kind_")
	value := firstNonEmptyString(target.GetAgentName(), target.GetShortId(), shortOpaqueID(target.GetId()), "-")
	project := firstNonEmptyString(target.GetProjectName(), shortOpaqueID(target.GetProjectId()))
	if project != "" && target.GetKind() != agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT {
		return fmt.Sprintf("%s %s (project %s)", kind, value, project)
	}
	return fmt.Sprintf("%s %s", kind, value)
}

func writeComposeInspectOutput(cmd *cobra.Command, output any) error {
	if flag := cmd.Flags().Lookup("omit-scripts"); flag != nil && flag.Value.String() == "true" {
		var err error
		output, err = omitInspectScripts(output)
		if err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
}
