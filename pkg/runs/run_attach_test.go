package runs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chaitin/agent-compose/internal/projects"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

func TestRunCommandInteractionMissingDependenciesPreservesLogsPath(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:           root,
		SandboxRoot:        filepath.Join(root, "sandboxes"),
		RuntimeDriver:      driverpkg.RuntimeDriverDocker,
		DefaultImage:       "guest:latest",
		DockerDefaultImage: "guest:latest",
		GuestStateRoot:     "/guest/state",
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	sandbox, err := store.CreateSandbox(ctx, "attach session", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	run := domain.ProjectRunRecord{RunID: "run-attach-edge", ProjectID: "project-1", AgentName: "worker"}
	wantLogsPath := filepath.Join(projectRunCommandArtifactsDir(run, sandbox), "transcript.txt")

	// A bare Controller has no store/runtime configured, so
	// openCommandInteraction fails before opening an interaction. The
	// returned transition must still carry LogsPath: it is persisted via
	// completeProjectRunError and sent to the client via
	// runAttachResultResponse, matching runPromptInteraction's error path.
	transition, execErr := (&Controller{config: config}).runCommandInteraction(ctx, interactionRunContext{
		Run:     run,
		Sandbox: sandbox,
		Request: RunAgentRequest{Command: "echo hi"},
	}, RunAttachInput{}, nil, nil)
	if execErr == nil {
		t.Fatalf("runCommandInteraction with missing dependencies returned nil error")
	}
	if transition.LogsPath != wantLogsPath {
		t.Fatalf("transition.LogsPath = %q, want %q", transition.LogsPath, wantLogsPath)
	}
	if transition.ExitCode != 1 {
		t.Fatalf("transition.ExitCode = %d, want 1", transition.ExitCode)
	}
	if transition.Error == "" {
		t.Fatalf("transition.Error is empty, want a message")
	}
}

func TestRunProjectCommandAttachRegisteredNotifiesRunningRun(t *testing.T) {
	// An attach retry whose idempotency key resolves to an already-running run
	// must send a started frame carrying the run id instead of closing the
	// stream silently and opening a second session.
	controller, configDB, _ := newTestRunAttachController(t, nil)
	runID, err := projects.StableProjectRunID("project-1", "worker", domain.ProjectRunSourceManual, "req-1")
	if err != nil {
		t.Fatalf("StableProjectRunID: %v", err)
	}
	configDB.runs[runID] = domain.ProjectRunRecord{
		RunID:     runID,
		ProjectID: "project-1",
		AgentName: "worker",
		Source:    domain.ProjectRunSourceManual,
		Status:    domain.ProjectRunStatusRunning,
		SandboxID: "sandbox-1",
	}

	var sent []RunAttachOutput
	err = controller.RunProjectCommandAttachRegistered(
		context.Background(), context.Background(),
		func() (RunAttachInput, error) {
			return RunAttachInput{
				Kind:    RunAttachInputStart,
				Mode:    RunAttachModePrompt,
				Request: RunAgentRequest{ProjectID: "project-1", AgentName: "worker", Source: domain.ProjectRunSourceManual, ClientRequestID: "req-1", Prompt: "already running"},
			}, nil
		},
		func(output RunAttachOutput) error { sent = append(sent, output); return nil },
		nil,
	)
	if err != nil {
		t.Fatalf("RunProjectCommandAttachRegistered() error = %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d frames, want exactly one started frame", len(sent))
	}
	if sent[0].Kind != RunAttachOutputStarted || sent[0].Run.RunID != runID || sent[0].SandboxID != "sandbox-1" {
		t.Fatalf("sent[0] = %#v, want a started frame for %s sandbox-1", sent[0], runID)
	}
}
