package main

import (
	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/compose"
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

type composeSchedulerListOptions struct {
	Verbose bool
}

func runComposeSchedulerListCommand(cmd *cobra.Command, cli cliOptions, options composeSchedulerListOptions, args []string) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	agentFilter := ""
	if len(args) > 0 {
		agentFilter, err = resolveComposeAgentNameFromSpec(normalized, projectID, args[0])
		if err != nil {
			return err
		}
	}
	triggers, err := listComposeSchedulerTriggers(cmd.Context(), clients, normalized, projectID, agentFilter)
	if err != nil {
		return err
	}
	output := composeSchedulerListOutput{
		Project:  composeUpProjectOutput{ID: displayOpaqueID(projectID), Name: normalized.Name},
		Triggers: triggers,
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeSchedulerListText(cmd.OutOrStdout(), output, options.Verbose)
}

func resolveSchedulerRunID(ctx context.Context, client agentcomposev2connect.ResourceServiceClient, projectID, runRef string) (string, error) {
	runRef = strings.TrimSpace(runRef)
	if identity.IsID(runRef) || isLegacySchedulerRunID(runRef) {
		return runRef, nil
	}
	if strings.Contains(runRef, "-") {
		return "", schedulerResourceNotFoundError{kind: "run", ref: runRef}
	}
	resp, err := client.ResolveID(ctx, connect.NewRequest(&agentcomposev2.ResolveResourceIDRequest{
		Id:    runRef,
		Kinds: []agentcomposev2.ResourceKind{agentcomposev2.ResourceKind_RESOURCE_KIND_RUN},
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return "", schedulerResourceNotFoundError{kind: "run", ref: runRef}
		}
		return "", commandExitErrorForConnect(fmt.Errorf("resolve scheduler run %s: %w", runRef, err))
	}
	matches := make([]string, 0, len(resp.Msg.GetTargets()))
	for _, target := range resp.Msg.GetTargets() {
		if target.GetKind() == agentcomposev2.ResourceKind_RESOURCE_KIND_RUN && (target.GetProjectId() == "" || target.GetProjectId() == projectID) {
			matches = append(matches, target.GetId())
		}
	}
	if len(matches) == 0 {
		return "", schedulerResourceNotFoundError{kind: "run", ref: runRef}
	}
	if len(matches) > 1 {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler run reference %q is ambiguous", runRef)}
	}
	return matches[0], nil
}

func shouldResolveSchedulerRunRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	return shouldResolveComposeLogResourceRef(ref) || (len(ref) >= 6 && strings.Contains(ref, "-"))
}

func listComposeSchedulerTriggers(ctx context.Context, clients cliServiceClients, normalized *compose.NormalizedProjectSpec, projectID, agentFilter string) ([]composeSchedulerTriggerItem, error) {
	var items []composeSchedulerTriggerItem
	for _, agent := range normalized.Agents {
		if agentFilter != "" && agent.Name != agentFilter {
			continue
		}
		if agent.Scheduler == nil {
			continue
		}
		schedulerID, err := domain.StableProjectSchedulerID(projectID, agent.Name, "")
		if err != nil {
			return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolve scheduler for agent %q: %w", agent.Name, err)}
		}
		schedulerEnabled := agent.Scheduler.Enabled
		if agent.Scheduler.HasScript() {
			scheduler, err := clients.project.GetScheduler(ctx, connect.NewRequest(&agentcomposev2.GetSchedulerRequest{Project: &agentcomposev2.ProjectRef{ProjectId: projectID}, AgentName: agent.Name}))
			if err != nil {
				return nil, commandExitErrorForConnect(fmt.Errorf("get scheduler %s: %w", schedulerID, err))
			}
			for _, trigger := range scheduler.Msg.GetTriggers() {
				items = append(items, schedulerTriggerItemFromResolved(agent.Name, schedulerID, schedulerEnabled, trigger))
			}
			continue
		}
		for index, trigger := range agent.Scheduler.Triggers {
			id, err := domain.StableManagedTriggerID(projectID, agent.Name, "", trigger.Name, index)
			if err != nil {
				return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolve trigger for agent %q: %w", agent.Name, err)}
			}
			items = append(items, schedulerTriggerItemFromDeclarative(agent.Name, schedulerID, schedulerEnabled, id, trigger))
		}
	}
	if agentFilter != "" && len(items) == 0 {
		if _, ok := composeRunAgentSpec(normalized, agentFilter); !ok {
			return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent %q is not configured in this project", agentFilter)}
		}
	}
	return items, nil
}

func listComposeSchedulerRuns(ctx context.Context, clients cliServiceClients, normalized *compose.NormalizedProjectSpec, projectID, agentRef, triggerRef, status string, limit uint32) ([]composeSchedulerRunItem, error) {
	agentFilter := ""
	if strings.TrimSpace(agentRef) != "" {
		scheduler, resolveErr := resolveComposeScheduler(normalized, projectID, agentRef)
		if resolveErr != nil {
			return nil, resolveErr
		}
		agentFilter = scheduler.AgentName
	}
	if limit == 0 {
		limit = 20
	}
	runStatus, statusText, statusErr := parseSchedulerRunStatusFilter(status)
	if statusErr != nil {
		return nil, statusErr
	}
	triggerRef = strings.TrimSpace(triggerRef)
	items := make([]composeSchedulerRunItem, 0)
	for _, agent := range normalized.Agents {
		if agent.Scheduler == nil || (agentFilter != "" && agent.Name != agentFilter) {
			continue
		}
		schedulerID, idErr := domain.StableProjectSchedulerID(projectID, agent.Name, "")
		if idErr != nil {
			return nil, idErr
		}
		loaderID, idErr := domain.StableManagedLoaderID(projectID, agent.Name, "")
		if idErr != nil {
			return nil, idErr
		}
		runsResp, listErr := clients.run.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
			ProjectId:   projectID,
			AgentName:   agent.Name,
			SchedulerId: schedulerID,
			Status:      runStatus,
			Limit:       limit,
		}))
		if listErr != nil && connect.CodeOf(listErr) != connect.CodeUnimplemented {
			return nil, commandExitErrorForConnect(fmt.Errorf("list scheduler runs for agent %s: %w", agent.Name, listErr))
		}
		var triggers []composeSchedulerTriggerItem
		if triggerRef != "" {
			triggers, listErr = listComposeSchedulerTriggers(ctx, clients, normalized, projectID, agent.Name)
			if listErr != nil {
				return nil, listErr
			}
		}
		var projectRuns []*agentcomposev2.RunSummary
		if runsResp != nil {
			projectRuns = runsResp.Msg.GetRuns()
		}
		for _, run := range projectRuns {
			if statusText != "" && strings.ToLower(runStatusText(run.GetStatus())) != statusText {
				continue
			}
			if triggerRef != "" && !resourceRefMatches(triggerRef, run.GetTriggerId()) {
				matched := false
				for _, trigger := range triggers {
					if resourceRefMatches(triggerRef, trigger.Name, trigger.TriggerID, trigger.RawTriggerID) && resourceRefMatches(run.GetTriggerId(), trigger.TriggerID, trigger.RawTriggerID) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			items = append(items, schedulerRunItem(agent.Name, schedulerID, loaderID, run))
		}
		runtimeRuns, runtimeErr := listSchedulerRuntimeRuns(ctx, clients.project, projectID, agent.Name, schedulerID, loaderID, 500)
		if runtimeErr != nil {
			return nil, runtimeErr
		}
		for _, run := range runtimeRuns {
			if statusText != "" && run.Status != statusText {
				continue
			}
			if triggerRef != "" && !schedulerRunTriggerMatches(run.TriggerID, triggerRef, triggers) {
				continue
			}
			items = append(items, run)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].StartedAt == items[j].StartedAt {
			return items[i].RunID > items[j].RunID
		}
		return items[i].StartedAt > items[j].StartedAt
	})
	if uint32(len(items)) > limit {
		items = items[:limit]
	}
	return items, nil
}

