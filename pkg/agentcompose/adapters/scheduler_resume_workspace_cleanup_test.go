package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestSchedulerResumeWorkspaceCleanupOptInRemovesGitIndexLockBeforeStart(t *testing.T) {
	ctx := context.Background()
	bridge, driver, runner, sandbox, lockPath := newSchedulerResumeCleanupFixture(t, "opt-in", domain.VMStatusStopped, []domain.SandboxEnvVar{
		{Name: resumeCleanupGitIndexLockEnv, Value: "1"},
	}, nil)

	ensureCalls := 0
	runner.workspaceEnsurer = &recordingSchedulerWorkspaceEnsurer{ensure: func(context.Context, *domain.Sandbox) error {
		ensureCalls++
		if _, err := os.Lstat(lockPath); err != nil {
			t.Fatalf("Git index lock before cleanup: %v", err)
		}
		return nil
	}}
	driver.onStart = func(*domain.Sandbox) {
		if _, err := os.Lstat(lockPath); !os.IsNotExist(err) {
			t.Fatalf("Git index lock at driver start: err=%v, want absent", err)
		}
		events, err := bridge.store.ListEvents(ctx, sandbox.Summary.ID)
		if err != nil {
			t.Fatalf("ListEvents at driver start: %v", err)
		}
		if event := findSandboxEvent(events, resumeWorkspaceCleanupEvent); event == nil {
			t.Fatalf("events at driver start = %#v, want %q", events, resumeWorkspaceCleanupEvent)
		} else if strings.Contains(event.Message, sandbox.Summary.WorkspacePath) {
			t.Fatalf("cleanup event exposed workspace path: %q", event.Message)
		}
	}

	resumed, eventType, err := runner.LoadOrResume(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("LoadOrResume returned error: %v", err)
	}
	if ensureCalls != 1 || len(driver.startCalls) != 1 {
		t.Fatalf("workspace ensure/start calls = %d/%d, want 1/1", ensureCalls, len(driver.startCalls))
	}
	if resumed.Summary.VMStatus != domain.VMStatusRunning || eventType != "scheduler.sandbox.resumed" {
		t.Fatalf("resumed status/event = %q/%q", resumed.Summary.VMStatus, eventType)
	}
}

