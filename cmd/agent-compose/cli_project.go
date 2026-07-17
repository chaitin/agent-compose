package main

import (
	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
)

type composeListProjectsOptions struct {
	Verbose bool
	Limit   uint32
	Offset  uint32
}

func listProjects(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, options composeListProjectsOptions) (composeProjectListOutput, error) {
	if options.Limit > 0 || options.Offset > 0 {
		return listProjectsPage(ctx, client, options.Offset, options.Limit)
	}
	return listAllProjects(ctx, client)
}

func listProjectsPage(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, offset, limit uint32) (composeProjectListOutput, error) {
	resp, err := client.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{
		Offset: offset,
		Limit:  limit,
	}))
	if err != nil {
		return composeProjectListOutput{}, err
	}
	msg := resp.Msg
	output := composeProjectListOutput{
		Projects:   make([]composeProjectListItem, 0, len(msg.GetProjects())),
		TotalCount: msg.GetTotalCount(),
		HasMore:    msg.GetHasMore(),
		NextOffset: msg.GetNextOffset(),
	}
	for _, project := range msg.GetProjects() {
		output.Projects = append(output.Projects, composeProjectListItemFromSummary(project))
	}
	return output, nil
}

func listAllProjects(ctx context.Context, client agentcomposev2connect.ProjectServiceClient) (composeProjectListOutput, error) {
	const pageSize uint32 = 200
	var output composeProjectListOutput
	for {
		offset := output.NextOffset
		resp, err := client.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{
			Offset: offset,
			Limit:  pageSize,
		}))
		if err != nil {
			return composeProjectListOutput{}, err
		}
		msg := resp.Msg
		output.TotalCount = msg.GetTotalCount()
		output.HasMore = msg.GetHasMore()
		output.NextOffset = msg.GetNextOffset()
		for _, project := range msg.GetProjects() {
			output.Projects = append(output.Projects, composeProjectListItemFromSummary(project))
		}
		if !msg.GetHasMore() {
			break
		}
		if msg.GetNextOffset() == offset {
			return composeProjectListOutput{}, fmt.Errorf("project list pagination did not advance")
		}
	}
	output.HasMore = false
	return output, nil
}

func resolveComposeAgentNameFromProject(project *agentcomposev2.Project, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent ref is required")}
	}
	if project == nil {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent %q not found in current project", ref)}
	}
	candidates := make([]composeAgentRefCandidate, 0, len(project.GetAgents()))
	for _, agent := range project.GetAgents() {
		name := strings.TrimSpace(agent.GetAgentName())
		if name == "" {
			continue
		}
		id := strings.TrimSpace(agent.GetManagedAgentId())
		candidates = append(candidates, composeAgentRefCandidate{Name: name, ID: id, ShortID: shortOpaqueID(id)})
	}
	return resolveComposeAgentNameFromCandidates(ref, candidates)
}

func resolveComposeProject(cli cliOptions) (string, *compose.NormalizedProjectSpec, string, error) {
	composePath, normalized, err := loadNormalizedCompose(cli)
	if err != nil {
		return "", nil, "", err
	}
	projectID, err := domain.StableProjectID(normalized.Name, domain.NormalizeProjectSourcePath(composePath))
	if err != nil {
		return "", nil, "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s: resolve project %s: %w", composePath, normalized.Name, err)}
	}
	return composePath, normalized, projectID, nil
}

type composeUpOutput struct {
	Project   composeUpProjectOutput  `json:"project"`
	Revision  composeUpRevisionOutput `json:"revision"`
	Applied   bool                    `json:"applied"`
	Unchanged bool                    `json:"unchanged"`
	Changes   []composeUpChangeOutput `json:"changes"`
}

type composeDownOutput struct {
	Project            composeUpProjectOutput  `json:"project"`
	Status             string                  `json:"status"`
	FailedSandboxStops uint32                  `json:"failed_sandbox_stops"`
	Changes            []composeUpChangeOutput `json:"changes"`
}

type composeProjectListOutput struct {
	Projects   []composeProjectListItem `json:"projects"`
	TotalCount uint32                   `json:"total_count"`
	HasMore    bool                     `json:"has_more"`
	NextOffset uint32                   `json:"next_offset"`
}

