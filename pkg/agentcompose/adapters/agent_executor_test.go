package adapters

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/sessionstore"
)

func TestAgentExecutorExecuteAgentRequestPersistsCellAndEvents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SessionStartTimeout:  2 * time.Second,
		AgentTimeout:         2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSession(ctx, "agent executor session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	runtime := &fakeAgentRuntime{}
	runner := NewAgentRunner(config, store, nil, nil, fakeRuntimeProvider{runtime: runtime})
	executor := NewAgentExecutor(config, store, nil, runner)

	cell, userEvent, assistantEvent, err := executor.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{
		Agent:   "codex",
		Message: "hello",
	})
	if err != nil {
		t.Fatalf("ExecuteAgentRequest returned error: %v", err)
	}
	if !cell.Success || cell.Type != execution.CellTypeAgent || cell.AgentSessionID != "agent-session-1" {
		t.Fatalf("cell = %#v", cell)
	}
	if userEvent.Type != "agent.user" || assistantEvent.Type != "agent.assistant" {
		t.Fatalf("events = %#v %#v", userEvent, assistantEvent)
	}
	cells, err := store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells) == 0 || cells[len(cells)-1].ID != cell.ID || !cells[len(cells)-1].Success {
		t.Fatalf("stored cells = %#v", cells)
	}
	events, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("events = %#v, want user and assistant events", events)
	}
}

func TestAgentExecutorStreamsOnlyHumanVisibleAgentOutput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SessionStartTimeout:  2 * time.Second,
		AgentTimeout:         2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSession(ctx, "agent stream session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	payload := execution.AgentResultPrefix + `{"provider":"codex","sessionId":"agent-session-1","finalText":"done","transcript":"loader agent transcript","stopReason":"completed"}`
	runtime := &fakeAgentRuntime{
		streamChunks: []domain.ExecChunk{
			{Text: payload},
			{Text: "stdout transcript\n" + payload},
			{Text: "loader agent transcript\n", Stream: domain.StdioStderr},
		},
		result: domain.ExecResult{Stdout: payload, Output: payload, ExitCode: 0, Success: true},
	}
	runner := NewAgentRunner(config, store, nil, nil, fakeRuntimeProvider{runtime: runtime})
	executor := NewAgentExecutor(config, store, nil, runner)
	var chunks []domain.ExecChunk

	cell, _, _, err := executor.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{
		Agent:   "codex",
		Message: "hello",
		Stream: execution.AgentExecutionStream{
			OnChunk: func(_ string, chunk domain.ExecChunk) error {
				chunks = append(chunks, chunk)
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteAgentRequest returned error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("stream chunks = %#v", chunks)
	}
	if chunks[0].Text != "stdout transcript\n" || domain.NormalizeStdioStream(chunks[0].Stream) != domain.StdioStdout {
		t.Fatalf("stdout stream chunk = %#v", chunks[0])
	}
	if chunks[1].Text != "loader agent transcript\n" || domain.NormalizeStdioStream(chunks[1].Stream) != domain.StdioStderr {
		t.Fatalf("stderr stream chunk = %#v", chunks[1])
	}
	for _, chunk := range chunks {
		if strings.Contains(chunk.Text, execution.AgentResultPrefix) {
			t.Fatalf("stream chunk leaked agent result payload: %#v", chunk)
		}
	}
	if !strings.Contains(cell.Stdout, "stdout transcript") || !strings.Contains(cell.Stderr, "loader agent transcript") {
		t.Fatalf("cell stdout/stderr = %q/%q", cell.Stdout, cell.Stderr)
	}
	if !strings.Contains(cell.Output, "stdout transcript") || !strings.Contains(cell.Output, "loader agent transcript") || strings.Contains(cell.Output, execution.AgentResultPrefix) {
		t.Fatalf("cell output = %q", cell.Output)
	}
}

func TestAgentExecutorPersistsFailedCellWhenStreamCallbackFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SessionStartTimeout:  2 * time.Second,
		AgentTimeout:         2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSession(ctx, "agent failed stream session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}

	callbackErr := errors.New("client stream closed")
	runtime := &fakeAgentRuntime{
		streamChunks: []domain.ExecChunk{{Text: "partial output\n", Stream: domain.StdioStdout}},
		result:       domain.ExecResult{Stdout: "partial output\n", Output: "partial output\n", ExitCode: 0, Success: true},
	}
	runner := NewAgentRunner(config, store, nil, nil, fakeRuntimeProvider{runtime: runtime})
	executor := NewAgentExecutor(config, store, nil, runner)

	cell, userEvent, assistantEvent, err := executor.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{
		Agent:   "codex",
		Message: "hello",
		Stream: execution.AgentExecutionStream{
			OnChunk: func(string, domain.ExecChunk) error {
				return callbackErr
			},
		},
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("ExecuteAgentRequest error = %v, want %v", err, callbackErr)
	}
	if cell.Success || cell.Running || cell.ExitCode == 0 {
		t.Fatalf("failed cell = %#v", cell)
	}
	if !strings.Contains(cell.Output, "partial output") {
		t.Fatalf("failed cell output = %q", cell.Output)
	}
	if userEvent.Type != "agent.user" || assistantEvent.Type != "agent.assistant.failed" || assistantEvent.Level != "error" {
		t.Fatalf("events = %#v %#v", userEvent, assistantEvent)
	}
	cells, err := store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells) == 0 || cells[len(cells)-1].ID != cell.ID || cells[len(cells)-1].Success {
		t.Fatalf("stored cells = %#v", cells)
	}
	events, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if len(events) < 2 || events[len(events)-1].Type != "agent.assistant.failed" {
		t.Fatalf("events = %#v, want failed assistant event", events)
	}
}
