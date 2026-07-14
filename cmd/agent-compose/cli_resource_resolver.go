package main

import (
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func isComposeInspectResourceKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "project", "agent", "run", "sandbox", "session", "image", "cache", "volume":
		return true
	default:
		return false
	}
}

func runComposeResolvedInspectCommand(cmd *cobra.Command, cli cliOptions, ref string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	ref = strings.TrimSpace(ref)
	resource, err := resolveCLIResourceRef(cmd, clients.resource, ref, nil, fmt.Sprintf("use inspect <type> %s to select one", ref))
	if err != nil {
		return err
	}
	return runComposeResolvedResourceInspectCommand(cmd, cli, clients, resource)
}

func resolveCLIResourceRef(cmd *cobra.Command, client agentcomposev2connect.ResourceServiceClient, ref string, kinds []agentcomposev2.ResourceKind, ambiguityHint string) (*agentcomposev2.ResolvedResource, error) {
	ref = strings.TrimSpace(ref)
	response, err := client.ResolveResource(cmd.Context(), connect.NewRequest(&agentcomposev2.ResolveResourceRequest{Ref: ref, Kinds: kinds}))
	if err != nil {
		return nil, commandExitErrorForConnect(fmt.Errorf("resolve resource %s: %w", ref, err))
	}
	for _, warning := range response.Msg.GetWarnings() {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", warning); err != nil {
			return nil, err
		}
	}
	resolved := response.Msg.GetResources()
	if len(resolved) == 0 {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resource %q not found", ref)}
	}
	if len(resolved) > 1 {
		matches := make([]string, 0, len(resolved))
		for _, resource := range resolved {
			matches = append(matches, composeResolvedResourceDescription(resource))
		}
		message := fmt.Sprintf("resource ref %q is ambiguous; matches: %s", ref, strings.Join(matches, ", "))
		if ambiguityHint = strings.TrimSpace(ambiguityHint); ambiguityHint != "" {
			message += "; " + ambiguityHint
		}
		return nil, commandExitError{Code: exitCodeUsage, Err: errors.New(message)}
	}
	return resolved[0], nil
}

func runComposeResolvedResourceInspectCommand(cmd *cobra.Command, cli cliOptions, clients cliServiceClients, resource *agentcomposev2.ResolvedResource) error {
	if resource == nil {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolved resource is empty")}
	}
	inspectRef := firstNonEmptyString(resource.GetInspectRef(), resource.GetId(), resource.GetName())
	switch resource.GetKind() {
	case agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT:
		project, err := clients.project.GetProject(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{
			Project:     &agentcomposev2.ProjectRef{ProjectId: resource.GetId()},
			IncludeSpec: true,
		}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect project %s: %w", inspectRef, err))
		}
		return writeComposeInspectOutput(cmd, composeProjectOutputFromProject(project.Msg.GetProject()))
	case agentcomposev2.ResourceKind_RESOURCE_KIND_AGENT:
		project, err := clients.project.GetProject(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{
			Project:     &agentcomposev2.ProjectRef{ProjectId: resource.GetProjectId()},
			IncludeSpec: true,
		}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect agent %s in project %s: %w", resource.GetName(), resource.GetProjectName(), err))
		}
		agent, err := composeAgentInspectOutputFor(cmd.Context(), clients, project.Msg.GetProject(), resource.GetName())
		if err != nil {
			return err
		}
		return writeComposeInspectOutput(cmd, agent)
	case agentcomposev2.ResourceKind_RESOURCE_KIND_RUN:
		run, err := getRunDetail(cmd.Context(), clients.run, resource.GetProjectId(), resource.GetId())
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect run %s: %w", inspectRef, err))
		}
		return writeComposeInspectOutput(cmd, composeRunOutputFromDetail(run.Msg.GetRun()))
	case agentcomposev2.ResourceKind_RESOURCE_KIND_SANDBOX:
		output, err := composeSandboxInspectOutputFor(cmd.Context(), clients, resource.GetId())
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect sandbox %s: %w", inspectRef, err))
		}
		return writeComposeInspectOutput(cmd, output)
	case agentcomposev2.ResourceKind_RESOURCE_KIND_IMAGE:
		return runComposeImageInspectCommand(cmd, cli, inspectRef)
	case agentcomposev2.ResourceKind_RESOURCE_KIND_CACHE:
		return runComposeCacheInspectCommand(cmd, cli, inspectRef)
	case agentcomposev2.ResourceKind_RESOURCE_KIND_VOLUME:
		return runComposeVolumeInspectCommand(cmd, cli, inspectRef)
	default:
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("unsupported resolved resource type %s", resource.GetKind().String())}
	}
}

func composeResolvedResourceDescription(resource *agentcomposev2.ResolvedResource) string {
	if resource == nil {
		return "unknown"
	}
	kind := strings.TrimPrefix(strings.ToLower(resource.GetKind().String()), "resource_kind_")
	target := firstNonEmptyString(resource.GetName(), resource.GetShortId(), shortOpaqueID(resource.GetId()), resource.GetInspectRef(), "-")
	project := firstNonEmptyString(resource.GetProjectName(), shortOpaqueID(resource.GetProjectId()))
	if project != "" && resource.GetKind() != agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT {
		return fmt.Sprintf("%s %s (project %s)", kind, target, project)
	}
	return fmt.Sprintf("%s %s", kind, target)
}
