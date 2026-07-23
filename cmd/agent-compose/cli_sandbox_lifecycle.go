package main

import (
	"agent-compose/pkg/identity"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

type composeSandboxActionOutput struct {
	Results []composeSandboxActionResult `json:"results"`
}

type composeSandboxActionResult struct {
	SandboxID string `json:"sandbox_id"`
	Status    string `json:"status"`
}

type composeSandboxRemoveOptions struct {
	Force bool
}

func sandboxActionArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("requires at least 1 sandbox")}
	}
	return nil
}

func runComposeSandboxActionCommand(cmd *cobra.Command, cli cliOptions, action, status string, sandboxes []string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	sandboxes, err = resolveComposeSandboxRefsForCommand(cmd.Context(), cli, clients, sandboxes)
	if err != nil {
		return err
	}
	output := composeSandboxActionOutput{
		Results: make([]composeSandboxActionResult, 0, len(sandboxes)),
	}
	for _, sandbox := range sandboxes {
		sandbox = strings.TrimSpace(sandbox)
		if sandbox == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s requires non-empty sandbox", action)}
		}
		switch action {
		case "stop":
			_, err = clients.sandbox.StopSandbox(cmd.Context(), connect.NewRequest(&agentcomposev2.StopSandboxRequest{SandboxId: sandbox}))
		case "resume":
			_, err = clients.sandbox.ResumeSandbox(cmd.Context(), connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: sandbox}))
		default:
			return fmt.Errorf("unsupported sandbox action %q", action)
		}
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("%s sandbox %s: %w", action, sandbox, err))
		}
		output.Results = append(output.Results, composeSandboxActionResult{
			SandboxID: sandbox,
			Status:    status,
		})
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	for _, result := range output.Results {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s sandbox %s\n", result.Status, result.SandboxID); err != nil {
			return err
		}
	}
	return nil
}

func runComposeSandboxRemoveCommand(cmd *cobra.Command, cli cliOptions, options composeSandboxRemoveOptions, sandboxes []string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	sandboxes, err = resolveComposeSandboxRefsForCommand(cmd.Context(), cli, clients, sandboxes)
	if err != nil {
		return err
	}
	output := composeSandboxActionOutput{
		Results: make([]composeSandboxActionResult, 0, len(sandboxes)),
	}
	for _, sandbox := range sandboxes {
		sandbox = strings.TrimSpace(sandbox)
		if sandbox == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("rm requires non-empty sandbox")}
		}
		if err := removeSandbox(cmd.Context(), clients.sandbox, sandbox, options.Force); err != nil {
			return commandExitErrorForConnect(fmt.Errorf("rm sandbox %s: %w", sandbox, err))
		}
		output.Results = append(output.Results, composeSandboxActionResult{
			SandboxID: sandbox,
			Status:    "removed",
		})
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	for _, result := range output.Results {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s sandbox %s\n", result.Status, result.SandboxID); err != nil {
			return err
		}
	}
	return nil
}

func removeSandbox(ctx context.Context, client agentcomposev2connect.SandboxServiceClient, sandboxID string, force bool) error {
	_, err := client.RemoveSandbox(ctx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{
		SandboxId: sandboxID,
		Force:     force,
	}))
	return err
}

func composeSandboxInspectOutputFor(ctx context.Context, clients cliServiceClients, sandbox string) (composeSandboxOutput, error) {
	response, err := clients.sandbox.GetSandbox(ctx, connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandbox}))
	if err != nil {
		return composeSandboxOutput{}, err
	}
	return composeSandboxOutputFromSummary(response.Msg.GetSandbox()), nil
}

type composeSandboxOutput struct {
	SandboxID            string                             `json:"sandbox_id"`
	SandboxShortID       string                             `json:"sandbox_short_id,omitempty"`
	Title                string                             `json:"title,omitempty"`
	Driver               string                             `json:"driver,omitempty"`
	VMStatus             string                             `json:"vm_status,omitempty"`
	WorkspacePath        string                             `json:"workspace_path,omitempty"`
	ProxyPath            string                             `json:"proxy_path,omitempty"`
	GuestImage           string                             `json:"guest_image,omitempty"`
	TriggerSource        string                             `json:"trigger_source,omitempty"`
	CreatedAt            string                             `json:"created_at,omitempty"`
	UpdatedAt            string                             `json:"updated_at,omitempty"`
	CellCount            uint32                             `json:"cell_count"`
	EventCount           uint32                             `json:"event_count"`
	Tags                 map[string]string                  `json:"tags,omitempty"`
	WorkspaceReclamation *composeWorkspaceReclamationOutput `json:"workspace_reclamation,omitempty"`
}

