package projects

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

const agentSystemPromptFileName = "system-prompt.txt"

func TestRunServiceProjectRunWritesSystemPromptFromManagedAgent(t *testing.T) {
	spec := newProjectServiceTestSpec("demo", "gpt-test")
	spec.Agents[0].SystemPrompt = "Reply only in project runs"
	store, service, projectID := setupRunPreparationProject(t, spec, t.TempDir())
	client, closeServer := newRunServiceTestClient(t, service)
	defer closeServer()
	ctx := context.Background()

	events, err := collectRunAgentStreamEvents(ctx, client, &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "review with system prompt",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
		ClientRequestId: "system-prompt-run-request",
	})
	if err != nil {
		t.Fatalf("RunAgentStream returned error: %v", err)
	}
	completed := lastRunAgentStreamEvent(events, agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED)
	if completed == nil {
		t.Fatalf("RunAgentStream events missing completion: %#v", events)
	}
	stored, err := store.GetProjectRun(ctx, completed.GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	if stored.SessionID == "" {
		t.Fatalf("stored run missing session id: %#v", stored)
	}

	hostPath := filepath.Join(service.config.SessionRoot, stored.SessionID, "state", "agents", "system-prompts", agentSystemPromptFileName)
	content, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", hostPath, err)
	}
	if string(content) != "Reply only in project runs" {
		t.Fatalf("system prompt file content = %q", string(content))
	}
}
