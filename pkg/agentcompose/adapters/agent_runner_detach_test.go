package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

// detachAgentRuntime plays the guest side of a detached agent run: `start`
// succeeds, `attach` replays journal records (or blocks until cancelled), and
// every exec spec is recorded.
type detachAgentRuntime struct {
	fakeAgentRuntime
	mu           sync.Mutex
	execs        []domain.ExecSpec
	journal      []string
	attachBlocks bool
}

func (r *detachAgentRuntime) ExecStream(ctx context.Context, _ *domain.Sandbox, _ domain.VMState, spec domain.ExecSpec, stream domain.ExecStreamWriter) (domain.ExecResult, error) {
	r.mu.Lock()
	r.execs = append(r.execs, spec)
	r.mu.Unlock()
	if len(spec.Args) > 0 && spec.Args[0] == "attach" {
		if r.attachBlocks {
			<-ctx.Done()
			return domain.ExecResult{}, ctx.Err()
		}
		for _, line := range r.journal {
			stream(domain.ExecChunk{Stream: domain.StdioStdout, Text: line + "\n"})
		}
	}
	return domain.ExecResult{}, nil
}

func (r *detachAgentRuntime) subcommands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var names []string
	for _, spec := range r.execs {
		if spec.Command == "sh" {
			names = append(names, "start")
			continue
		}
		if len(spec.Args) > 0 {
			names = append(names, spec.Args[0])
		}
	}
	return names
}