func listAllSandboxes(ctx context.Context, client agentcomposev2connect.SandboxServiceClient) ([]*agentcomposev2.Sandbox, error) {
	return listFilteredSandboxes(ctx, client, "", nil)
}

func listFilteredSandboxes(ctx context.Context, client agentcomposev2connect.SandboxServiceClient, projectID string, statuses []string) ([]*agentcomposev2.Sandbox, error) {
	var result []*agentcomposev2.Sandbox
	var cursor string
	const limit uint32 = 100
	for {
		resp, err := client.ListSandboxes(ctx, connect.NewRequest(&agentcomposev2.ListSandboxesRequest{
			Cursor:    cursor,
			Limit:     limit,
			ProjectId: strings.TrimSpace(projectID),
			Status:    append([]string(nil), statuses...),
		}))
		if err != nil {
			return nil, err
		}
		result = append(result, resp.Msg.GetSandboxes()...)
		next := resp.Msg.GetNextCursor()
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return result, nil
}

func latestRunsBySandbox(runs []*agentcomposev2.RunSummary) map[string]*agentcomposev2.RunSummary {
	result := map[string]*agentcomposev2.RunSummary{}
	for _, run := range runs {
		sandboxID := strings.TrimSpace(run.GetSandboxId())
		if sandboxID == "" {
			continue
		}
		if current := result[sandboxID]; current == nil || runSortTime(run) > runSortTime(current) {
			result[sandboxID] = run
		}
	}
	return result
}

func runSummarySandboxID(run *agentcomposev2.RunSummary) string {
	if run == nil {
		return ""
	}
	return strings.TrimSpace(run.GetSandboxId())
}

func firstRunningSandboxOutput(ctx context.Context, clients cliServiceClients, projectID, agentName string) (*composeSandboxOutput, error) {
	resp, err := clients.run.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: projectID,
		AgentName: agentName,
		Limit:     20,
	}))
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, run := range resp.Msg.GetRuns() {
		sandboxID := strings.TrimSpace(run.GetSandboxId())
		if sandboxID == "" {
			continue
		}
		if _, ok := seen[sandboxID]; ok {
			continue
		}
		seen[sandboxID] = struct{}{}
		session, err := clients.sandbox.GetSandbox(ctx, connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxID}))
		if err != nil {
			continue
		}
		summary := session.Msg.GetSandbox()
		if strings.EqualFold(summary.GetStatus(), "running") {
			output := composeSandboxOutputFromSummary(summary)
			return &output, nil
		}
	}
	return nil, nil
}

func composeSandboxOutputFromSummary(summary *agentcomposev2.Sandbox) composeSandboxOutput {
	tags := make(map[string]string, len(summary.GetTags()))
	for _, tag := range summary.GetTags() {
		name := strings.TrimSpace(tag.GetName())
		if name == "" {
			continue
		}
		tags[name] = tag.GetValue()
	}
	if len(tags) == 0 {
		tags = nil
	}
	result := composeSandboxOutput{
		SandboxID:      displayOpaqueID(summary.GetSandboxId()),
		SandboxShortID: identity.ShortID(summary.GetSandboxId()),
		Title:          summary.GetTitle(),
		Driver:         summary.GetDriver(),
		VMStatus:       strings.ToLower(strings.TrimSpace(summary.GetStatus())),
		WorkspacePath:  summary.GetWorkspacePath(),
		ProxyPath:      summary.GetProxyPath(),
		GuestImage:     summary.GetImage(),
		TriggerSource:  summary.GetTriggerSource(),
		CreatedAt:      formatProtoTimestamp(summary.GetCreatedAt()),
		UpdatedAt:      formatProtoTimestamp(summary.GetUpdatedAt()),
		CellCount:      summary.GetCellCount(),
		EventCount:     summary.GetEventCount(),
		Tags:           tags,
	}
	if summary.GetWorkspaceReclamationState() != "" {
		result.WorkspaceReclamation = &composeWorkspaceReclamationOutput{
			State: summary.GetWorkspaceReclamationState(), StartedAt: formatProtoTimestamp(summary.GetWorkspaceReclamationStartedAt()),
			CompletedAt: formatProtoTimestamp(summary.GetWorkspaceReclamationCompletedAt()), LastError: summary.GetWorkspaceReclamationLastError(),
		}
	}
	return result
}