func TestSchedulerResumeWorkspaceCleanupIgnoresMergedEnvironment(t *testing.T) {
	ctx := context.Background()
	bridge, driver, runner, sandbox, lockPath := newSchedulerResumeCleanupFixture(t, "merged-env", domain.VMStatusStopped, nil, []domain.SandboxEnvVar{
		{Name: resumeCleanupGitIndexLockEnv, Value: "1"},
	})
	if _, err := bridge.configDB.ReplaceGlobalEnv(ctx, []domain.SandboxEnvVar{
		{Name: resumeCleanupGitIndexLockEnv, Value: "1"},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	driver.onStart = func(*domain.Sandbox) {
		if _, err := os.Lstat(lockPath); err != nil {
			t.Fatalf("Git index lock at driver start: %v, want preserved", err)
		}
	}

	if _, _, err := runner.LoadOrResume(ctx, sandbox.Summary.ID); err != nil {
		t.Fatalf("LoadOrResume returned error: %v", err)
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("driver start calls = %#v, want one", driver.startCalls)
	}
	if _, err := os.Lstat(lockPath); err != nil {
		t.Fatalf("Git index lock after opt-out resume: %v, want preserved", err)
	}
	events, err := bridge.store.ListEvents(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if event := findSandboxEvent(events, resumeWorkspaceCleanupEvent); event != nil {
		t.Fatalf("unexpected cleanup event = %#v", event)
	}
}

func TestSchedulerResumeWorkspaceCleanupMissingLockIsNoOp(t *testing.T) {
	ctx := context.Background()
	bridge, driver, runner, sandbox, lockPath := newSchedulerResumeCleanupFixture(t, "missing", domain.VMStatusStopped, []domain.SandboxEnvVar{
		{Name: resumeCleanupGitIndexLockEnv, Value: "1"},
	}, nil)
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove fixture Git index lock: %v", err)
	}

	if _, _, err := runner.LoadOrResume(ctx, sandbox.Summary.ID); err != nil {
		t.Fatalf("LoadOrResume returned error: %v", err)
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("driver start calls = %#v, want one", driver.startCalls)
	}
	events, err := bridge.store.ListEvents(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if event := findSandboxEvent(events, resumeWorkspaceCleanupEvent); event != nil {
		t.Fatalf("unexpected cleanup event = %#v", event)
	}
}

func TestSchedulerResumeWorkspaceCleanupRunningSandboxIsUntouched(t *testing.T) {
	ctx := context.Background()
	_, driver, runner, sandbox, lockPath := newSchedulerResumeCleanupFixture(t, "running", domain.VMStatusRunning, []domain.SandboxEnvVar{
		{Name: resumeCleanupGitIndexLockEnv, Value: "1"},
	}, nil)
	runner.resumeWorkspaceCleanup = func(string) (bool, error) {
		t.Fatal("running sandbox invoked resume workspace cleanup")
		return false, nil
	}

	loaded, eventType, err := runner.LoadOrResume(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("LoadOrResume returned error: %v", err)
	}
	if loaded.Summary.ID != sandbox.Summary.ID || eventType != "" {
		t.Fatalf("loaded sandbox/event = %q/%q", loaded.Summary.ID, eventType)
	}
	if len(driver.startCalls) != 0 {
		t.Fatalf("driver start calls = %#v, want none", driver.startCalls)
	}
	if _, err := os.Lstat(lockPath); err != nil {
		t.Fatalf("Git index lock after running fast path: %v, want preserved", err)
	}
}

func TestSchedulerResumeWorkspaceCleanupOtherNonRunningStateIsUntouched(t *testing.T) {
	ctx := context.Background()
	_, driver, runner, sandbox, lockPath := newSchedulerResumeCleanupFixture(t, "failed", domain.VMStatusFailed, []domain.SandboxEnvVar{
		{Name: resumeCleanupGitIndexLockEnv, Value: "1"},
	}, nil)
	runner.resumeWorkspaceCleanup = func(string) (bool, error) {
		t.Fatal("failed sandbox invoked stopped-resume workspace cleanup")
		return false, nil
	}
	driver.onStart = func(*domain.Sandbox) {
		if _, err := os.Lstat(lockPath); err != nil {
			t.Fatalf("Git index lock at driver start: %v, want preserved", err)
		}
	}

	if _, _, err := runner.LoadOrResume(ctx, sandbox.Summary.ID); err != nil {
		t.Fatalf("LoadOrResume returned error: %v", err)
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("driver start calls = %#v, want one", driver.startCalls)
	}
}

func TestSchedulerResumeWorkspaceCleanupRejectsUnsafeOrFailedCleanupBeforeStart(t *testing.T) {
	t.Run("nonregular lock", func(t *testing.T) {
		ctx := context.Background()
		_, driver, runner, sandbox, lockPath := newSchedulerResumeCleanupFixture(t, "nonregular", domain.VMStatusStopped, []domain.SandboxEnvVar{
			{Name: resumeCleanupGitIndexLockEnv, Value: "1"},
		}, nil)
		if err := os.Remove(lockPath); err != nil {
			t.Fatalf("remove regular fixture lock: %v", err)
		}
		if err := os.Mkdir(lockPath, 0o755); err != nil {
			t.Fatalf("create nonregular fixture lock: %v", err)
		}

		_, _, err := runner.LoadOrResume(ctx, sandbox.Summary.ID)
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("LoadOrResume error = %v, want nonregular lock failure", err)
		}
		if len(driver.startCalls) != 0 {
			t.Fatalf("driver start calls = %#v, want none", driver.startCalls)
		}
	})

	t.Run("symlinked Git metadata", func(t *testing.T) {
		ctx := context.Background()
		_, driver, runner, sandbox, lockPath := newSchedulerResumeCleanupFixture(t, "git-symlink", domain.VMStatusStopped, []domain.SandboxEnvVar{
			{Name: resumeCleanupGitIndexLockEnv, Value: "1"},
		}, nil)
		gitPath := filepath.Dir(lockPath)
		if err := os.Remove(lockPath); err != nil {
			t.Fatalf("remove regular fixture lock: %v", err)
		}
		if err := os.Remove(gitPath); err != nil {
			t.Fatalf("remove fixture Git metadata directory: %v", err)
		}
		externalGitPath := t.TempDir()
		externalLockPath := filepath.Join(externalGitPath, "index.lock")
		if err := os.WriteFile(externalLockPath, []byte("external-lock\n"), 0o600); err != nil {
			t.Fatalf("create external Git index lock: %v", err)
		}
		if err := os.Symlink(externalGitPath, gitPath); err != nil {
			t.Fatalf("symlink fixture Git metadata directory: %v", err)
		}

		_, _, err := runner.LoadOrResume(ctx, sandbox.Summary.ID)
		if err == nil || !strings.Contains(err.Error(), "Git metadata path is not a real directory") {
			t.Fatalf("LoadOrResume error = %v, want symlinked Git metadata failure", err)
		}
		if len(driver.startCalls) != 0 {
			t.Fatalf("driver start calls = %#v, want none", driver.startCalls)
		}
		if got, readErr := os.ReadFile(externalLockPath); readErr != nil || string(got) != "external-lock\n" {
			t.Fatalf("external Git index lock = %q err=%v, want untouched", got, readErr)
		}
	})

	t.Run("cleanup failure", func(t *testing.T) {
		ctx := context.Background()
		_, driver, runner, sandbox, _ := newSchedulerResumeCleanupFixture(t, "failure", domain.VMStatusStopped, []domain.SandboxEnvVar{
			{Name: resumeCleanupGitIndexLockEnv, Value: "1"},
		}, nil)
		cleanupErr := errors.New("injected cleanup failure")
		runner.resumeWorkspaceCleanup = func(string) (bool, error) {
			return false, cleanupErr
		}

		_, _, err := runner.LoadOrResume(ctx, sandbox.Summary.ID)
		if !errors.Is(err, cleanupErr) {
			t.Fatalf("LoadOrResume error = %v, want %v", err, cleanupErr)
		}
		if len(driver.startCalls) != 0 {
			t.Fatalf("driver start calls = %#v, want none", driver.startCalls)
		}
	})
}

func TestSchedulerResumeWorkspaceCleanupDoesNotRunForFreshCreate(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	agent := createNativeTestAgent(t, ctx, bridge.configDB, domain.AgentDefinition{
		ID:       "resume-cleanup-fresh-agent",
		Name:     "resume-cleanup-fresh-agent",
		Enabled:  true,
		Provider: "codex",
		EnvItems: []domain.SandboxEnvVar{{Name: resumeCleanupGitIndexLockEnv, Value: "1"}},
	})
	var lockPath string
	ensurer := &recordingSchedulerWorkspaceEnsurer{ensure: func(_ context.Context, sandbox *domain.Sandbox) error {
		lockPath = filepath.Join(sandbox.Summary.WorkspacePath, ".git", "index.lock")
		if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(lockPath, []byte("fresh-create\n"), 0o600)
	}}
	runner := NewSchedulerSandboxRunner(bridge.config, bridge.store, bridge.configDB, ensurer, driver, nil, nil, bridge.streams, nil, nil, bridge.agentExecutor)
	runner.resumeWorkspaceCleanup = func(string) (bool, error) {
		t.Fatal("fresh create invoked resume workspace cleanup")
		return false, nil
	}
	driver.onStart = func(*domain.Sandbox) {
		if _, err := os.Lstat(lockPath); err != nil {
			t.Fatalf("fresh-create Git index lock at driver start: %v, want preserved", err)
		}
	}
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID:            "resume-cleanup-fresh-scheduler",
		Name:          "Resume Cleanup Fresh Scheduler",
		AgentID:       agent.ID,
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.SchedulerSandboxPolicyNew,
	}}

	if _, _, err := runner.Ensure(ctx, scheduler, domain.SchedulerAgentRequest{}, false); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("driver start calls = %#v, want one", driver.startCalls)
	}
}