func schedulerRunTriggerMatches(runTriggerID, ref string, triggers []composeSchedulerTriggerItem) bool {
	if resourceRefMatches(ref, runTriggerID) {
		return true
	}
	for _, trigger := range triggers {
		if resourceRefMatches(ref, trigger.Name, trigger.TriggerID, trigger.RawTriggerID) && resourceRefMatches(runTriggerID, trigger.TriggerID, trigger.RawTriggerID) {
			return true
		}
	}
	return false
}

func parseSchedulerRunStatusFilter(value string) (agentcomposev2.RunStatus, string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	statuses := map[string]agentcomposev2.RunStatus{
		"":          agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED,
		"pending":   agentcomposev2.RunStatus_RUN_STATUS_PENDING,
		"running":   agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
		"succeeded": agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
		"failed":    agentcomposev2.RunStatus_RUN_STATUS_FAILED,
		"canceled":  agentcomposev2.RunStatus_RUN_STATUS_CANCELED,
		"skipped":   agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED,
	}
	status, ok := statuses[value]
	if !ok {
		return agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED, "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler runs --status must be pending, running, succeeded, failed, canceled, or skipped")}
	}
	return status, value, nil
}

func schedulerRunItem(agentName, schedulerID, loaderID string, run *agentcomposev2.RunSummary) composeSchedulerRunItem {
	sandboxIDs := []string(nil)
	if strings.TrimSpace(run.GetSandboxId()) != "" {
		sandboxIDs = []string{run.GetSandboxId()}
	}
	return composeSchedulerRunItem{
		RunID:           run.GetRunId(),
		RunShortID:      shortOpaqueID(run.GetRunId()),
		AgentName:       agentName,
		SchedulerID:     schedulerID,
		ManagedLoaderID: loaderID,
		TriggerID:       run.GetTriggerId(),
		Status:          runStatusText(run.GetStatus()),
		SandboxIDs:      sandboxIDs,
		StartedAt:       run.GetStartedAt(),
		CompletedAt:     run.GetCompletedAt(),
		DurationMs:      run.GetDurationMs(),
		Error:           run.GetError(),
		rawRun:          run,
	}
}

