package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

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
	FailedSessionStops uint32                  `json:"failed_session_stops"`
	Changes            []composeUpChangeOutput `json:"changes"`
}

type composeUpProjectOutput struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
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
	ResourceID   string `json:"resource_id"`
	Name         string `json:"name"`
	Message      string `json:"message,omitempty"`
}

type composeRunOutput struct {
	RunID        string `json:"run_id"`
	ProjectID    string `json:"project_id"`
	ProjectName  string `json:"project_name"`
	AgentName    string `json:"agent_name"`
	Source       string `json:"source"`
	Status       string `json:"status"`
	SessionID    string `json:"session_id"`
	ExitCode     int32  `json:"exit_code"`
	Error        string `json:"error,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
	DurationMs   int64  `json:"duration_ms,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	Output       string `json:"output,omitempty"`
	ResultJSON   string `json:"result_json,omitempty"`
	LogsPath     string `json:"logs_path,omitempty"`
	ArtifactsDir string `json:"artifacts_dir,omitempty"`
	CleanupError string `json:"cleanup_error,omitempty"`
	Driver       string `json:"driver,omitempty"`
	ImageRef     string `json:"image_ref,omitempty"`
}

type composeLogsOutput struct {
	Runs []composeRunOutput `json:"runs"`
}

type serviceClients struct {
	project agentcomposev2connect.ProjectServiceClient
	run     agentcomposev2connect.RunServiceClient
	exec    agentcomposev2connect.ExecServiceClient
	image   agentcomposev2connect.ImageServiceClient
	session agentcomposev1connect.SessionServiceClient
}

type composePSOutput struct {
	Project composeUpProjectOutput `json:"project"`
	Agents  []composePSAgentOutput `json:"agents"`
}

type composePSAgentOutput struct {
	AgentName         string                `json:"agent_name"`
	ManagedAgentID    string                `json:"managed_agent_id"`
	SchedulerEnabled  bool                  `json:"scheduler_enabled"`
	SchedulerID       string                `json:"scheduler_id,omitempty"`
	SchedulerTriggers uint32                `json:"scheduler_triggers"`
	LatestRun         *composeRunOutput     `json:"latest_run,omitempty"`
	RunningSession    *composeSessionOutput `json:"running_session,omitempty"`
	Driver            string                `json:"driver,omitempty"`
	Image             string                `json:"image,omitempty"`
}

type composeProjectOutput struct {
	Project    composeUpProjectOutput          `json:"project"`
	Agents     []composeProjectAgentOutput     `json:"agents"`
	Schedulers []composeProjectSchedulerOutput `json:"schedulers"`
}

type composeProjectAgentOutput struct {
	AgentName        string `json:"agent_name"`
	ManagedAgentID   string `json:"managed_agent_id"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	Image            string `json:"image,omitempty"`
	Driver           string `json:"driver,omitempty"`
	SchedulerEnabled bool   `json:"scheduler_enabled"`
}

type composeProjectSchedulerOutput struct {
	AgentName       string `json:"agent_name"`
	SchedulerID     string `json:"scheduler_id"`
	ManagedLoaderID string `json:"managed_loader_id"`
	Enabled         bool   `json:"enabled"`
	TriggerCount    uint32 `json:"trigger_count"`
}

type composeAgentInspectOutput struct {
	Project         composeUpProjectOutput          `json:"project"`
	Agent           composeProjectAgentOutput       `json:"agent"`
	Schedulers      []composeProjectSchedulerOutput `json:"schedulers"`
	LatestRun       *composeRunOutput               `json:"latest_run,omitempty"`
	RunningSessions []composeSessionOutput          `json:"running_sessions,omitempty"`
}

type composeSessionOutput struct {
	SessionID     string            `json:"session_id"`
	Title         string            `json:"title,omitempty"`
	Driver        string            `json:"driver,omitempty"`
	VMStatus      string            `json:"vm_status,omitempty"`
	WorkspacePath string            `json:"workspace_path,omitempty"`
	ProxyPath     string            `json:"proxy_path,omitempty"`
	GuestImage    string            `json:"guest_image,omitempty"`
	TriggerSource string            `json:"trigger_source,omitempty"`
	CreatedAt     string            `json:"created_at,omitempty"`
	UpdatedAt     string            `json:"updated_at,omitempty"`
	CellCount     uint32            `json:"cell_count"`
	EventCount    uint32            `json:"event_count"`
	Tags          map[string]string `json:"tags,omitempty"`
}

type composeExecOutput struct {
	ExecID    string   `json:"exec_id"`
	SessionID string   `json:"session_id"`
	RunID     string   `json:"run_id,omitempty"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	ExitCode  int32    `json:"exit_code"`
	Success   bool     `json:"success"`
	Stdout    string   `json:"stdout,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	Output    string   `json:"output,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type composeImageListOutput struct {
	Images      []composeImageOutput    `json:"images"`
	TotalCount  uint32                  `json:"total_count"`
	HasMore     bool                    `json:"has_more"`
	NextOffset  uint32                  `json:"next_offset"`
	StoreStatus composeImageStoreOutput `json:"store_status"`
}

