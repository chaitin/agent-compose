package main

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/chaitin/agent-compose/pkg/identity"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func runComposeProjectInspectCommand(cmd *cobra.Command, cli cliOptions, clients cliServiceClients, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		runtimeProject, err := resolveComposeRuntimeProject(cmd.Context(), clients.project, cli, "inspect project")
		if err != nil {
			return err
		}
		return writeComposeProjectInspectOutput(cmd, runtimeProject.project)
	}

	project, err := resolveExplicitInspectProject(cmd, clients, ref)
	if err != nil {
		return err
	}
	return writeComposeProjectInspectOutput(cmd, project)
}

func resolveExplicitInspectProject(cmd *cobra.Command, clients cliServiceClients, ref string) (*agentcomposev2.Project, error) {
	project, err := getInspectProject(cmd.Context(), clients.project, &agentcomposev2.ProjectRef{
		Selector: &agentcomposev2.ProjectRef_Name{Name: ref},
	})
	if err == nil {
		return project, nil
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		return nil, explicitInspectProjectError(ref, err)
	}

	if identity.IsID(ref) {
		project, err = getInspectProject(cmd.Context(), clients.project, &agentcomposev2.ProjectRef{
			Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: ref},
		})
		if err != nil {
			return nil, explicitInspectProjectError(ref, err)
		}
		return project, nil
	}
	if !identity.IsIDPrefix(ref) {
		return nil, explicitInspectProjectError(ref, err)
	}

	target, err := resolveCLIResourceID(cmd, clients.resource, ref, []agentcomposev2.ResourceKind{
		agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT,
	})
	if err != nil {
		return nil, err
	}
	project, err = getInspectProject(cmd.Context(), clients.project, &agentcomposev2.ProjectRef{
		Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: target.GetId()},
	})
	if err != nil {
		return nil, explicitInspectProjectError(ref, err)
	}
	return project, nil
}

func getInspectProject(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, ref *agentcomposev2.ProjectRef) (*agentcomposev2.Project, error) {
	response, err := client.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project:     ref,
		IncludeSpec: true,
	}))
	if err != nil {
		return nil, err
	}
	project := response.Msg.GetProject()
	if project == nil || strings.TrimSpace(project.GetSummary().GetProjectId()) == "" {
		return nil, fmt.Errorf("get inspect project: response did not include a project")
	}
	return project, nil
}

func explicitInspectProjectError(ref string, err error) error {
	return commandExitErrorForComposeProject(
		fmt.Errorf("inspect project %s: %w", ref, err),
		"inspect project",
		ref,
		"",
	)
}