func listSchedulerRunEvents(ctx context.Context, clients cliServiceClients, projectID string, run composeSchedulerRunItem) ([]composeSchedulerLogEvent, error) {
	if run.schedulerRuntime {
		return listSchedulerRuntimeLogEvents(ctx, clients.project, projectID, run)
	}
	events := make([]composeSchedulerLogEvent, 0)
	sandboxID := ""
	if len(run.SandboxIDs) > 0 {
		sandboxID = run.SandboxIDs[0]
	}
	cursor := ""
	for {
		resp, err := clients.run.ListRunEvents(ctx, connect.NewRequest(&agentcomposev2.ListRunEventsRequest{RunId: run.RunID, Limit: 500, Cursor: cursor}))
		if err != nil {
			return nil, err
		}
		for _, event := range resp.Msg.GetEvents() {
			events = append(events, composeSchedulerLogEvent{
				ID:          event.GetId(),
				RunID:       event.GetRunId(),
				AgentName:   run.AgentName,
				TriggerID:   run.TriggerID,
				Type:        schedulerRunEventType(event.GetKind()),
				Level:       "info",
				Message:     firstNonEmptyString(event.GetText(), event.GetName()),
				PayloadJSON: event.GetPayloadJson(),
				SandboxID:   sandboxID,
				CreatedAt:   formatProtoTimestamp(event.GetCreatedAt()),
			})
		}
		nextCursor := strings.TrimSpace(resp.Msg.GetNextCursor())
		if nextCursor == "" || nextCursor == cursor {
			break
		}
		cursor = nextCursor
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].CreatedAt < events[j].CreatedAt })
	return events, nil
}

func listSchedulerRuntimeRuns(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, projectID, agentName, schedulerID, loaderID string, limit uint32) ([]composeSchedulerRunItem, error) {
	runs, err := listSchedulerRunsFromAPI(ctx, client, projectID, agentName, schedulerID, loaderID, limit)
	if err == nil {
		legacy, legacyErr := listLegacySchedulerRuntimeRuns(ctx, client, projectID, agentName, schedulerID, loaderID, limit)
		if legacyErr == nil {
			return mergeSchedulerRuntimeRuns(runs, legacy), nil
		}
		return runs, nil
	}
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		return nil, commandExitErrorForConnect(fmt.Errorf("list scheduler runs for agent %s: %w", agentName, err))
	}
	legacy, legacyErr := listLegacySchedulerRuntimeRuns(ctx, client, projectID, agentName, schedulerID, loaderID, limit)
	if legacyErr != nil {
		return nil, commandExitErrorForConnect(fmt.Errorf("list scheduler runs for agent %s: %w", agentName, err))
	}
	return legacy, nil
}

