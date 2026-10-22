package runs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

func TestReattachDetachedRunIgnoresNonDetachRun(t *testing.T) {
	controller := &Controller{}
	run := domain.ProjectRunRecord{RunID: "run-1", Detach: false}
	got, err := controller.ReattachDetachedRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun returned error: %v", err)
	}
	if got.RunID != "run-1" {
		t.Fatalf("run changed: %+v", got)
	}
}

func TestReattachDetachedRunMarksFailedWhenSandboxNotRunning(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := &config.Config{
		DataRoot:      root,
		SandboxRoot:   filepath.Join(root, "sandboxes"),
		RuntimeDriver: driverpkg.RuntimeDriverK8s,
		DefaultImage:  "guest:latest",
	}
	store, err := sandboxstore.NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	sandbox, err := store.CreateSandbox(ctx, "re-attach", "", driverpkg.RuntimeDriverK8s, "guest:latest", "", domain.SandboxTypeScript, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	configDB := &fakeControllerStore{runs: map[string]domain.ProjectRunRecord{}}
	run := domain.ProjectRunRecord{RunID: "run-1", ProjectID: "project-1", SandboxID: sandbox.Summary.ID, Detach: true, DetachKind: domain.DetachKindAgent, ExecutionID: "exec-1", Status: domain.ProjectRunStatusRunning, CleanupPolicy: domain.ProjectRunCleanupKeepRunning}
	if _, err := configDB.CreateProjectRun(ctx, run); err != nil {
		t.Fatalf("CreateProjectRun returned error: %v", err)
	}
	controller := &Controller{
		config:   cfg,
		store:    store,
		configDB: configDB,
		runtime:  func(*domain.Sandbox) (Runtime, error) { return &fakeControllerRuntime{}, nil },
	}
	// The sandbox is still pending, so re-attach must fail the run rather than
	// hang on a follow.
	got, err := controller.ReattachDetachedRun(ctx, run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun returned error: %v", err)
	}
	if got.Status != domain.ProjectRunStatusFailed {
		t.Fatalf("run status = %q, want failed", got.Status)
	}
	if len(configDB.deletedFacadeTokens) != 1 || configDB.deletedFacadeTokens[0] != "run-1|agent" {
		t.Fatalf("deleted facade tokens = %v, want the retained agent tokens of the failed run", configDB.deletedFacadeTokens)
	}
}

type reattachFakeRuntime struct {
	fakeControllerRuntime
	records []string
}

func (r *reattachFakeRuntime) ExecStream(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, spec domain.ExecSpec, writer domain.ExecStreamWriter) (domain.ExecResult, error) {
	if len(spec.Args) > 0 && spec.Args[0] == "attach" {
		for _, record := range r.records {
			writer(domain.ExecChunk{Text: record + "\n"})
		}
		return domain.ExecResult{}, nil
	}
	return r.fakeControllerRuntime.ExecStream(ctx, session, vmState, spec, writer)
}

// newReattachTestController builds a controller whose runtime replays records
// as the guest `attach` output, plus a detached run to re-attach.
func newReattachTestController(t *testing.T, records []string) (*Controller, domain.ProjectRunRecord) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	cfg := &config.Config{
		DataRoot:           root,
		SandboxRoot:        filepath.Join(root, "sandboxes"),
		RuntimeDriver:      driverpkg.RuntimeDriverK8s,
		DefaultImage:       "guest:latest",
		GuestWorkspacePath: "/workspace",
		GuestStateRoot:     "/state",
		GuestHomePath:      "/root",
	}
	store, err := sandboxstore.NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	sandbox, err := store.CreateSandbox(ctx, "re-attach", "", driverpkg.RuntimeDriverK8s, "guest:latest", "", domain.SandboxTypeScript, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	if err := store.SaveVMState(sandbox.Summary.ID, domain.VMState{Driver: driverpkg.RuntimeDriverK8s, BoxID: sandbox.Summary.ID}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	configDB := &fakeControllerStore{runs: map[string]domain.ProjectRunRecord{}}
	run := domain.ProjectRunRecord{RunID: "run-1", ProjectID: "project-1", SandboxID: sandbox.Summary.ID, Detach: true, ExecutionID: "exec-1", Status: domain.ProjectRunStatusRunning, CleanupPolicy: domain.ProjectRunCleanupKeepRunning}
	if _, err := configDB.CreateProjectRun(ctx, run); err != nil {
		t.Fatalf("CreateProjectRun returned error: %v", err)
	}
	controller := &Controller{
		config:   cfg,
		store:    store,
		configDB: configDB,
		runtime: func(*domain.Sandbox) (Runtime, error) {
			return &reattachFakeRuntime{records: records}, nil
		},
	}
	return controller, run
}

// attachOutRecord encodes text as a journal `out` record at offset.
func attachOutRecord(t *testing.T, text string, offset uint64) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"t": "out", "s": "stdout", "text": text, "offset": offset})
	if err != nil {
		t.Fatalf("marshal out record: %v", err)
	}
	return string(data)
}

