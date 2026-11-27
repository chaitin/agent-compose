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
			{Text: payload + "\n"},
			{Text: "stdout transcript\n" + payload + "\n"},
			{Text: "scheduler agent transcript\n", Stream: domain.StdioStderr},
			{Text: "quoted "},
			{Text: execution.AgentResultPrefix + "{}\n"},
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
	if len(chunks) != 4 {
		t.Fatalf("stream chunks = %#v", chunks)
	}
	if chunks[0].Text != "stdout transcript\n" || domain.NormalizeStdioStream(chunks[0].Stream) != domain.StdioStdout {
		t.Fatalf("stdout stream chunk = %#v", chunks[0])
	}
	if chunks[1].Text != "scheduler agent transcript\n" || domain.NormalizeStdioStream(chunks[1].Stream) != domain.StdioStderr {
		t.Fatalf("stderr stream chunk = %#v", chunks[1])
	}
	if chunks[2].Text+chunks[3].Text != "quoted "+execution.AgentResultPrefix+"{}\n" {
		t.Fatalf("quoted protocol marker was lost: %#v", chunks[2:])
	}
	for _, chunk := range chunks {
		if strings.Contains(chunk.Text, payload) {
			t.Fatalf("stream chunk leaked agent result payload: %#v", chunk)
		}
	}
	if !strings.Contains(cell.Stdout, "stdout transcript") || !strings.Contains(cell.Stderr, "scheduler agent transcript") {
		t.Fatalf("cell stdout/stderr = %q/%q", cell.Stdout, cell.Stderr)
	}
	if !strings.Contains(cell.Output, "stdout transcript") || !strings.Contains(cell.Output, "scheduler agent transcript") || strings.Contains(cell.Output, payload) {
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

func TestAgentExecutorRebuildsTranscriptFromAgentEvents(t *testing.T) {
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
	session, err := store.CreateSandbox(ctx, "agent events session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	events := `{"v":1,"seq":0,"type":"started","provider":"codex"}` + "\n" +
		`{"v":1,"seq":1,"type":"agent_event","event":{"kind":"text_delta","text":"Checking.\n"}}` + "\n" +
		`{"v":1,"seq":2,"type":"agent_event","event":{"kind":"tool_call","id":"c1","name":"shell","status":"completed","command":"ls"}}` + "\n" +
		`{"v":1,"seq":3,"type":"agent_event","event":{"kind":"tool_result","id":"c1","ok":true,"output":"a.txt\n"}}` + "\n" +
		`{"v":1,"seq":4,"type":"agent_event","event":{"kind":"text_delta","text":"All done."}}` + "\n" +
		`{"v":1,"seq":5,"type":"agent_turn_completed","finalText":"All done."}` + "\n"
	payload := `{"v":1,"seq":6,"type":"result","provider":"codex","threadId":"t","finalText":"All done.","transcript":"ignored","stopReason":"completed"}` + "\n"
	runtime := &fakeAgentRuntime{
		streamChunks: []domain.ExecChunk{
			{Text: events[:40]},
			{Text: events[40:] + payload},
			{Text: "warning: slow disk\n", Stream: domain.StdioStderr},
		},
		result: domain.ExecResult{Stdout: events + payload, Stderr: "warning: slow disk\n", Output: events + payload + "warning: slow disk\n", Success: true},
	}
	runner := NewAgentRunner(AgentRunnerDeps{Config: config, Store: store, Runtimes: fakeRuntimeProvider{runtime: runtime}})
	executor := NewAgentExecutor(config, store, nil, runner)
	var forwarded []string
	var frameKinds []string
	stream := execution.AgentExecutionStream{
		OnFrame: func(_ string, frame execution.AgentFrame) error {
			frameKinds = append(frameKinds, frame.Type)
			if frame.Type == "agent_event" {
				forwarded = append(forwarded, string(frame.Event))
			}
			return nil
		},
	}
	cell, _, _, err := executor.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{
		Agent:   "codex",
		Message: "hello",
		Stream:  stream,
	})
	if err != nil {
		t.Fatalf("ExecuteAgentRequest returned error: %v", err)
	}
	if len(forwarded) != 4 {
		t.Fatalf("forwarded events = %#v", forwarded)
	}
	if strings.Join(frameKinds, ",") != "started,agent_event,agent_event,agent_event,agent_event,agent_turn_completed,result" {
		t.Fatalf("lifecycle frames=%v", frameKinds)
	}
	if _, ok := runtime.specs[0].Env["AGENT_COMPOSE_RUNTIME_EVENTS"]; ok {
		t.Fatal("daemon still opts in to __AGENT_EVENT__ lines")
	}
	if got := runtime.specs[0].Env[execution.AgentOutputEnv]; got != "frames" {
		t.Fatalf("%s = %q, want frames", execution.AgentOutputEnv, got)
	}
	for _, want := range []string{"Checking.\n", "[tool:shell]\n$ ls\n", "a.txt\n", "All done.", "warning: slow disk"} {
		if !strings.Contains(cell.Output, want) {
			t.Fatalf("cell output %q missing %q", cell.Output, want)
		}
	}
	for _, leaked := range []string{`"type":"agent_event"`, execution.AgentResultPrefix} {
		if strings.Contains(cell.Output+cell.Stdout+cell.Stderr, leaked) {
			t.Fatalf("cell leaked %s: output=%q stdout=%q stderr=%q", leaked, cell.Output, cell.Stdout, cell.Stderr)
		}
	}
}