type composeProjectListItem struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	ShortID         string  `json:"short_id"`
	ConfigFile      string  `json:"config_file"`
	ProjectDir      string  `json:"project_dir,omitempty"`
	Revision        uint64  `json:"revision"`
	SpecHash        string  `json:"spec_hash,omitempty"`
	AgentCount      uint32  `json:"agent_count"`
	SchedulerCount  uint32  `json:"scheduler_count"`
	ServiceCount    *uint32 `json:"service_count"`
	RunningRunCount uint32  `json:"running_run_count"`
	LatestRunID     string  `json:"latest_run_id,omitempty"`
	CreatedAt       string  `json:"created_at,omitempty"`
	UpdatedAt       string  `json:"updated_at,omitempty"`
	RemovedAt       string  `json:"removed_at,omitempty"`
}

type composeUpProjectOutput struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ShortID         string `json:"short_id"`
	SourcePath      string `json:"source_path"`
	CurrentRevision uint64 `json:"current_revision"`
	SpecHash        string `json:"spec_hash"`
	AgentCount      uint32 `json:"agent_count"`
	SchedulerCount  uint32 `json:"scheduler_count"`
}

type composeUpRevisionOutput struct {
	Revision uint64 `json:"revision"`
	SpecHash string `json:"spec_hash"`
}

type composeUpChangeOutput struct {
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ID           string `json:"id"`
	ShortID      string `json:"short_id,omitempty"`
	Name         string `json:"name"`
	Message      string `json:"message,omitempty"`
}

type composeProjectOutput struct {
	Project    composeUpProjectOutput          `json:"project"`
	Agents     []composeProjectAgentOutput     `json:"agents"`
	Schedulers []composeProjectSchedulerOutput `json:"schedulers"`
}