func TestReattachDetachedRunFollowsJournalAndCompletes(t *testing.T) {
	// Cursor 5 means the daemon already consumed offsets 0..5; the tail resumes
	// at offset 5 without duplicating earlier output.
	payload := fakeRuntimeCommandPayload(domain.RuntimeCommandResult{Stdout: "tail\n", Output: "tail\n", ExitCode: 0, Success: true})
	controller, run := newReattachTestController(t, []string{
		attachOutRecord(t, "tail\n", 5),
		attachOutRecord(t, payload, 10),
		`{"t":"exit","code":0,"reason":"completed"}`,
	})
	got, err := controller.ReattachDetachedRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun returned error: %v", err)
	}
	if got.Status != domain.ProjectRunStatusSucceeded || got.ExitCode != 0 {
		t.Fatalf("run = %+v, want succeeded", got)
	}
	// Re-attach must append to the same transcript the live run was writing, not
	// a fresh output.txt, so pre-restart output stays in the run log.
	if filepath.Base(got.LogsPath) != "transcript.txt" {
		t.Fatalf("LogsPath = %q, want transcript.txt", got.LogsPath)
	}
	// Output comes from the parsed result payload, like a fresh run, so the
	// raw payload line never reaches the run record.
	if got.Output != "tail\n" || strings.Contains(got.Output, execution.CommandResultPrefix) {
		t.Fatalf("Output = %q, want parsed payload output", got.Output)
	}
}

func TestReattachDetachedRunFailsOnNonZeroExit(t *testing.T) {
	payload := fakeRuntimeCommandPayload(domain.RuntimeCommandResult{Stderr: "boom\n", Output: "boom\n", ExitCode: 2, Success: false})
	controller, run := newReattachTestController(t, []string{
		attachOutRecord(t, payload, 0),
		`{"t":"exit","code":2,"reason":"completed"}`,
	})
	got, err := controller.ReattachDetachedRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun returned error: %v", err)
	}
	if got.Status != domain.ProjectRunStatusFailed || got.ExitCode != 2 {
		t.Fatalf("run status = %q exit = %d, want failed with exit 2", got.Status, got.ExitCode)
	}
}

func TestReattachDetachedRunFailsWithoutResultPayload(t *testing.T) {
	controller, run := newReattachTestController(t, []string{
		attachOutRecord(t, "no payload\n", 0),
		`{"t":"exit","code":0,"reason":"completed"}`,
	})
	got, err := controller.ReattachDetachedRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun returned error: %v", err)
	}
	if got.Status != domain.ProjectRunStatusFailed {
		t.Fatalf("run status = %q, want failed", got.Status)
	}
}

func TestReattachDetachedRunFallsBackToResultFileWhenCursorPassedPayload(t *testing.T) {
	// The daemon stopped after consuming the payload line but before the exit
	// record, so the re-attach only sees the exit.
	controller, run := newReattachTestController(t, []string{
		`{"t":"exit","code":0,"reason":"completed"}`,
	})
	sandbox, err := controller.store.GetSandbox(context.Background(), run.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	artifactsDir := projectRunCommandArtifactsDir(run, sandbox)
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	data, err := json.Marshal(domain.RuntimeCommandResult{Stdout: "done\n", Output: "done\n", ExitCode: 0, Success: true})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactsDir, "command-result.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	got, err := controller.ReattachDetachedRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun returned error: %v", err)
	}
	if got.Status != domain.ProjectRunStatusSucceeded || got.Output != "done\n" {
		t.Fatalf("run status = %q output = %q, want succeeded from command-result.json", got.Status, got.Output)
	}
}

// A re-attach that cannot resume the agent run (no executor support here) is an
// abandoned re-attach: the run is finalized as failed and the facade tokens kept
// for it must be retired, or they accumulate across upgrades.
func TestReattachDetachedAgentRunDeletesRetainedTokensWhenResumeUnsupported(t *testing.T) {
	controller, run := newReattachTestController(t, nil)
	run.DetachKind = domain.DetachKindAgent
	got, err := controller.ReattachDetachedRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ReattachDetachedRun returned error: %v", err)
	}
	if got.Status != domain.ProjectRunStatusFailed {
		t.Fatalf("run status = %q, want failed", got.Status)
	}
	store := controller.configDB.(*fakeControllerStore)
	if len(store.deletedFacadeTokens) != 1 || store.deletedFacadeTokens[0] != "run-1|agent" {
		t.Fatalf("deleted facade tokens = %v, want the retained agent tokens", store.deletedFacadeTokens)
	}
}