func listSchedulerRunsFromAPI(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, projectID, agentName, schedulerID, loaderID string, limit uint32) ([]composeSchedulerRunItem, error) {
	if limit == 0 || limit > 500 {
		limit = 500
	}
	runs := make([]composeSchedulerRunItem, 0, limit)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for uint32(len(runs)) < limit {
		pageLimit := uint32(100)
		if remaining := limit - uint32(len(runs)); remaining < pageLimit {
			pageLimit = remaining
		}
		resp, err := client.ListSchedulerRuns(ctx, connect.NewRequest(&agentcomposev2.ListSchedulerRunsRequest{
			Project: &agentcomposev2.ProjectRef{ProjectId: projectID}, AgentName: agentName, Limit: pageLimit, Cursor: cursor,
		}))
		if err != nil {
			return nil, err
		}
		for _, run := range resp.Msg.GetRuns() {
			runs = append(runs, schedulerRuntimeRunItem(schedulerID, loaderID, run))
			if uint32(len(runs)) == limit {
				return runs, nil
			}
		}
		next := strings.TrimSpace(resp.Msg.GetNextCursor())
		if next == "" {
			return runs, nil
		}
		if _, ok := seenCursors[next]; ok {
			return nil, fmt.Errorf("daemon returned a repeated scheduler run cursor")
		}
		seenCursors[next] = struct{}{}
		cursor = next
	}
	return runs, nil
}

func schedulerRuntimeRunItem(schedulerID, loaderID string, run *agentcomposev2.SchedulerRun) composeSchedulerRunItem {
	return composeSchedulerRunItem{
		RunID:            run.GetRunId(),
		RunShortID:       shortOpaqueID(run.GetRunId()),
		AgentName:        run.GetAgentName(),
		SchedulerID:      firstNonEmptyString(run.GetSchedulerId(), schedulerID),
		ManagedLoaderID:  loaderID,
		TriggerID:        run.GetTriggerId(),
		TriggerKind:      run.GetTriggerKind(),
		TriggerSource:    run.GetTriggerSource(),
		Status:           schedulerRunStatusText(run.GetStatus()),
		StartedAt:        formatProtoTimestamp(run.GetStartedAt()),
		CompletedAt:      formatProtoTimestamp(run.GetCompletedAt()),
		DurationMs:       run.GetDurationMs(),
		Error:            run.GetError(),
		ResultJSON:       run.GetResultJson(),
		PayloadJSON:      run.GetPayloadJson(),
		ArtifactsDir:     run.GetArtifactsDir(),
		schedulerRuntime: true,
	}
}

func listLegacySchedulerRuntimeRuns(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, projectID, agentName, schedulerID, loaderID string, eventLimit uint32) ([]composeSchedulerRunItem, error) {
	resp, err := client.ListSchedulerEvents(ctx, connect.NewRequest(&agentcomposev2.ListSchedulerEventsRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID}, AgentName: agentName, Limit: eventLimit,
	}))
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*composeSchedulerRunItem)
	for _, event := range resp.Msg.GetEvents() {
		runID := strings.TrimSpace(event.GetRunId())
		if runID == "" {
			continue
		}
		run := byID[runID]
		if run == nil {
			run = &composeSchedulerRunItem{RunID: runID, RunShortID: shortOpaqueID(runID), AgentName: agentName, SchedulerID: schedulerID, ManagedLoaderID: loaderID, TriggerID: event.GetTriggerId(), Status: "running", schedulerRuntime: true}
			byID[runID] = run
		}
		applySchedulerRuntimeEvent(run, event)
	}
	runs := make([]composeSchedulerRunItem, 0, len(byID))
	for _, run := range byID {
		if run.StartedAt != "" && run.CompletedAt != "" {
			started, startErr := time.Parse(time.RFC3339Nano, run.StartedAt)
			completed, completeErr := time.Parse(time.RFC3339Nano, run.CompletedAt)
			if startErr == nil && completeErr == nil {
				run.DurationMs = completed.Sub(started).Milliseconds()
			}
		}
		runs = append(runs, *run)
	}
	return runs, nil
}

