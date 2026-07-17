package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
)

type composePSSandboxOutput struct {
	Kind           string `json:"kind,omitempty"`
	RuntimeID      string `json:"runtime_id,omitempty"`
	SandboxID      string `json:"sandbox_id"`
	RawID          string `json:"-"`
	SandboxShortID string `json:"sandbox_short_id"`
	Agent          string `json:"agent,omitempty"`
	Status         string `json:"status"`
	RunID          string `json:"run_id,omitempty"`
	RunShortID     string `json:"run_short_id,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	Driver         string `json:"driver,omitempty"`
	Image          string `json:"image,omitempty"`
	Workspace      string `json:"workspace,omitempty"`
}

func countProjectDownFailedSandboxStops(changes []*agentcomposev2.ProjectChange) int {
	count := 0
	for _, change := range changes {
		if change.GetAction() == agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED &&
			change.GetResourceType() == "sandbox" &&
			strings.TrimSpace(change.GetMessage()) != "" {
			count++
		}
	}
	return count
}

func composePSSessionBelongsToProject(session *agentcomposev2.Sandbox, project *agentcomposev2.Project, runsBySandbox map[string]*agentcomposev2.RunSummary) bool {
	projectID := strings.TrimSpace(project.GetSummary().GetProjectId())
	projectName := strings.TrimSpace(project.GetSummary().GetName())
	sourcePath := strings.TrimSpace(project.GetSummary().GetSourcePath())
	if run := runsBySandbox[session.GetSandboxId()]; run != nil {
		if strings.TrimSpace(run.GetProjectId()) == projectID {
			return true
		}
	}
	tags := sessionTagsMap(session.GetTags())
	for _, key := range []string{"project", "project_id"} {
		if value := strings.TrimSpace(tags[key]); value != "" && (value == projectID || value == projectName || value == sourcePath) {
			return true
		}
	}
	if legacySchedulerSandboxBelongsToProject(tags, project) {
		return true
	}
	if value := strings.TrimSpace(session.GetTriggerSource()); value != "" {
		value = strings.ToLower(value)
		return (projectID != "" && strings.Contains(value, strings.ToLower(projectID))) ||
			(projectName != "" && strings.Contains(value, strings.ToLower(projectName)))
	}
	return false
}

func sessionTagsMap(items []*agentcomposev2.SandboxTag) map[string]string {
	result := make(map[string]string, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.GetName())
		if name == "" {
			continue
		}
		result[name] = strings.TrimSpace(item.GetValue())
	}
	return result
}

func composeStatsOutputForSandbox(ctx context.Context, client agentcomposev2connect.SandboxServiceClient, sandboxID string) (composeStatsOutput, error) {
	resp, err := client.GetSandboxStats(ctx, connect.NewRequest(&agentcomposev2.GetSandboxStatsRequest{SandboxId: sandboxID}))
	if err != nil {
		return composeStatsOutput{}, err
	}
	return composeStatsOutputFromProto(resp.Msg.GetStats()), nil
}

func resolveComposeSandboxRefFromSessions(ctx context.Context, client agentcomposev2connect.SandboxServiceClient, ref string) (string, error) {
	sessions, err := listAllSandboxes(ctx, client)
	if err != nil {
		return "", commandExitErrorForConnect(fmt.Errorf("resolve sandbox %s from daemon sessions: %w", ref, err))
	}
	matches := map[string]struct{}{}
	for _, session := range sessions {
		id := strings.TrimSpace(session.GetSandboxId())
		if id != "" && resourceIDMatchesRef(id, shortOpaqueID(id), ref) {
			matches[id] = struct{}{}
		}
	}
	if len(matches) == 0 {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox %q not found in daemon sessions", ref)}
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for id := range matches {
			ids = append(ids, shortOpaqueID(id))
		}
		sort.Strings(ids)
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox ref %q is ambiguous; matches: %s", ref, strings.Join(ids, ", "))}
	}
	for id := range matches {
		return id, nil
	}
	return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox %q not found in daemon sessions", ref)}
}