type composeImageInspectOutput struct {
	Image       composeImageOutput      `json:"image"`
	StoreStatus composeImageStoreOutput `json:"store_status"`
}

type composeImagePullOutput struct {
	ImageRef    string                     `json:"image_ref"`
	ResolvedRef string                     `json:"resolved_ref,omitempty"`
	Status      string                     `json:"status"`
	Image       composeImageOutput         `json:"image"`
	Progress    []composeImageProgressItem `json:"progress,omitempty"`
	Warnings    []string                   `json:"warnings,omitempty"`
}

type composeImageRemoveOutput struct {
	ImageRef     string   `json:"image_ref"`
	UntaggedRefs []string `json:"untagged_refs,omitempty"`
	DeletedIDs   []string `json:"deleted_ids,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

type composeImageOutput struct {
	ImageID            string            `json:"image_id"`
	ImageRef           string            `json:"image_ref"`
	ResolvedRef        string            `json:"resolved_ref,omitempty"`
	RepoTags           []string          `json:"repo_tags,omitempty"`
	RepoDigests        []string          `json:"repo_digests,omitempty"`
	Store              string            `json:"store"`
	AvailabilityStatus string            `json:"availability_status"`
	Platform           string            `json:"platform,omitempty"`
	SizeBytes          uint64            `json:"size_bytes"`
	VirtualSizeBytes   uint64            `json:"virtual_size_bytes"`
	CreatedAt          string            `json:"created_at,omitempty"`
	InspectedAt        string            `json:"inspected_at,omitempty"`
	Dangling           bool              `json:"dangling"`
	ContainerCount     uint64            `json:"container_count"`
	Labels             map[string]string `json:"labels,omitempty"`
}

type composeImageStoreOutput struct {
	Store     string `json:"store"`
	Available bool   `json:"available"`
	Endpoint  string `json:"endpoint,omitempty"`
	Error     string `json:"error,omitempty"`
}

type composeImageProgressItem struct {
	ID           string `json:"id,omitempty"`
	Status       string `json:"status,omitempty"`
	Progress     string `json:"progress,omitempty"`
	CurrentBytes uint64 `json:"current_bytes,omitempty"`
	TotalBytes   uint64 `json:"total_bytes,omitempty"`
}

func composeUpOutputFromResponse(resp *agentcomposev2.ApplyProjectResponse) composeUpOutput {
	summary := resp.GetProject().GetSummary()
	revision := resp.GetRevision()
	changes := make([]composeUpChangeOutput, 0, len(resp.GetChanges()))
	for _, change := range resp.GetChanges() {
		changes = append(changes, composeUpChangeOutput{
			Action:       projectChangeActionText(change.GetAction()),
			ResourceType: change.GetResourceType(),
			ResourceID:   change.GetResourceId(),
			Name:         change.GetName(),
			Message:      change.GetMessage(),
		})
	}
	return composeUpOutput{
		Project: composeUpProjectOutput{
			ID:              summary.GetProjectId(),
			Name:            summary.GetName(),
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
	failedSessionStops := countProjectDownFailedSessionStops(resp.GetChanges())
	status := "down"
	if len(changes) == 0 {
		status = "unchanged"
	}
	if failedSessionStops > 0 {
		status = "partial-failure"
	}
	return composeDownOutput{
		Project:            composeProjectSummaryOutput(resp.GetProject().GetSummary()),
		Status:             status,
		FailedSessionStops: uint32(failedSessionStops),
		Changes:            changes,
	}
}

func composeChangeOutputs(changes []*agentcomposev2.ProjectChange) []composeUpChangeOutput {
	output := make([]composeUpChangeOutput, 0, len(changes))
	for _, change := range changes {
		output = append(output, composeUpChangeOutput{
			Action:       projectChangeActionText(change.GetAction()),
			ResourceType: change.GetResourceType(),
			ResourceID:   change.GetResourceId(),
			Name:         change.GetName(),
			Message:      change.GetMessage(),
		})
	}
	return output
}

func writeComposeUpText(out io.Writer, resp *agentcomposev2.ApplyProjectResponse) error {
	summary := resp.GetProject().GetSummary()
	revision := resp.GetRevision()
	status := "applied"
	if resp.GetUnchanged() {
		status = "unchanged"
	} else if !resp.GetApplied() {
		status = "not-applied"
	}
	if _, err := fmt.Fprintf(out, "Project: %s\nID: %s\nRevision: %d\nSpec: %s\nStatus: %s\nAgents: %d\nSchedulers: %d\n\n",
		summary.GetName(),
		summary.GetProjectId(),
		revision.GetRevision(),
		revision.GetSpecHash(),
		status,
		summary.GetAgentCount(),
		summary.GetSchedulerCount(),
	); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ACTION\tTYPE\tNAME\tID"); err != nil {
		return err
	}
	for _, change := range resp.GetChanges() {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			projectChangeActionText(change.GetAction()),
			change.GetResourceType(),
			change.GetName(),
			change.GetResourceId(),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeComposeDownText(out io.Writer, output composeDownOutput) error {
	if _, err := fmt.Fprintf(out, "Project: %s\nID: %s\nStatus: %s\nFailed session stops: %d\n\n",
		output.Project.Name,
		output.Project.ID,
		output.Status,
		output.FailedSessionStops,
	); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ACTION\tTYPE\tNAME\tID\tMESSAGE"); err != nil {
		return err
	}
	for _, change := range output.Changes {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			change.Action,
			change.ResourceType,
			change.Name,
			change.ResourceID,
			change.Message,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func countProjectDownFailedSessionStops(changes []*agentcomposev2.ProjectChange) int {
	count := 0
	for _, change := range changes {
		if change.GetAction() == agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED &&
			change.GetResourceType() == "session" &&
			strings.TrimSpace(change.GetMessage()) != "" {
			count++
		}
	}
	return count
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

func newCLIServiceClients(cli options) (serviceClients, error) {
	clientConfig, err := ResolveClientConfig(cli.Host)
	if err != nil {
		return serviceClients{}, err
	}
	httpClient := newDaemonHTTPClient(clientConfig)
	return serviceClients{
		project: agentcomposev2connect.NewProjectServiceClient(httpClient, clientConfig.BaseURL),
		run:     agentcomposev2connect.NewRunServiceClient(httpClient, clientConfig.BaseURL),
		exec:    agentcomposev2connect.NewExecServiceClient(httpClient, clientConfig.BaseURL),
		image:   agentcomposev2connect.NewImageServiceClient(httpClient, clientConfig.BaseURL),
		session: agentcomposev1connect.NewSessionServiceClient(httpClient, clientConfig.BaseURL),
	}, nil
}

func composePSOutputFromProject(ctx context.Context, clients serviceClients, project *agentcomposev2.Project) (composePSOutput, error) {
	output := composePSOutput{Project: composeProjectSummaryOutput(project.GetSummary())}
	schedulers := schedulersByAgent(project.GetSchedulers())
	for _, agent := range project.GetAgents() {
		item := composePSAgentOutput{
			AgentName:        agent.GetAgentName(),
			ManagedAgentID:   agent.GetManagedAgentId(),
			SchedulerEnabled: agent.GetSchedulerEnabled(),
			Driver:           agent.GetDriver(),
			Image:            agent.GetImage(),
		}
		if scheduler := schedulers[agent.GetAgentName()]; scheduler != nil {
			item.SchedulerID = scheduler.GetSchedulerId()
			item.SchedulerTriggers = scheduler.GetTriggerCount()
			item.SchedulerEnabled = scheduler.GetEnabled()
		}
		if latest, err := latestRunOutput(ctx, clients.run, project.GetSummary().GetProjectId(), agent.GetAgentName()); err != nil {
			return composePSOutput{}, err
		} else {
			item.LatestRun = latest
			if latest != nil {
				if latest.Driver != "" {
					item.Driver = latest.Driver
				}
				if latest.ImageRef != "" {
					item.Image = latest.ImageRef
				}
			}
		}
		if session, err := firstRunningSessionOutput(ctx, clients, project.GetSummary().GetProjectId(), agent.GetAgentName()); err != nil {
			return composePSOutput{}, err
		} else {
			item.RunningSession = session
		}
		output.Agents = append(output.Agents, item)
	}
	return output, nil
}

func latestRunOutput(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID, agentName string) (*composeRunOutput, error) {
	resp, err := client.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: projectID,
		AgentName: agentName,
		Limit:     1,
	}))
	if err != nil {
		return nil, err
	}
	if len(resp.Msg.GetRuns()) == 0 {
		return nil, nil
	}
	detail, err := getRunDetail(ctx, client, projectID, resp.Msg.GetRuns()[0].GetRunId())
	if err != nil {
		return nil, err
	}
	output := composeRunOutputFromDetail(detail.Msg.GetRun())
	return &output, nil
}

func firstRunningSessionOutput(ctx context.Context, clients serviceClients, projectID, agentName string) (*composeSessionOutput, error) {
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
		sessionID := strings.TrimSpace(run.GetSessionId())
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		session, err := clients.session.GetSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID}))
		if err != nil {
			continue
		}
		summary := session.Msg.GetSession().GetSummary()
		if strings.EqualFold(summary.GetVmStatus(), "running") {
			output := composeSessionOutputFromSummary(summary)
			return &output, nil
		}
	}
	return nil, nil
}

func writePSText(out io.Writer, output composePSOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "AGENT\tSCHEDULER\tLATEST RUN\tRUN STATUS\tSESSION\tDRIVER\tIMAGE"); err != nil {
		return err
	}
	for _, agent := range output.Agents {
		latestRunID := "-"
		latestStatus := "-"
		if agent.LatestRun != nil {
			latestRunID = agent.LatestRun.RunID
			latestStatus = agent.LatestRun.Status
		}
		sessionID := "-"
		if agent.RunningSession != nil {
			sessionID = agent.RunningSession.SessionID
		}
		scheduler := "disabled"
		if agent.SchedulerEnabled {
			scheduler = "enabled"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			agent.AgentName,
			scheduler,
			latestRunID,
			latestStatus,
			sessionID,
			firstNonEmptyString(agent.Driver, "-"),
			firstNonEmptyString(agent.Image, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func schedulersByAgent(items []*agentcomposev2.ProjectScheduler) map[string]*agentcomposev2.ProjectScheduler {
	result := make(map[string]*agentcomposev2.ProjectScheduler, len(items))
	for _, item := range items {
		result[item.GetAgentName()] = item
	}
	return result
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

func composeAgentInspectOutputFor(ctx context.Context, clients serviceClients, project *agentcomposev2.Project, agentName string) (composeAgentInspectOutput, error) {
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
	if session, err := firstRunningSessionOutput(ctx, clients, project.GetSummary().GetProjectId(), agentName); err != nil {
		return composeAgentInspectOutput{}, commandExitErrorForConnect(fmt.Errorf("list running session for agent %s: %w", agentName, err))
	} else if session != nil {
		output.RunningSessions = append(output.RunningSessions, *session)
	}
	return output, nil
}

func composeProjectSummaryOutput(summary *agentcomposev2.ProjectSummary) composeUpProjectOutput {
	return composeUpProjectOutput{
		ID:              summary.GetProjectId(),
		Name:            summary.GetName(),
		SourcePath:      summary.GetSourcePath(),
		CurrentRevision: summary.GetCurrentRevision(),
		SpecHash:        summary.GetSpecHash(),
		AgentCount:      summary.GetAgentCount(),
		SchedulerCount:  summary.GetSchedulerCount(),
	}
}

func composeProjectAgentOutputFromProto(agent *agentcomposev2.ProjectAgent) composeProjectAgentOutput {
	return composeProjectAgentOutput{
		AgentName:        agent.GetAgentName(),
		ManagedAgentID:   agent.GetManagedAgentId(),
		Provider:         agent.GetProvider(),
		Model:            agent.GetModel(),
		Image:            agent.GetImage(),
		Driver:           agent.GetDriver(),
		SchedulerEnabled: agent.GetSchedulerEnabled(),
	}
}

func composeProjectSchedulerOutputFromProto(scheduler *agentcomposev2.ProjectScheduler) composeProjectSchedulerOutput {
	return composeProjectSchedulerOutput{
		AgentName:       scheduler.GetAgentName(),
		SchedulerID:     scheduler.GetSchedulerId(),
		ManagedLoaderID: scheduler.GetManagedLoaderId(),
		Enabled:         scheduler.GetEnabled(),
		TriggerCount:    scheduler.GetTriggerCount(),
	}
}

func composeRunOutputFromDetail(run *agentcomposev2.RunDetail) composeRunOutput {
	summary := run.GetSummary()
	return composeRunOutput{
		RunID:        summary.GetRunId(),
		ProjectID:    summary.GetProjectId(),
		ProjectName:  summary.GetProjectName(),
		AgentName:    summary.GetAgentName(),
		Source:       runSourceText(summary.GetSource()),
		Status:       runStatusText(summary.GetStatus()),
		SessionID:    summary.GetSessionId(),
		ExitCode:     summary.GetExitCode(),
		Error:        summary.GetError(),
		StartedAt:    summary.GetStartedAt(),
		CompletedAt:  summary.GetCompletedAt(),
		DurationMs:   summary.GetDurationMs(),
		Prompt:       run.GetPrompt(),
		Output:       run.GetOutput(),
		ResultJSON:   run.GetResultJson(),
		LogsPath:     run.GetLogsPath(),
		ArtifactsDir: run.GetArtifactsDir(),
		CleanupError: run.GetCleanupError(),
		Driver:       run.GetDriver(),
		ImageRef:     run.GetImageRef(),
	}
}

func composeExecOutputFromResult(result *agentcomposev2.ExecResult) composeExecOutput {
	return composeExecOutput{
		ExecID:    result.GetExecId(),
		SessionID: result.GetSessionId(),
		RunID:     result.GetRunId(),
		Command:   result.GetCommand().GetCommand(),
		Args:      append([]string(nil), result.GetCommand().GetArgs()...),
		Cwd:       result.GetCwd(),
		ExitCode:  result.GetExitCode(),
		Success:   result.GetSuccess(),
		Stdout:    result.GetStdout(),
		Stderr:    result.GetStderr(),
		Output:    result.GetOutput(),
		Error:     result.GetError(),
	}
}

func composeImageListOutputFromResponse(resp *agentcomposev2.ListImagesResponse) composeImageListOutput {
	output := composeImageListOutput{
		Images:      make([]composeImageOutput, 0, len(resp.GetImages())),
		TotalCount:  resp.GetTotalCount(),
		HasMore:     resp.GetHasMore(),
		NextOffset:  resp.GetNextOffset(),
		StoreStatus: composeImageStoreOutputFromProto(resp.GetStoreStatus()),
	}
	for _, image := range resp.GetImages() {
		output.Images = append(output.Images, composeImageOutputFromProto(image))
	}
	return output
}

func composeImagePullOutputFromResponse(resp *agentcomposev2.PullImageResponse) composeImagePullOutput {
	output := composeImagePullOutput{
		ImageRef:    firstNonEmptyString(resp.GetImage().GetImageRef(), resp.GetResolvedRef()),
		ResolvedRef: resp.GetResolvedRef(),
		Status:      imageOperationStatusText(resp.GetStatus()),
		Image:       composeImageOutputFromProto(resp.GetImage()),
		Warnings:    append([]string(nil), resp.GetWarnings()...),
		Progress:    make([]composeImageProgressItem, 0, len(resp.GetProgress())),
	}
	for _, item := range resp.GetProgress() {
		output.Progress = append(output.Progress, composeImageProgressItem{
			ID:           item.GetId(),
			Status:       item.GetStatus(),
			Progress:     item.GetProgress(),
			CurrentBytes: item.GetCurrentBytes(),
			TotalBytes:   item.GetTotalBytes(),
		})
	}
	return output
}

func composeImageInspectOutputFromResponse(resp *agentcomposev2.InspectImageResponse) composeImageInspectOutput {
	return composeImageInspectOutput{
		Image:       composeImageOutputFromProto(resp.GetImage()),
		StoreStatus: composeImageStoreOutputFromProto(resp.GetStoreStatus()),
	}
}

func composeImageRemoveOutputFromResponse(resp *agentcomposev2.RemoveImageResponse) composeImageRemoveOutput {
	return composeImageRemoveOutput{
		ImageRef:     resp.GetImageRef(),
		UntaggedRefs: append([]string(nil), resp.GetUntaggedRefs()...),
		DeletedIDs:   append([]string(nil), resp.GetDeletedIds()...),
		Warnings:     append([]string(nil), resp.GetWarnings()...),
	}
}

func composeImageOutputFromProto(image *agentcomposev2.Image) composeImageOutput {
	if image == nil {
		return composeImageOutput{}
	}
	return composeImageOutput{
		ImageID:            image.GetImageId(),
		ImageRef:           image.GetImageRef(),
		ResolvedRef:        image.GetResolvedRef(),
		RepoTags:           append([]string(nil), image.GetRepoTags()...),
		RepoDigests:        append([]string(nil), image.GetRepoDigests()...),
		Store:              imageStoreText(image.GetStore()),
		AvailabilityStatus: imageAvailabilityStatusText(image.GetAvailabilityStatus()),
		Platform:           imagePlatformText(image.GetPlatform()),
		SizeBytes:          image.GetSizeBytes(),
		VirtualSizeBytes:   image.GetVirtualSizeBytes(),
		CreatedAt:          image.GetCreatedAt(),
		InspectedAt:        image.GetInspectedAt(),
		Dangling:           image.GetDangling(),
		ContainerCount:     image.GetContainerCount(),
		Labels:             cloneStringMap(image.GetLabels()),
	}
}

func composeImageStoreOutputFromProto(status *agentcomposev2.ImageStoreStatus) composeImageStoreOutput {
	if status == nil {
		return composeImageStoreOutput{}
	}
	return composeImageStoreOutput{
		Store:     imageStoreText(status.GetStore()),
		Available: status.GetAvailable(),
		Endpoint:  status.GetEndpoint(),
		Error:     status.GetError(),
	}
}

func writeImagesText(out io.Writer, images []composeImageOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "IMAGE ID\tREF\tSTATUS\tSIZE\tCREATED"); err != nil {
		return err
	}
	for _, image := range images {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			shortImageID(image.ImageID),
			firstNonEmptyString(image.ImageRef, image.ResolvedRef, "-"),
			firstNonEmptyString(image.AvailabilityStatus, "-"),
			image.SizeBytes,
			firstNonEmptyString(image.CreatedAt, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func composeSessionOutputFromSummary(summary *agentcomposev1.SessionSummary) composeSessionOutput {
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
	return composeSessionOutput{
		SessionID:     summary.GetSessionId(),
		Title:         summary.GetTitle(),
		Driver:        summary.GetDriver(),
		VMStatus:      strings.ToLower(strings.TrimSpace(summary.GetVmStatus())),
		WorkspacePath: summary.GetWorkspacePath(),
		ProxyPath:     summary.GetProxyPath(),
		GuestImage:    summary.GetGuestImage(),
		TriggerSource: summary.GetTriggerSource(),
		CreatedAt:     summary.GetCreatedAt(),
		UpdatedAt:     summary.GetUpdatedAt(),
		CellCount:     summary.GetCellCount(),
		EventCount:    summary.GetEventCount(),
		Tags:          tags,
	}
}