func resolveComposeSandboxRefsForCommand(ctx context.Context, cli cliOptions, clients cliServiceClients, refs []string) ([]string, error) {
	resolved := make([]string, 0, len(refs))
	var projectID string
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			resolved = append(resolved, ref)
			continue
		}
		if identity.IsID(ref) {
			resolved = append(resolved, ref)
			continue
		}
		if !shouldResolveComposeLogResourceRef(ref) {
			resolved = append(resolved, ref)
			continue
		}
		if projectID == "" {
			runtimeProject, err := resolveComposeRuntimeProject(ctx, clients.project, cli, "resolve sandbox", runtimeProjectIdentityOnly)
			if err != nil {
				return nil, err
			}
			projectID = runtimeProject.id()
		}
		sandboxID, err := resolveComposeSandboxRefWithProject(ctx, clients, projectID, ref)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, sandboxID)
	}
	return resolved, nil
}

func resolveComposeSandboxRefForCommand(ctx context.Context, cli cliOptions, clients cliServiceClients, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || identity.IsID(ref) {
		return ref, nil
	}
	if !shouldResolveComposeLogResourceRef(ref) {
		return ref, nil
	}
	runtimeProject, err := resolveComposeRuntimeProject(ctx, clients.project, cli, "resolve sandbox", runtimeProjectIdentityOnly)
	if err != nil {
		return "", err
	}
	return resolveComposeSandboxRefWithProject(ctx, clients, runtimeProject.id(), ref)
}

func resolveComposeSandboxRefWithProject(ctx context.Context, clients cliServiceClients, projectID, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox id is required")}
	}
	if identity.IsID(ref) {
		return ref, nil
	}
	if !shouldResolveComposeLogResourceRef(ref) {
		return ref, nil
	}
	project, err := clients.project.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID},
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return resolveComposeSandboxRefFromSessions(ctx, clients.sandbox, ref)
		}
		return "", commandExitErrorForConnect(fmt.Errorf("resolve sandbox %s: %w", ref, err))
	}
	return resolveComposeSandboxRefFromProject(ctx, clients, project.Msg.GetProject(), ref)
}

func resolveComposeSandboxRefFromProject(ctx context.Context, clients cliServiceClients, project *agentcomposev2.Project, ref string) (string, error) {
	psOutput, err := composePSOutputFromProject(ctx, clients, project, composePSOptions{All: true})
	if err != nil {
		return "", commandExitErrorForConnect(fmt.Errorf("resolve sandbox %s: %w", ref, err))
	}
	matches := map[string]struct{}{}
	for _, sandbox := range psOutput.Sandboxes {
		if resourceIDMatchesRef(sandbox.SandboxID, sandbox.SandboxShortID, ref) {
			matches[firstNonEmptyString(sandbox.RawID, sandbox.SandboxID)] = struct{}{}
		}
	}
	if len(matches) == 0 {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox %q not found in current project", ref)}
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for id := range matches {
			ids = append(ids, shortOpaqueID(id))
		}
		sort.Strings(ids)
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox ref %q is ambiguous in current project; matches: %s", ref, strings.Join(ids, ", "))}
	}
	for id := range matches {
		return id, nil
	}
	return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox %q not found in current project", ref)}
}

func resolveComposeSandboxIDRefFromRuns(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID, agentName, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox id is required")}
	}
	if identity.IsID(ref) {
		return ref, nil
	}
	if !shouldResolveComposeLogResourceRef(ref) {
		return ref, nil
	}
	runs, err := listLogRunRefCandidates(ctx, client, projectID, agentName)
	if err != nil {
		return "", commandExitErrorForConnect(fmt.Errorf("resolve sandbox %s: %w", ref, err))
	}
	matches := map[string]struct{}{}
	for _, run := range runs {
		sandboxID := runSummarySandboxID(run)
		if sandboxID == "" {
			continue
		}
		if resourceIDMatchesRef(sandboxID, shortOpaqueID(sandboxID), ref) {
			matches[sandboxID] = struct{}{}
		}
	}
	if len(matches) == 0 {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox %q not found in current project runs", ref)}
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for id := range matches {
			ids = append(ids, shortOpaqueID(id))
		}
		sort.Strings(ids)
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox ref %q is ambiguous in current project; matches: %s", ref, strings.Join(ids, ", "))}
	}
	for id := range matches {
		return id, nil
	}
	return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox %q not found in current project runs", ref)}
}