type composeProjectAgentOutput struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ShortID          string `json:"short_id"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	Image            string `json:"image,omitempty"`
	Driver           string `json:"driver,omitempty"`
	SchedulerEnabled bool   `json:"scheduler_enabled"`
}

type composeAgentInspectOutput struct {
	Project          composeUpProjectOutput          `json:"project"`
	Agent            composeProjectAgentOutput       `json:"agent"`
	Schedulers       []composeProjectSchedulerOutput `json:"schedulers"`
	LatestRun        *composeRunOutput               `json:"latest_run,omitempty"`
	RunningSandboxes []composeSandboxOutput          `json:"running_sandboxes,omitempty"`
}

func composeProjectListItemFromSummary(summary *agentcomposev2.ProjectSummary) composeProjectListItem {
	configFile := summary.GetSourcePath()
	projectDir := ""
	if configFile != "" {
		projectDir = filepath.Dir(configFile)
	}
	return composeProjectListItem{
		ID:              displayOpaqueID(summary.GetProjectId()),
		Name:            summary.GetName(),
		ShortID:         shortOpaqueID(summary.GetProjectId()),
		ConfigFile:      configFile,
		ProjectDir:      projectDir,
		Revision:        summary.GetCurrentRevision(),
		SpecHash:        summary.GetSpecHash(),
		AgentCount:      summary.GetAgentCount(),
		SchedulerCount:  summary.GetSchedulerCount(),
		ServiceCount:    nil,
		RunningRunCount: summary.GetRunningRunCount(),
		LatestRunID:     displayOpaqueID(summary.GetLatestRunId()),
		CreatedAt:       summary.GetCreatedAt(),
		UpdatedAt:       summary.GetUpdatedAt(),
		RemovedAt:       summary.GetRemovedAt(),
	}
}

func composeUpOutputFromResponse(resp *agentcomposev2.ApplyProjectResponse) composeUpOutput {
	summary := resp.GetProject().GetSummary()
	revision := resp.GetRevision()
	changes := make([]composeUpChangeOutput, 0, len(resp.GetChanges()))
	for _, change := range resp.GetChanges() {
		changes = append(changes, composeUpChangeOutput{
			Action:       projectChangeActionText(change.GetAction()),
			ResourceType: change.GetResourceType(),
			ID:           displayOpaqueID(change.GetResourceId()),
			ShortID:      shortOpaqueID(change.GetResourceId()),
			Name:         change.GetName(),
			Message:      change.GetMessage(),
		})
	}
	return composeUpOutput{
		Project: composeUpProjectOutput{
			ID:              displayOpaqueID(summary.GetProjectId()),
			Name:            summary.GetName(),
			ShortID:         shortOpaqueID(summary.GetProjectId()),
			SourcePath:      summary.GetSourcePath(),
			CurrentRevision: summary.GetCurrentRevision(),
			SpecHash:        summary.GetSpecHash(),
			AgentCount:      summary.GetAgentCount(),
			SchedulerCount:  summary.GetSchedulerCount(),
		},
		Revision: composeUpRevisionOutput{
			Revision: revision.GetRevision(),
			SpecHash: revision.GetSpecHash(),
		},
		Applied:   resp.GetApplied(),
		Unchanged: resp.GetUnchanged(),
		Changes:   changes,
	}
}

func composeDownOutputFromResponse(resp *agentcomposev2.RemoveProjectResponse) composeDownOutput {
	changes := composeChangeOutputs(resp.GetChanges())
	failedSandboxStops := countProjectDownFailedSandboxStops(resp.GetChanges())
	status := "down"
	if len(changes) == 0 {
		status = "unchanged"
	}
	if failedSandboxStops > 0 {
		status = "partial-failure"
	}
	return composeDownOutput{
		Project:            composeProjectSummaryOutput(resp.GetProject().GetSummary()),
		Status:             status,
		FailedSandboxStops: uint32(failedSandboxStops),
		Changes:            changes,
	}
}

func writeProjectListText(out io.Writer, projects []composeProjectListItem, verbose bool) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if verbose {
		if _, err := fmt.Fprintln(tw, "ID\tNAME\tCONFIG FILE\tREVISION\tAGENTS\tSCHEDULERS\tSERVICES\tPROJECT DIR\tSPEC HASH\tUPDATED\tSTATUS"); err != nil {
			return err
		}
		for _, project := range projects {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\n",
				firstNonEmptyString(project.ID, "-"),
				project.Name,
				firstNonEmptyString(project.ConfigFile, "-"),
				project.Revision,
				project.AgentCount,
				project.SchedulerCount,
				projectServiceCountText(project.ServiceCount),
				firstNonEmptyString(project.ProjectDir, "-"),
				firstNonEmptyString(project.SpecHash, "-"),
				firstNonEmptyString(project.UpdatedAt, "-"),
				projectListStatus(project),
			); err != nil {
				return err
			}
		}
		return tw.Flush()
	}
	if _, err := fmt.Fprintln(tw, "ID\tNAME\tCONFIG FILE\tAGENTS\tSCHEDULERS"); err != nil {
		return err
	}
	for _, project := range projects {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\n",
			firstNonEmptyString(project.ShortID, shortOpaqueID(project.ID), "-"),
			project.Name,
			firstNonEmptyString(project.ConfigFile, "-"),
			project.AgentCount,
			project.SchedulerCount,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func projectServiceCountText(count *uint32) string {
	if count == nil {
		return "-"
	}
	return strconv.FormatUint(uint64(*count), 10)
}

func projectListStatus(project composeProjectListItem) string {
	if project.RemovedAt != "" {
		return "removed"
	}
	return "active"
}

func writeComposeUpText(out io.Writer, changes []composeDisplayChangeOutput) error {
	return writeComposeChangeTable(out, changes)
}

func writeComposeDownText(out io.Writer, changes []composeDisplayChangeOutput) error {
	return writeComposeChangeTable(out, changes)
}

func composeDisplayChangesFromProjectChanges(changes []*agentcomposev2.ProjectChange, spec *compose.NormalizedProjectSpec, projectIDs ...string) []composeDisplayChangeOutput {
	builder := newComposeDisplayChangeBuilder()
	if len(projectIDs) > 0 {
		builder.projectID = projectIDs[0]
	}
	for _, change := range changes {
		builder.addProjectChange(change, spec)
	}
	return builder.items
}

func composeDownDisplayChanges(resp *agentcomposev2.RemoveProjectResponse, spec *compose.NormalizedProjectSpec) []composeDisplayChangeOutput {
	builder := newComposeDisplayChangeBuilder()
	project := resp.GetProject()
	summary := project.GetSummary()
	builder.projectID = summary.GetProjectId()
	removed := false
	for _, change := range resp.GetChanges() {
		if change.GetResourceType() == "project" && change.GetAction() == agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED {
			removed = true
		}
		builder.addProjectChange(change, spec)
	}
	if summary.GetRemovedAt() != "" {
		removed = true
	}
	if len(builder.items) == 0 && summary.GetProjectId() != "" {
		builder.add(composeDisplayChangeOutput{
			Action:       "unchanged",
			ResourceType: "project",
			ID:           shortOpaqueID(summary.GetProjectId()),
			Name:         summary.GetName(),
		})
	}
	if removed {
		for _, agent := range project.GetAgents() {
			builder.add(composeDisplayChangeOutput{
				Action:       "removed",
				ResourceType: "agent",
				ID:           shortOpaqueID(agent.GetManagedAgentId()),
				Name:         agent.GetAgentName(),
			})
		}
		for _, scheduler := range project.GetSchedulers() {
			builder.addTriggerChanges("removed", shortOpaqueID(scheduler.GetSchedulerId()), scheduler.GetAgentName(), "", spec)
		}
	}
	return builder.items
}

func (b *composeDisplayChangeBuilder) addProjectChange(change *agentcomposev2.ProjectChange, spec *compose.NormalizedProjectSpec) {
	resourceType := composeDisplayResourceType(change.GetResourceType())
	if resourceType == "trigger" {
		b.addTriggerChanges(
			projectChangeActionText(change.GetAction()),
			shortOpaqueID(change.GetResourceId()),
			change.GetName(),
			change.GetMessage(),
			spec,
		)
		return
	}
	b.add(composeDisplayChangeOutput{
		Action:       projectChangeActionText(change.GetAction()),
		ResourceType: resourceType,
		ID:           shortOpaqueID(change.GetResourceId()),
		Name:         change.GetName(),
		Message:      change.GetMessage(),
	})
}

func projectChangeActionRank(action string) int {
	switch action {
	case "removed":
		return 4
	case "updated":
		return 3
	case "created":
		return 2
	case "unchanged":
		return 1
	default:
		return 0
	}
}

func projectChangeActionText(action agentcomposev2.ProjectChangeAction) string {
	switch action {
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED:
		return "created"
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED:
		return "updated"
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED:
		return "removed"
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED:
		return "unchanged"
	default:
		return "unspecified"
	}
}

func formatProjectValidationIssues(issues []*agentcomposev2.ProjectValidationIssue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.GetPath() == "" {
			parts = append(parts, issue.GetMessage())
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", issue.GetPath(), issue.GetMessage()))
	}
	return strings.Join(parts, "; ")
}

func composePSOutputFromProject(ctx context.Context, clients cliServiceClients, project *agentcomposev2.Project, options composePSOptions) (composePSOutput, error) {
	output := composePSOutput{Project: composeProjectSummaryOutput(project.GetSummary())}
	statusFilter, err := composePSStatusFilter(options)
	if err != nil {
		return composePSOutput{}, err
	}
	projectID := project.GetSummary().GetProjectId()
	runs, err := listProjectRuns(ctx, clients.run, projectID)
	if err != nil {
		return composePSOutput{}, err
	}
	runBySandbox := latestRunsBySandbox(runs)
	sessions, err := listAllSandboxes(ctx, clients.sandbox)
	if err != nil {
		return composePSOutput{}, err
	}
	schedulerRunBySandbox, err := latestSchedulerRunsBySandbox(ctx, clients.project, project, sessions)
	if err != nil {
		return composePSOutput{}, err
	}
	for _, session := range sessions {
		if !composePSSessionBelongsToProject(session, project, runBySandbox) {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(session.GetStatus()))
		if status == "" {
			status = "unknown"
		}
		if statusFilter != nil && !statusFilter[status] {
			continue
		}
		run := runBySandbox[session.GetSandboxId()]
		schedulerRun := schedulerRunBySandbox[session.GetSandboxId()]
		tags := sessionTagsMap(session.GetTags())
		agent := firstNonEmptyString(run.GetAgentName(), tags["agent"])
		runID := firstNonEmptyString(run.GetRunId(), tags["run_id"])
		if schedulerRunIsNewer(schedulerRun, run) {
			agent = firstNonEmptyString(schedulerRun.AgentName, agent)
			runID = schedulerRun.RunID
		}
		output.Sandboxes = append(output.Sandboxes, composePSSandboxOutput{
			SandboxID:      displayOpaqueID(session.GetSandboxId()),
			RawID:          session.GetSandboxId(),
			SandboxShortID: shortOpaqueID(session.GetSandboxId()),
			Agent:          agent,
			Status:         status,
			RunID:          displayOpaqueID(runID),
			RunShortID:     shortOpaqueID(runID),
			CreatedAt:      formatProtoTimestamp(session.GetCreatedAt()),
			UpdatedAt:      formatProtoTimestamp(session.GetUpdatedAt()),
			Driver:         session.GetDriver(),
			Image:          session.GetImage(),
			Workspace:      session.GetWorkspacePath(),
		})
	}
	return output, nil
}

func composeProjectOutputFromProject(project *agentcomposev2.Project) composeProjectOutput {
	output := composeProjectOutput{Project: composeProjectSummaryOutput(project.GetSummary())}
	for _, agent := range project.GetAgents() {
		output.Agents = append(output.Agents, composeProjectAgentOutputFromProto(agent))
	}
	for _, scheduler := range project.GetSchedulers() {
		output.Schedulers = append(output.Schedulers, composeProjectSchedulerOutputFromProto(scheduler))
	}
	return output
}

func composeAgentInspectOutputFor(ctx context.Context, clients cliServiceClients, project *agentcomposev2.Project, agentName string) (composeAgentInspectOutput, error) {
	var found *agentcomposev2.ProjectAgent
	for _, agent := range project.GetAgents() {
		if agent.GetAgentName() == agentName {
			found = agent
			break
		}
	}
	if found == nil {
		return composeAgentInspectOutput{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent %s not found in project %s", agentName, project.GetSummary().GetName())}
	}
	output := composeAgentInspectOutput{
		Project: composeProjectSummaryOutput(project.GetSummary()),
		Agent:   composeProjectAgentOutputFromProto(found),
	}
	for _, scheduler := range project.GetSchedulers() {
		if scheduler.GetAgentName() == agentName {
			output.Schedulers = append(output.Schedulers, composeProjectSchedulerOutputFromProto(scheduler))
		}
	}
	if latest, err := latestRunOutput(ctx, clients.run, project.GetSummary().GetProjectId(), agentName); err != nil {
		return composeAgentInspectOutput{}, commandExitErrorForConnect(fmt.Errorf("list latest run for agent %s: %w", agentName, err))
	} else {
		output.LatestRun = latest
	}
	if session, err := firstRunningSandboxOutput(ctx, clients, project.GetSummary().GetProjectId(), agentName); err != nil {
		return composeAgentInspectOutput{}, commandExitErrorForConnect(fmt.Errorf("list running sandbox for agent %s: %w", agentName, err))
	} else if session != nil {
		output.RunningSandboxes = append(output.RunningSandboxes, *session)
	}
	return output, nil
}

func composeProjectSummaryOutput(summary *agentcomposev2.ProjectSummary) composeUpProjectOutput {
	return composeUpProjectOutput{
		ID:              displayOpaqueID(summary.GetProjectId()),
		Name:            summary.GetName(),
		ShortID:         shortOpaqueID(summary.GetProjectId()),
		SourcePath:      summary.GetSourcePath(),
		CurrentRevision: summary.GetCurrentRevision(),
		SpecHash:        summary.GetSpecHash(),
		AgentCount:      summary.GetAgentCount(),
		SchedulerCount:  summary.GetSchedulerCount(),
	}
}

func composeProjectAgentOutputFromProto(agent *agentcomposev2.ProjectAgent) composeProjectAgentOutput {
	return composeProjectAgentOutput{
		ID:               displayOpaqueID(agent.GetManagedAgentId()),
		Name:             agent.GetAgentName(),
		ShortID:          shortOpaqueID(agent.GetManagedAgentId()),
		Provider:         agent.GetProvider(),
		Model:            agent.GetModel(),
		Image:            agent.GetImage(),
		Driver:           agent.GetDriver(),
		SchedulerEnabled: agent.GetSchedulerEnabled(),
	}
}

func commandExitErrorForComposeProject(err error, command, projectName, composePath string) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return commandExitError{
			Code: exitCodeUsage,
			Err: fmt.Errorf(
				"project %q is not running: it has not been started on this daemon or was removed by `agent-compose down`.\nTo start it, run `agent-compose up --file %s` before `agent-compose %s`",
				projectName,
				composePath,
				command,
			),
		}
	}
	return commandExitErrorForConnect(err)
}
