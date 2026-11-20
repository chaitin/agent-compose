package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

func TestAgentExecutorExecuteAgentRequestPersistsCellAndEvents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
		AgentTimeout:         2 * time.Second,
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "agent executor session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	runtime := &fakeAgentRuntime{}
	runner := NewAgentRunner(AgentRunnerDeps{
		Config:   config,
		Store:    store,
		ConfigDB: nil,
		Agents:   nil,
		Runtimes: fakeRuntimeProvider{runtime: runtime},
	})
	executor := NewAgentExecutor(config, store, nil, runner)

	cell, userEvent, assistantEvent, err := executor.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{
		Agent:   "codex",
		Message: "hello",
	})
	if err != nil {
		t.Fatalf("ExecuteAgentRequest returned error: %v", err)
	}
	if !cell.Success || cell.Type != execution.CellTypeAgent || cell.AgentThreadID != "agent-thread-1" {
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
	// The thread ID lives on the cell; no per-cell resume artifact is written.
	for _, name := range []string{"agent-thread.json", "agent-session.json"} {
		if _, err := os.Stat(filepath.Join(execution.HostSandboxDir(session), "state", "cells", cell.ID, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists or stat failed unexpectedly: %v", name, err)
		}
	}
	events, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("events = %#v, want user and assistant events", events)
	}
}

func TestAgentAssistantMessageExcludesTranscriptFallback(t *testing.T) {
	transcript := "$ command\ntool output"
	if got := agentAssistantMessage(domain.AgentRunResult{
		Agent:           "codex",
		FinalText:       transcript,
		FinalTextSource: domain.AgentFinalTextSourceTranscriptFallback,
		Transcript:      transcript,
		Success:         true,
	}); got != "" {
		t.Fatalf("fallback summary = %q, want empty", got)
	}
	if got := agentAssistantMessage(domain.AgentRunResult{
		Agent:           "codex",
		FinalText:       "final answer",
		FinalTextSource: domain.AgentFinalTextSourceProviderMessage,
		Transcript:      transcript + "\nfinal answer",
		Success:         true,
	}); got != "final answer" {
		t.Fatalf("provider summary = %q", got)
	}
}

func TestAgentExecutorStreamsOnlyHumanVisibleAgentOutput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
		AgentTimeout:         2 * time.Second,
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "agent stream session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	payload := execution.AgentResultPrefix + `{"provider":"codex","threadId":"agent-thread-1","finalText":"done","transcript":"scheduler agent transcript","stopReason":"completed"}`
	runtime := &fakeAgentRuntime{
		streamChunks: []domain.ExecChunk{
			{Text: payload},
			{Text: "stdout transcript\n" + payload},
			{Text: "scheduler agent transcript\n", Stream: domain.StdioStderr},
		},
		result: domain.ExecResult{Stdout: payload, Output: payload, ExitCode: 0, Success: true},
	}
	runner := NewAgentRunner(AgentRunnerDeps{
		Config:   config,
		Store:    store,
		ConfigDB: nil,
		Agents:   nil,
		Runtimes: fakeRuntimeProvider{runtime: runtime},
	})
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
	if chunks[1].Text != "scheduler agent transcript\n" || domain.NormalizeStdioStream(chunks[1].Stream) != domain.StdioStderr {
		t.Fatalf("stderr stream chunk = %#v", chunks[1])
	}
	for _, chunk := range chunks {
		if strings.Contains(chunk.Text, execution.AgentResultPrefix) {
			t.Fatalf("stream chunk leaked agent result payload: %#v", chunk)
		}
	}
	if !strings.Contains(cell.Stdout, "stdout transcript") || !strings.Contains(cell.Stderr, "scheduler agent transcript") {
		t.Fatalf("cell stdout/stderr = %q/%q", cell.Stdout, cell.Stderr)
	}
	if !strings.Contains(cell.Output, "stdout transcript") || !strings.Contains(cell.Output, "scheduler agent transcript") || strings.Contains(cell.Output, execution.AgentResultPrefix) {
		t.Fatalf("cell output = %q", cell.Output)
	}
}

func TestAgentExecutorPersistsFailedCellWhenStreamCallbackFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
		AgentTimeout:         2 * time.Second,
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "agent failed stream session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}

	callbackErr := errors.New("client stream closed")
	runtime := &fakeAgentRuntime{
		streamChunks: []domain.ExecChunk{{Text: "partial output\n", Stream: domain.StdioStdout}},
		result:       domain.ExecResult{Stdout: "partial output\n", Output: "partial output\n", ExitCode: 0, Success: true},
	}
	runner := NewAgentRunner(AgentRunnerDeps{
		Config:   config,
		Store:    store,
		ConfigDB: nil,
		Agents:   nil,
		Runtimes: fakeRuntimeProvider{runtime: runtime},
	})
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
