package runs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// fakeAgentResumer is an executor that only resumes detached agent runs.
type fakeAgentResumer struct {
	fakeControllerExecutor
	resumed execution.ResumeAgentRequest
	cell    domain.NotebookCell
	err     error
}

func (e *fakeAgentResumer) ResumeAgentRequest(_ context.Context, _ *domain.Sandbox, req execution.ResumeAgentRequest) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error) {
	e.resumed = req
	return e.cell, domain.SandboxEvent{}, domain.SandboxEvent{ID: "assistant", Message: "done"}, e.err
}

func newAgentReattachTestController(t *testing.T, resumer *fakeAgentResumer) (*Controller, domain.ProjectRunRecord) {
	t.Helper()
	controller, run := newReattachTestController(t, nil)
	controller.executor = resumer
	run.DetachKind = domain.DetachKindAgent
	run.LogsPath = filepath.Join(t.TempDir(), "sandbox", "state", "cells", "cell-9", "output.txt")
	if _, err := controller.configDB.UpdateProjectRun(context.Background(), run); err != nil {
		t.Fatalf("UpdateProjectRun: %v", err)
	}
	return controller, run
}

func TestReattachDetachedAgentRunResumesItsCell(t *testing.T) {
	resumer := &fakeAgentResumer{cell: domain.NotebookCell{ID: "cell-9", Type: execution.CellTypeAgent, Output: "done", Success: true}}
	controller, run := newAgentReattachTestController(t, resumer)

	got, err := controller.ReattachDetachedRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun: %v", err)
	}
	if resumer.resumed.CellID != "cell-9" || resumer.resumed.ExecutionID != "exec-1" || resumer.resumed.RunID != "run-1" {
		t.Fatalf("resume request = %+v", resumer.resumed)
	}
	if got.Status != domain.ProjectRunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", got.Status)
	}
}

func TestReattachDetachedAgentRunFailsWithTheCell(t *testing.T) {
	resumer := &fakeAgentResumer{cell: domain.NotebookCell{ID: "cell-9", Type: execution.CellTypeAgent, ExitCode: 1}, err: errors.New("agent exited 1")}
	controller, run := newAgentReattachTestController(t, resumer)

	got, err := controller.ReattachDetachedRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun: %v", err)
	}
	if got.Status != domain.ProjectRunStatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	// The re-attach is over and the run is finalized, so the tokens kept for
	// it must be retired.
	store := controller.configDB.(*fakeControllerStore)
	if len(store.deletedFacadeTokens) != 1 || store.deletedFacadeTokens[0] != "run-1|agent" {
		t.Fatalf("deleted facade tokens = %v, want the retained agent tokens", store.deletedFacadeTokens)
	}
}

// ResumeAgentRequest can fail before the runner starts following the journal
// (VM state, runtime, locating the cell). The run is still finalized as failed,
// so its retained tokens must be retired here too.
func TestReattachDetachedAgentRunDeletesTokensWhenResumeFailsEarly(t *testing.T) {
	resumer := &fakeAgentResumer{err: errors.New("agent cell cell-9 not found")}
	controller, run := newAgentReattachTestController(t, resumer)

	got, err := controller.ReattachDetachedRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun: %v", err)
	}
	if got.Status != domain.ProjectRunStatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	store := controller.configDB.(*fakeControllerStore)
	if len(store.deletedFacadeTokens) != 1 || store.deletedFacadeTokens[0] != "run-1|agent" {
		t.Fatalf("deleted facade tokens = %v, want the retained agent tokens", store.deletedFacadeTokens)
	}
}

func TestReattachDetachedAgentRunStaysRunningOnDisconnect(t *testing.T) {
	resumer := &fakeAgentResumer{err: ErrRunDisconnected}
	controller, run := newAgentReattachTestController(t, resumer)

	got, err := controller.ReattachDetachedRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun: %v", err)
	}
	if got.Status != domain.ProjectRunStatusRunning {
		t.Fatalf("status = %q, want running", got.Status)
	}
	if store := controller.configDB.(*fakeControllerStore); len(store.deletedFacadeTokens) != 0 {
		t.Fatalf("deleted facade tokens = %v; a run that is still running keeps them", store.deletedFacadeTokens)
	}
}

// An unconfirmed termination may mean the task is still running in the guest,
// so the tokens it uses stay.
func TestReattachDetachedAgentRunKeepsTokensWhenTerminationIsUnconfirmed(t *testing.T) {
	resumer := &fakeAgentResumer{err: domain.ErrExecTerminationUnconfirmed}
	controller, run := newAgentReattachTestController(t, resumer)

	if _, err := controller.ReattachDetachedRun(context.Background(), run); err != nil {
		t.Fatalf("ReattachDetachedRun: %v", err)
	}
	if store := controller.configDB.(*fakeControllerStore); len(store.deletedFacadeTokens) != 0 {
		t.Fatalf("deleted facade tokens = %v; an unconfirmed termination may still be running", store.deletedFacadeTokens)
	}
}