func applySchedulerRuntimeEvent(run *composeSchedulerRunItem, event *agentcomposev2.SchedulerEvent) {
	eventType := strings.TrimSpace(event.GetType())
	createdAt := formatProtoTimestamp(event.GetCreatedAt())
	switch eventType {
	case "loader.run.started":
		run.StartedAt = createdAt
	case "loader.run.completed":
		run.Status, run.CompletedAt = "succeeded", createdAt
	case "loader.run.failed":
		run.Status, run.CompletedAt, run.Error = "failed", createdAt, event.GetMessage()
	}
	var payload map[string]any
	if json.Unmarshal([]byte(event.GetPayloadJson()), &payload) == nil {
		if sandboxID, ok := payload["sandboxId"].(string); ok && sandboxID != "" && !slices.Contains(run.SandboxIDs, sandboxID) {
			run.SandboxIDs = append(run.SandboxIDs, sandboxID)
		}
		if result, ok := payload["resultJson"].(string); ok {
			run.ResultJSON = result
		}
	}
}

func resolveSchedulerRuntimeRun(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, normalized *compose.NormalizedProjectSpec, projectID, ref string) (*composeSchedulerRunItem, error) {
	ref = strings.TrimSpace(ref)
	response, err := client.GetSchedulerRun(ctx, connect.NewRequest(&agentcomposev2.GetSchedulerRunRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID}, RunId: ref,
	}))
	if err == nil && response != nil && response.Msg.GetRun() != nil {
		run := response.Msg.GetRun()
		loaderID, idErr := domain.StableManagedLoaderID(projectID, run.GetAgentName(), "")
		if idErr != nil {
			return nil, idErr
		}
		item := schedulerRuntimeRunItem(run.GetSchedulerId(), loaderID, run)
		return &item, nil
	}
	if err != nil && connect.CodeOf(err) != connect.CodeNotFound && connect.CodeOf(err) != connect.CodeUnimplemented {
		return nil, commandExitErrorForConnect(fmt.Errorf("get scheduler run %s: %w", ref, err))
	}
	matches := make([]composeSchedulerRunItem, 0)
	for _, agent := range normalized.Agents {
		if agent.Scheduler == nil {
			continue
		}
		schedulerID, _ := domain.StableProjectSchedulerID(projectID, agent.Name, "")
		loaderID, _ := domain.StableManagedLoaderID(projectID, agent.Name, "")
		runs, err := listSchedulerRuntimeRuns(ctx, client, projectID, agent.Name, schedulerID, loaderID, 500)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if resourceRefMatches(ref, run.RunID, run.RunShortID) {
				matches = append(matches, run)
			}
		}
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}
	if len(matches) > 1 {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler run reference %q is ambiguous", ref)}
	}
	return nil, schedulerResourceNotFoundError{kind: "run", ref: ref}
}

func listSchedulerRuntimeLogEvents(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, projectID string, run composeSchedulerRunItem) ([]composeSchedulerLogEvent, error) {
	resp, err := client.ListSchedulerEvents(ctx, connect.NewRequest(&agentcomposev2.ListSchedulerEventsRequest{Project: &agentcomposev2.ProjectRef{ProjectId: projectID}, AgentName: run.AgentName, Limit: 500}))
	if err != nil {
		return nil, err
	}
	events := make([]composeSchedulerLogEvent, 0)
	for _, event := range resp.Msg.GetEvents() {
		if event.GetRunId() == run.RunID {
			events = append(events, composeSchedulerLogEvent{ID: event.GetId(), RunID: run.RunID, AgentName: run.AgentName, TriggerID: event.GetTriggerId(), Type: schedulerDisplayEventType(event.GetType()), Level: event.GetLevel(), Message: event.GetMessage(), PayloadJSON: event.GetPayloadJson(), CreatedAt: formatProtoTimestamp(event.GetCreatedAt())})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].CreatedAt < events[j].CreatedAt })
	return events, nil
}

func schedulerRunEventType(kind agentcomposev2.RunEventKind) string {
	return "scheduler." + strings.ReplaceAll(strings.TrimPrefix(strings.ToLower(kind.String()), "run_event_kind_"), "_", ".")
}