func journalLines(t *testing.T, chunks ...string) []string {
	t.Helper()
	var lines []string
	offset := 0
	for _, chunk := range chunks {
		data, err := json.Marshal(map[string]any{"t": "out", "s": "stdout", "text": chunk, "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
		offset += len(chunk)
	}
	return append(lines, `{"t":"exit","code":0,"reason":"completed"}`)
}

const detachAgentPayload = `{"provider":"claude","threadId":"t-1","finalText":"done","transcript":"trace","stopReason":"completed"}`

type detachRunnerFixture struct {
	runner   *AgentRunner
	configDB *configstore.ConfigStore
	store    *sandboxstore.Store
	session  *domain.Sandbox
	runtime  *detachAgentRuntime
}

func newDetachRunnerFixture(t *testing.T, runtime *detachAgentRuntime) detachRunnerFixture {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:            root,
		DbAddr:              filepath.Join(root, "data.db"),
		SandboxRoot:         filepath.Join(root, "sandboxes"),
		RuntimeDriver:       driverpkg.RuntimeDriverDocker,
		DefaultImage:        "guest:latest",
		GuestWorkspacePath:  "/workspace",
		GuestStateRoot:      "/data/state",
		GuestRuntimeRoot:    "/data/runtime",
		GuestHomePath:       "/root",
		RuntimeBaseURL:      "http://agent-compose.test:7410",
		LLMAPIKey:           "provider-key",
		SandboxStartTimeout: 2 * time.Second,
	}
	configDB, store, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("OpenStores: %v", err)
	}
	if err := configDB.UpsertDefaultLLMConfig(context.Background(), llms.Provider{
		ID: "anthropic-primary", Name: "Anthropic", ProviderType: llms.ProviderFamilyAnthropic,
		BaseURL: "https://anthropic.upstream.test", APIKey: "anthropic-upstream-secret", Scope: llms.ProviderScopeSystem,
	}, llms.Model{ID: "claude-agent", Name: "claude-agent", DefaultModel: true, Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("seed llm catalog: %v", err)
	}
	session, err := store.CreateSandbox(context.Background(), "detach agent", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(context.Background(), session); err != nil {
		t.Fatalf("UpdateSandbox: %v", err)
	}
	if err := store.SaveVMState(session.Summary.ID, domain.VMState{Driver: driverpkg.RuntimeDriverDocker, BoxID: "container-1"}); err != nil {
		t.Fatalf("SaveVMState: %v", err)
	}
	runner := NewAgentRunner(AgentRunnerDeps{Config: config, Store: store, ConfigDB: configDB, Runtimes: fakeRuntimeProvider{runtime: runtime}})
	return detachRunnerFixture{runner: runner, configDB: configDB, store: store, session: session, runtime: runtime}
}

func (f detachRunnerFixture) startSpecToken(t *testing.T) string {
	t.Helper()
	for _, spec := range f.runtime.execs {
		if spec.Command == "sh" {
			return spec.Env["AGENT_COMPOSE_SANDBOX_TOKEN"]
		}
	}
	t.Fatal("no start exec recorded")
	return ""
}

func TestAgentRunnerDetachedRunStartsSupervisorAndParsesJournal(t *testing.T) {
	runtime := &detachAgentRuntime{}
	runtime.journal = journalLines(t, "thinking\n", execution.AgentResultPrefix+detachAgentPayload+"\n")
	f := newDetachRunnerFixture(t, runtime)
	var started time.Time
	startedCalled := false
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	_, parsed, err := f.runner.ExecuteAgentRun(ctx, AgentRunRequest{
		Session: f.session, Agent: "claude", RunID: "run-1", Message: "hi",
		Detach: &AgentRunDetach{
			ExecutionID: "exec-1",
			CursorPath:  filepath.Join(t.TempDir(), "cursor"),
			OnStart:     func(deadline time.Time) error { started, startedCalled = deadline, true; return nil },
		},
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteAgentRun: %v", err)
	}
	if parsed.FinalText != "done" || !parsed.Success {
		t.Fatalf("parsed = %+v", parsed)
	}
	if !startedCalled || started.IsZero() {
		t.Fatal("OnStart was not called with the run deadline before the task started")
	}
	if got := strings.Join(f.runtime.subcommands(), ","); got != "start,attach,cleanup" {
		t.Fatalf("guest execs = %s, want start,attach,cleanup", got)
	}
	start := strings.Join(f.runtime.execs[0].Args, " ")
	for _, want := range []string{"'start' '--detach'", "'--execution-id' 'exec-1'", "'--task-timeout-ms'", "'--' 'prompt' '--provider' 'claude'"} {
		if !strings.Contains(start, want) {
			t.Fatalf("start args %q missing %q", start, want)
		}
	}
	token := f.startSpecToken(t)
	if _, err := f.configDB.GetLLMFacadeToken(context.Background(), token); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("facade token after a finished detached run: err = %v, want not found", err)
	}
}

func TestAgentRunnerDetachedRunKeepsTokenWhenDisconnected(t *testing.T) {
	runtime := &detachAgentRuntime{attachBlocks: true}
	f := newDetachRunnerFixture(t, runtime)
	shutdown := make(chan struct{})
	ctx, cancel := context.WithCancelCause(runs.WithDaemonShutdown(context.Background(), shutdown))
	defer cancel(nil)

	done := make(chan error, 1)
	go func() {
		_, _, err := f.runner.ExecuteAgentRun(ctx, AgentRunRequest{
			Session: f.session, Agent: "claude", RunID: "run-1", Message: "hi",
			Detach: &AgentRunDetach{ExecutionID: "exec-1", CursorPath: filepath.Join(t.TempDir(), "cursor")},
		}, nil)
		done <- err
	}()
	waitForSubcommand(t, runtime, "attach")
	cancel(runs.ErrRunDisconnected)
	close(shutdown)

	if err := <-done; !errors.Is(err, runs.ErrRunDisconnected) {
		t.Fatalf("err = %v, want ErrRunDisconnected", err)
	}
	token := f.startSpecToken(t)
	if _, err := f.configDB.GetLLMFacadeToken(context.Background(), token); err != nil {
		t.Fatalf("facade token was removed while the detached agent still runs: %v", err)
	}
	for _, name := range runtime.subcommands() {
		if name == "cleanup" || name == "stop" {
			t.Fatalf("guest got %q on disconnect; the task must be left alone", name)
		}
	}
}

func TestAgentRunnerResumeReplaysJournalAndRetiresToken(t *testing.T) {
	runtime := &detachAgentRuntime{}
	payload := execution.AgentResultPrefix + detachAgentPayload + "\n"
	runtime.journal = journalLines(t, "before restart\n", payload)
	f := newDetachRunnerFixture(t, runtime)
	tokenHash, _ := llms.HashFacadeToken("raw-run-1")
	if err := f.configDB.SaveLLMFacadeToken(context.Background(), llms.FacadeToken{
		TokenHash: tokenHash, SandboxID: f.session.Summary.ID, Source: "agent", RunID: "run-1",
	}); err != nil {
		t.Fatalf("SaveLLMFacadeToken: %v", err)
	}
	cursorPath := filepath.Join(t.TempDir(), "cursor")
	// The earlier daemon delivered the first record and the result payload.
	if err := runs.WriteDetachCursorFile(cursorPath, uint64(len("before restart\n")+len(payload))); err != nil {
		t.Fatal(err)
	}
	var forwarded []string
	_, parsed, err := f.runner.ResumeAgentRun(context.Background(), AgentResumeRequest{
		Session: f.session, Agent: "claude", RunID: "run-1",
		Detach: &AgentRunDetach{ExecutionID: "exec-1", CursorPath: cursorPath},
	}, func(chunk domain.ExecChunk) { forwarded = append(forwarded, chunk.Text) })
	if err != nil {
		t.Fatalf("ResumeAgentRun: %v", err)
	}
	if parsed.FinalText != "done" {
		t.Fatalf("parsed = %+v; the payload before the cursor must still be parsed", parsed)
	}
	if len(forwarded) != 0 {
		t.Fatalf("forwarded %q; output before the cursor must not be delivered again", forwarded)
	}
	attach := strings.Join(runtime.execs[0].Args, " ")
	if !strings.Contains(attach, "--from 0") {
		t.Fatalf("attach args %q, want a replay from 0", attach)
	}
	if _, err := f.configDB.GetLLMFacadeToken(context.Background(), "raw-run-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("run token after resume: err = %v, want not found", err)
	}
}

func waitForSubcommand(t *testing.T, runtime *detachAgentRuntime, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, seen := range runtime.subcommands() {
			if seen == name {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("guest never got %q", name)
}

func TestAgentExecutorResumeFinishesTheRunningCell(t *testing.T) {
	runtime := &detachAgentRuntime{}
	payload := execution.AgentResultPrefix + detachAgentPayload + "\n"
	runtime.journal = journalLines(t, "before\n", "after\n", payload)
	f := newDetachRunnerFixture(t, runtime)
	executor := NewAgentExecutor(f.runner.config, f.store, nil, f.runner)
	cell := domain.NotebookCell{ID: "cell-9", Type: execution.CellTypeAgent, Source: "hi", Agent: "claude", Running: true, Stdout: "before\n", Output: "before\n"}
	if err := f.store.AddCell(context.Background(), f.session, cell); err != nil {
		t.Fatalf("AddCell: %v", err)
	}
	hostCellDir := filepath.Join(execution.HostSandboxDir(f.session), "state", "cells", "cell-9")
	if err := runs.WriteDetachCursorFile(agentCellCursorPath(hostCellDir), uint64(len("before\n"))); err != nil {
		t.Fatal(err)
	}
	var chunks []string
	finished, _, assistant, err := executor.ResumeAgentRequest(context.Background(), f.session, execution.ResumeAgentRequest{
		RunID: "run-1", CellID: "cell-9", ExecutionID: "exec-1",
		Stream: execution.AgentExecutionStream{OnChunk: func(_ string, chunk domain.ExecChunk) error {
			chunks = append(chunks, chunk.Text)
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("ResumeAgentRequest: %v", err)
	}
	if finished.Running || !finished.Success || finished.AgentThreadID != "t-1" {
		t.Fatalf("cell = %+v", finished)
	}
	if assistant.Message != "done" {
		t.Fatalf("assistant event = %+v", assistant)
	}
	if strings.Join(chunks, "") != "after\n" {
		t.Fatalf("chunks = %q, want only the output after the cursor", chunks)
	}
	cells, err := f.store.ListCells(context.Background(), f.session.Summary.ID)
	if err != nil || len(cells) != 1 || cells[0].Running {
		t.Fatalf("stored cells = %+v, err = %v", cells, err)
	}
}

func TestAgentExecutorLeavesCellRunningOnDisconnect(t *testing.T) {
	runtime := &detachAgentRuntime{attachBlocks: true}
	f := newDetachRunnerFixture(t, runtime)
	executor := NewAgentExecutor(f.runner.config, f.store, nil, f.runner)
	shutdown := make(chan struct{})
	ctx, cancel := context.WithCancelCause(runs.WithDaemonShutdown(context.Background(), shutdown))
	defer cancel(nil)

	type outcome struct {
		cell domain.NotebookCell
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		cell, _, _, err := executor.ExecuteAgentRequest(ctx, f.session, execution.ExecuteAgentRequest{
			Agent: "claude", RunID: "run-1", Message: "hi",
			Detach: &execution.AgentDetach{ExecutionID: "exec-1"},
		})
		done <- outcome{cell, err}
	}()
	waitForSubcommand(t, runtime, "attach")
	cancel(runs.ErrRunDisconnected)
	close(shutdown)

	got := <-done
	if !errors.Is(got.err, runs.ErrRunDisconnected) {
		t.Fatalf("err = %v, want ErrRunDisconnected", got.err)
	}
	cells, err := f.store.ListCells(context.Background(), f.session.Summary.ID)
	if err != nil || len(cells) != 1 || !cells[0].Running {
		t.Fatalf("stored cells = %+v, err = %v; the cell must stay running for resume", cells, err)
	}
	events, err := f.store.ListEvents(context.Background(), f.session.Summary.ID)
	if err == nil {
		for _, event := range events {
			if event.Type == "agent.assistant.failed" {
				t.Fatalf("a disconnect recorded a failure event: %+v", event)
			}
		}
	}
}