func TestReattachDetachedAgentRunWithoutCellFails(t *testing.T) {
	resumer := &fakeAgentResumer{}
	controller, run := newAgentReattachTestController(t, resumer)
	run.LogsPath = ""

	got, err := controller.ReattachDetachedRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun: %v", err)
	}
	if got.Status != domain.ProjectRunStatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if resumer.resumed.CellID != "" {
		t.Fatal("resume must not be attempted without a cell")
	}
}

func TestAgentCellIDFromLogsPath(t *testing.T) {
	for path, want := range map[string]string{
		"/data/sandboxes/sb/state/cells/abc/output.txt": "abc",
		"/data/runs/run-1/transcript.log":               "",
		"/data/sandboxes/sb/state/other/abc/output.txt": "",
		"": "",
	} {
		if got := agentCellIDFromLogsPath(path); got != want {
			t.Fatalf("agentCellIDFromLogsPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestAgentRunDetachRecordsAgentKindOnTheStoredRun(t *testing.T) {
	controller, run := newReattachTestController(t, nil)
	controller.detachCache = &detachCapabilityCache{}
	controller.runtime = func(*domain.Sandbox) (Runtime, error) {
		return &detachRecordingRuntime{probeOutput: `{"detach":true}` + "\n"}, nil
	}
	sandbox, err := controller.store.GetSandbox(context.Background(), run.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox: %v", err)
	}
	// The cell has started since the snapshot was taken.
	stored := run
	stored.LogsPath = "/data/sandboxes/sb/state/cells/cell-1/output.txt"
	if _, err := controller.configDB.UpdateProjectRun(context.Background(), stored); err != nil {
		t.Fatalf("UpdateProjectRun: %v", err)
	}
	run.Detach, run.ExecutionID = false, ""

	detach := controller.agentRunDetach(context.Background(), sandboxedProjectRun{TransitionCtx: context.Background(), Run: run, Sandbox: sandbox})
	if detach == nil || detach.ExecutionID == "" {
		t.Fatalf("detach = %+v, want an execution", detach)
	}
	deadline := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	if err := detach.OnStart(deadline); err != nil {
		t.Fatalf("OnStart: %v", err)
	}
	got, err := controller.configDB.GetProjectRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("GetProjectRun: %v", err)
	}
	if !got.Detach || got.DetachKind != domain.DetachKindAgent || got.ExecutionID != detach.ExecutionID || !got.Deadline.Equal(deadline) {
		t.Fatalf("stored run = %+v", got)
	}
	if got.LogsPath != stored.LogsPath {
		t.Fatalf("logs path = %q, want %q (the stored record must not be overwritten)", got.LogsPath, stored.LogsPath)
	}
}

func TestAgentRunDetachKeepsLegacyGuestsDirect(t *testing.T) {
	controller, run := newReattachTestController(t, nil)
	controller.detachCache = &detachCapabilityCache{}
	controller.runtime = func(*domain.Sandbox) (Runtime, error) {
		return &detachRecordingRuntime{probeOutput: "unknown command\n"}, nil
	}
	sandbox, _ := controller.store.GetSandbox(context.Background(), run.SandboxID)
	if detach := controller.agentRunDetach(context.Background(), sandboxedProjectRun{Run: run, Sandbox: sandbox}); detach != nil {
		t.Fatalf("detach = %+v, want nil for a legacy guest", detach)
	}
}

func TestDetachFollowerDoesNotReforwardDeliveredOutput(t *testing.T) {
	var forwarded []string
	f := newDetachFollower(func(chunk domain.ExecChunk) { forwarded = append(forwarded, chunk.Text) }, nil)
	f.forwardFrom = 5
	f.consume(domain.ExecChunk{Stream: domain.StdioStdout, Text: `{"t":"out","s":"stdout","text":"abc","offset":0}` + "\n" +
		`{"t":"out","s":"stdout","text":"defg","offset":3}` + "\n" +
		`{"t":"out","s":"stdout","text":"hi","offset":7}` + "\n" +
		`{"t":"exit","code":0,"reason":"completed"}` + "\n"})
	res := f.result(nil, context.Background())
	if res.Result.Stdout != "abcdefghi" {
		t.Fatalf("stdout = %q, want the whole replay for parsing", res.Result.Stdout)
	}
	if len(forwarded) != 2 || forwarded[0] != "fg" || forwarded[1] != "hi" {
		t.Fatalf("forwarded = %q, want only output past offset 5", forwarded)
	}
}