func resolveComposeScheduler(normalized *compose.NormalizedProjectSpec, projectID, ref string) (*composeSchedulerItem, error) {
	ref = strings.TrimSpace(ref)
	matches := make([]composeSchedulerItem, 0)
	for _, agent := range normalized.Agents {
		if agent.Scheduler == nil {
			continue
		}
		schedulerID, err := domain.StableProjectSchedulerID(projectID, agent.Name, "")
		if err != nil {
			return nil, err
		}
		loaderID, err := domain.StableManagedLoaderID(projectID, agent.Name, "")
		if err != nil {
			return nil, err
		}
		if resourceRefMatches(ref, agent.Name, schedulerID, loaderID) {
			matches = append(matches, composeSchedulerItem{AgentName: agent.Name, SchedulerID: schedulerID, ManagedLoaderID: loaderID, Enabled: agent.Scheduler.Enabled, TriggerCount: len(agent.Scheduler.Triggers)})
		}
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}
	if len(matches) > 1 {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler reference %q is ambiguous", ref)}
	}
	return nil, schedulerResourceNotFoundError{kind: "resource", ref: ref}
}

func resolveSchedulerTriggerFromItems(items []composeSchedulerTriggerItem, ref string) (*composeSchedulerTriggerItem, error) {
	matches := make([]composeSchedulerTriggerItem, 0)
	for _, item := range items {
		if resourceRefMatches(ref, item.Name, item.TriggerID, item.RawTriggerID) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("scheduler trigger reference %q is ambiguous", ref)
	}
	return nil, schedulerResourceNotFoundError{kind: "trigger", ref: ref}
}