func newSchedulerResumeCleanupFixture(t *testing.T, suffix, status string, agentEnv, sandboxEnv []domain.SandboxEnvVar) (*SandboxRPCBridge, *fakeRPCSandboxDriver, *SchedulerSandboxRunner, *domain.Sandbox, string) {
	t.Helper()
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	agent := createNativeTestAgent(t, ctx, bridge.configDB, domain.AgentDefinition{
		ID:       "resume-cleanup-" + suffix + "-agent",
		Name:     "resume-cleanup-" + suffix + "-agent",
		Enabled:  true,
		Provider: "codex",
		EnvItems: agentEnv,
	})
	sandbox, err := bridge.store.CreateSandbox(ctx, "resume cleanup "+suffix, "", driverpkg.RuntimeDriverDocker, "", "", "scheduler", nil, sandboxEnv, []domain.SandboxTag{
		{Name: domain.AgentSandboxTagSource, Value: domain.AgentSandboxTagSourceVal},
		{Name: domain.AgentSandboxTagID, Value: agent.ID},
		{Name: domain.AgentSandboxTagProvider, Value: "codex"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	sandbox.Summary.VMStatus = status
	if err := bridge.store.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	if err := bridge.store.SaveVMState(sandbox.Summary.ID, domain.VMState{
		Driver:    driverpkg.RuntimeDriverDocker,
		StartedAt: time.Now().Add(-time.Minute).UTC(),
	}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	lockPath := filepath.Join(sandbox.Summary.WorkspacePath, ".git", "index.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("create fixture Git metadata: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("stale-lock\n"), 0o600); err != nil {
		t.Fatalf("create fixture Git index lock: %v", err)
	}
	runner := NewSchedulerSandboxRunner(bridge.config, bridge.store, bridge.configDB, bridge.workspaceEnsurer, driver, nil, nil, bridge.streams, nil, nil, bridge.agentExecutor)
	return bridge, driver, runner, sandbox, lockPath
}

func findSandboxEvent(events []domain.SandboxEvent, eventType string) *domain.SandboxEvent {
	for i := range events {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}