func resolveComposeSchedulerTrigger(ctx context.Context, clients cliServiceClients, normalized *compose.NormalizedProjectSpec, projectID, agentName, triggerRef string) (composeSchedulerTriggerItem, error) {
	triggerRef = strings.TrimSpace(triggerRef)
	if strings.TrimSpace(agentName) == "" || triggerRef == "" {
		return composeSchedulerTriggerItem{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler trigger requires non-empty agent and trigger")}
	}
	resolvedAgentName, err := resolveComposeAgentNameFromSpec(normalized, projectID, agentName)
	if err != nil {
		return composeSchedulerTriggerItem{}, err
	}
	agentName = resolvedAgentName
	items, err := listComposeSchedulerTriggers(ctx, clients, normalized, projectID, agentName)
	if err != nil {
		return composeSchedulerTriggerItem{}, err
	}
	var matches []composeSchedulerTriggerItem
	for _, item := range items {
		if item.TriggerID == triggerRef || item.RawTriggerID == triggerRef || (item.Name != "" && item.Name == triggerRef) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return composeSchedulerTriggerItem{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler trigger %q not found for agent %q", triggerRef, agentName)}
	}
	if len(matches) > 1 {
		return composeSchedulerTriggerItem{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler trigger %q for agent %q is ambiguous; use the trigger id", triggerRef, agentName)}
	}
	return matches[0], nil
}

func schedulerTriggerItemFromDeclarative(agentName, schedulerID string, schedulerEnabled bool, triggerID string, trigger compose.NormalizedTriggerSpec) composeSchedulerTriggerItem {
	protoTrigger := api.TriggerSpecToProto(trigger)
	return composeSchedulerTriggerItem{
		AgentName:        agentName,
		Name:             strings.TrimSpace(trigger.Name),
		TriggerID:        displayOpaqueID(triggerID),
		TriggerShortID:   shortOpaqueID(triggerID),
		RawTriggerID:     triggerID,
		Kind:             trigger.Kind,
		Source:           "declarative",
		SchedulerID:      displayOpaqueID(schedulerID),
		SchedulerShortID: shortOpaqueID(schedulerID),
		RawSchedulerID:   schedulerID,
		SchedulerEnabled: schedulerEnabled,
		TriggerEnabled:   true,
		declarative:      protoTrigger,
	}
}

func schedulerTriggerItemFromResolved(agentName, schedulerID string, schedulerEnabled bool, trigger *agentcomposev2.ResolvedTrigger) composeSchedulerTriggerItem {
	interval, _ := time.ParseDuration(trigger.GetSpec().GetInterval())
	registered := map[string]any{"loader_id": "", "trigger_id": trigger.GetTriggerId(), "kind": trigger.GetSpec().GetKind(), "enabled": trigger.GetEnabled(), "auto_id": false, "interval_ms": interval.Milliseconds(), "topic": trigger.GetSpec().GetEvent().GetTopic(), "spec_json": "", "next_fire_at": formatProtoTimestamp(trigger.GetNextFireAt()), "last_fired_at": formatProtoTimestamp(trigger.GetLastFiredAt())}
	return composeSchedulerTriggerItem{
		AgentName:        agentName,
		TriggerID:        displayOpaqueID(trigger.GetTriggerId()),
		TriggerShortID:   shortOpaqueID(trigger.GetTriggerId()),
		RawTriggerID:     trigger.GetTriggerId(),
		Kind:             trigger.GetSpec().GetKind(),
		Source:           "script",
		SchedulerID:      displayOpaqueID(schedulerID),
		SchedulerShortID: shortOpaqueID(schedulerID),
		RawSchedulerID:   schedulerID,
		SchedulerEnabled: schedulerEnabled,
		TriggerEnabled:   trigger.GetEnabled(), Topic: trigger.GetSpec().GetEvent().GetTopic(), IntervalMs: interval.Milliseconds(), NextFireAt: formatProtoTimestamp(trigger.GetNextFireAt()), LastFiredAt: formatProtoTimestamp(trigger.GetLastFiredAt()), registered: registered,
	}
}

type composeSchedulerItem struct {
	AgentName       string `json:"agent_name"`
	SchedulerID     string `json:"scheduler_id"`
	ManagedLoaderID string `json:"managed_loader_id"`
	Enabled         bool   `json:"enabled"`
	TriggerCount    int    `json:"trigger_count"`
}

type composeSchedulerRunItem struct {
	RunID            string   `json:"run_id"`
	RunShortID       string   `json:"run_short_id"`
	AgentName        string   `json:"agent_name"`
	SchedulerID      string   `json:"scheduler_id"`
	ManagedLoaderID  string   `json:"managed_loader_id"`
	TriggerID        string   `json:"trigger_id,omitempty"`
	TriggerKind      string   `json:"trigger_kind,omitempty"`
	TriggerSource    string   `json:"trigger_source,omitempty"`
	Status           string   `json:"status"`
	SandboxIDs       []string `json:"sandbox_ids,omitempty"`
	StartedAt        string   `json:"started_at,omitempty"`
	CompletedAt      string   `json:"completed_at,omitempty"`
	DurationMs       int64    `json:"duration_ms,omitempty"`
	Error            string   `json:"error,omitempty"`
	ResultJSON       string   `json:"result_json,omitempty"`
	PayloadJSON      string   `json:"payload_json,omitempty"`
	ArtifactsDir     string   `json:"artifacts_dir,omitempty"`
	rawRun           *agentcomposev2.RunSummary
	schedulerRuntime bool
}

type composeSchedulerLogEvent struct {
	ID            string `json:"id"`
	RunID         string `json:"run_id"`
	AgentName     string `json:"agent_name"`
	TriggerID     string `json:"trigger_id,omitempty"`
	Type          string `json:"type"`
	Level         string `json:"level"`
	Message       string `json:"message,omitempty"`
	PayloadJSON   string `json:"payload_json,omitempty"`
	SandboxID     string `json:"sandbox_id,omitempty"`
	CellID        string `json:"cell_id,omitempty"`
	AgentThreadID string `json:"agent_thread_id,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
}

type composeSchedulerTriggerItem struct {
	AgentName        string `json:"agent_name"`
	Name             string `json:"name,omitempty"`
	TriggerID        string `json:"trigger_id"`
	TriggerShortID   string `json:"trigger_short_id"`
	RawTriggerID     string `json:"-"`
	Kind             string `json:"kind"`
	Source           string `json:"source"`
	SchedulerID      string `json:"scheduler_id,omitempty"`
	SchedulerShortID string `json:"scheduler_short_id,omitempty"`
	RawSchedulerID   string `json:"-"`
	SchedulerEnabled bool   `json:"scheduler_enabled"`
	TriggerEnabled   bool   `json:"trigger_enabled"`
	Topic            string `json:"topic,omitempty"`
	IntervalMs       int64  `json:"interval_ms,omitempty"`
	SpecJSON         string `json:"spec_json,omitempty"`
	NextFireAt       string `json:"next_fire_at,omitempty"`
	LastFiredAt      string `json:"last_fired_at,omitempty"`
	declarative      *agentcomposev2.TriggerSpec
	registered       map[string]any
}
