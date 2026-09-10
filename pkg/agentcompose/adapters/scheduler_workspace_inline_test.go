package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/compose"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

// createNativeTestSchedulerWithWorkspace mirrors createNativeTestScheduler
// but attaches a full inline agent.Workspace spec (provider/url/path/target)
// instead of a bare {Name: workspaceID} label, matching what project apply
// actually produces for a yaml `workspace:` block (see pkg/compose
// normalizeInlineWorkspaceSpec, which always sets Provider).
func createNativeTestSchedulerWithWorkspace(t testing.TB, ctx context.Context, store *configstore.ConfigStore, scheduler domain.Scheduler, sourcePath string, workspace *compose.WorkspaceSpec) domain.Scheduler {
	t.Helper()
	projectID := "test-project-" + scheduler.Summary.ID
	agentName := "worker"
	agent := compose.NormalizedAgentSpec{
		Name: agentName, Enabled: true, Provider: scheduler.Summary.DefaultAgent, Image: scheduler.Summary.GuestImage,
		CapsetIDs: append([]string(nil), scheduler.Summary.CapsetIDs...),
		Workspace: workspace,
		Scheduler: &compose.NormalizedSchedulerSpec{
			Enabled: scheduler.Summary.Enabled, SandboxPolicy: scheduler.Summary.SandboxPolicy,
			ConcurrencyPolicy: scheduler.Summary.ConcurrencyPolicy, DisplayName: scheduler.Summary.Name,
			Description: scheduler.Summary.Description, Script: scheduler.Script,
		},
	}
	if driver := strings.TrimSpace(scheduler.Summary.Driver); driver != "" {
		agent.Driver = &compose.NormalizedDriverSpec{Name: driver}
	}
	specJSON, err := (&compose.NormalizedProjectSpec{Name: "test-project", Agents: []compose.NormalizedAgentSpec{agent}}).MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("marshal native scheduler fixture: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: projectID, Name: "test-project", SourcePath: sourcePath})
	if err != nil {
		t.Fatalf("upsert native scheduler project: %v", err)
	}
	revision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{ProjectID: project.ID, SpecHash: "fixture-" + scheduler.Summary.ID, SpecJSON: string(specJSON)})
	if err != nil {
		t.Fatalf("save native scheduler revision: %v", err)
	}
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ProjectID: project.ID, AgentName: agentName, Revision: revision.Revision, SchedulerEnabled: true, SpecJSON: "{}"}); err != nil {
		t.Fatalf("upsert scheduler project agent: %v", err)
	}
	if _, err := store.UpsertProjectScheduler(ctx, domain.ProjectSchedulerRecord{ID: scheduler.Summary.ID, ProjectID: project.ID, SchedulerID: scheduler.Summary.ID, AgentName: agentName, Revision: revision.Revision, Enabled: scheduler.Summary.Enabled, TriggerCount: len(scheduler.Triggers), SpecJSON: "{}"}); err != nil {
		t.Fatalf("upsert native scheduler: %v", err)
	}
	if _, err := store.ReplaceSchedulerTriggers(ctx, scheduler.Summary.ID, scheduler.Triggers); err != nil {
		t.Fatalf("replace native scheduler triggers: %v", err)
	}
	loaded, err := store.GetScheduler(ctx, scheduler.Summary.ID)
	if err != nil {
		t.Fatalf("load native scheduler: %v", err)
	}
	return loaded
}

// TestSchedulerSandboxRunnerEnsureResolvesInlineGitWorkspace reproduces
// issue #599: an agent-compose.yaml agent declares a scheduler-enabled
// `workspace: {provider: git, url: ...}` and the scheduler is run via a path
// that used to look the yaml `name` label up as a workspace_config preset id
// (scheduler.shell/scheduler.exec, or scheduler.agent with a session
// override). Since project apply never creates such a preset, Ensure used to
// fail with "workspace config <id> not found" even though the full workspace
// spec was declared in yaml. Ensure must now resolve the git workspace
// directly from the inline spec.
func TestSchedulerSandboxRunnerEnsureResolvesInlineGitWorkspace(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	ensurer := &recordingSchedulerWorkspaceEnsurer{}
	publisher := &schedulerSessionPublisherFake{}
	runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config:           bridge.config,
		Store:            bridge.store,
		ConfigDB:         bridge.configDB,
		WorkspaceEnsurer: ensurer,
		Driver:           driver,
		Cap:              nil,
		VolumeResolver:   nil,
		Streams:          bridge.streams,
		Publisher:        publisher,
		CapTokens:        nil,
		AgentExecutor:    bridge.agentExecutor,
	})

	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID:            "scheduler-inline-git",
		Name:          "Scheduler Inline Git",
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.SchedulerSandboxPolicySticky,
	}}
	scheduler = createNativeTestSchedulerWithWorkspace(t, ctx, bridge.configDB, scheduler, "", &compose.WorkspaceSpec{
		Provider: "git",
		URL:      "https://example.com/some/repo.git",
		Name:     "my-repo",
	})

	sandbox, eventType, err := runner.Ensure(ctx, scheduler, domain.SchedulerAgentRequest{BindingTriggerID: "trigger-inline-git"}, false)
	if err != nil {
		t.Fatalf("Ensure returned error: %v, want the inline git workspace to resolve without a workspace_config lookup", err)
	}
	if eventType != "scheduler.sandbox.created" {
		t.Fatalf("Ensure event type = %q, want scheduler.sandbox.created", eventType)
	}
	if len(ensurer.initialWorkspaces) != 1 || ensurer.initialWorkspaces[0] == nil {
		t.Fatalf("workspace Ensure snapshot = %#v, want one non-nil snapshot", ensurer.initialWorkspaces)
	}
	snapshot := ensurer.initialWorkspaces[0]
	if snapshot.Type != "git" {
		t.Fatalf("resolved workspace type = %q, want git", snapshot.Type)
	}
	var decoded workspaces.GitWorkspaceConfig
	if err := json.Unmarshal([]byte(snapshot.ConfigJSON), &decoded); err != nil {
		t.Fatalf("decode resolved git workspace config: %v", err)
	}
	if decoded.URL != "https://example.com/some/repo.git" {
		t.Fatalf("resolved git workspace url = %q, want https://example.com/some/repo.git", decoded.URL)
	}
	if sandbox.Summary.ID == "" {
		t.Fatalf("expected sandbox to be created")
	}
}

// TestSchedulerSandboxRunnerEnsureResolvesInlineFileWorkspace covers the
// `provider: file` variant of issue #599: the agent's local workspace
// content must be materialized directly from the project source path
// instead of being looked up as a workspace_config preset.
func TestSchedulerSandboxRunnerEnsureResolvesInlineFileWorkspace(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	ensurer := &recordingSchedulerWorkspaceEnsurer{}
	publisher := &schedulerSessionPublisherFake{}
	runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config:           bridge.config,
		Store:            bridge.store,
		ConfigDB:         bridge.configDB,
		WorkspaceEnsurer: ensurer,
		Driver:           driver,
		Cap:              nil,
		VolumeResolver:   nil,
		Streams:          bridge.streams,
		Publisher:        publisher,
		CapTokens:        nil,
		AgentExecutor:    bridge.agentExecutor,
	})

	projectSource := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectSource, "hello.txt"), []byte("hello from source\n"), 0o644); err != nil {
		t.Fatalf("write source fixture file: %v", err)
	}

	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID:            "scheduler-inline-file",
		Name:          "Scheduler Inline File",
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.SchedulerSandboxPolicySticky,
	}}
	scheduler = createNativeTestSchedulerWithWorkspace(t, ctx, bridge.configDB, scheduler, projectSource, &compose.WorkspaceSpec{
		Provider: "file",
		Path:     ".",
		Name:     "my-local-workspace",
	})

	_, eventType, err := runner.Ensure(ctx, scheduler, domain.SchedulerAgentRequest{BindingTriggerID: "trigger-inline-file"}, false)
	if err != nil {
		t.Fatalf("Ensure returned error: %v, want the inline file workspace to materialize without a workspace_config lookup", err)
	}
	if eventType != "scheduler.sandbox.created" {
		t.Fatalf("Ensure event type = %q, want scheduler.sandbox.created", eventType)
	}
	if len(ensurer.initialWorkspaces) != 1 || ensurer.initialWorkspaces[0] == nil {
		t.Fatalf("workspace Ensure snapshot = %#v, want one non-nil snapshot", ensurer.initialWorkspaces)
	}
	snapshot := ensurer.initialWorkspaces[0]
	if snapshot.Type != "file" {
		t.Fatalf("resolved workspace type = %q, want file", snapshot.Type)
	}
	contentRoot, err := workspaces.FileWorkspaceContentRoot(bridge.config, domain.WorkspaceConfig{ID: snapshot.ID, Type: "file", ConfigJSON: snapshot.ConfigJSON, SnapshotID: snapshot.SnapshotID})
	if err != nil {
		t.Fatalf("resolve materialized file workspace content root: %v", err)
	}
	copied, err := os.ReadFile(filepath.Join(contentRoot, "hello.txt"))
	if err != nil {
		t.Fatalf("read materialized workspace content: %v", err)
	}
	if string(copied) != "hello from source\n" {
		t.Fatalf("materialized workspace content = %q, want copied source content", copied)
	}
}

// Concurrent preparations have distinct physical generations and stable logical identity.
func TestSchedulerSandboxRunnerConcurrentInlineFileWorkspaceGenerationsAreIndependent(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	ensurer := &recordingSchedulerWorkspaceEnsurer{}
	publisher := &schedulerSessionPublisherFake{}
	runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config:           bridge.config,
		Store:            bridge.store,
		ConfigDB:         bridge.configDB,
		WorkspaceEnsurer: ensurer,
		Driver:           driver,
		Cap:              nil,
		VolumeResolver:   nil,
		Streams:          bridge.streams,
		Publisher:        publisher,
		CapTokens:        nil,
		AgentExecutor:    bridge.agentExecutor,
	})

	projectSource := t.TempDir()
	for dir := 0; dir < 8; dir++ {
		subdir := filepath.Join(projectSource, fmt.Sprintf("dir-%d", dir))
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatalf("create source subdir %d: %v", dir, err)
		}
		for file := 0; file < 40; file++ {
			content := strings.Repeat(fmt.Sprintf("dir-%d-file-%d\n", dir, file), 64)
			if err := os.WriteFile(filepath.Join(subdir, fmt.Sprintf("file-%d.txt", file)), []byte(content), 0o644); err != nil {
				t.Fatalf("write source fixture dir=%d file=%d: %v", dir, file, err)
			}
		}
	}

	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID:            "scheduler-inline-file-concurrent",
		Name:          "Scheduler Inline File Concurrent",
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.SchedulerSandboxPolicySticky,
	}}
	scheduler = createNativeTestSchedulerWithWorkspace(t, ctx, bridge.configDB, scheduler, projectSource, &compose.WorkspaceSpec{
		Provider: "file",
		Path:     ".",
		Name:     "my-local-workspace-concurrent",
	})
	agentDefinition, err := runner.ResolveSchedulerAgentDefinition(ctx, scheduler)
	if err != nil {
		t.Fatalf("ResolveSchedulerAgentDefinition returned error: %v", err)
	}
	spec := agentDefinitionInlineWorkspace(agentDefinition)
	if spec == nil {
		t.Fatalf("agentDefinitionInlineWorkspace returned nil, want the inline file workspace spec")
	}

	const callerCount = 16
	const timeout = 10 * time.Second
	type result struct {
		index    int
		snapshot *domain.SandboxWorkspace
		err      error
	}
	results := make(chan result, callerCount)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callerCount)
	for index := 0; index < callerCount; index++ {
		go func(index int) {
			ready.Done()
			<-start
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			snapshot, _, err := runner.inlineWorkspaceSnapshot(runCtx, agentDefinition, spec, driverpkg.RuntimeDriverDocker)
			results <- result{index: index, snapshot: snapshot, err: err}
		}(index)
	}
	ready.Wait()
	close(start)

	snapshots := make([]*domain.SandboxWorkspace, 0, callerCount)
	for i := 0; i < callerCount; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("concurrent inlineWorkspaceSnapshot call %d returned error: %v, want the shared content directory reset+copy to be serialized", r.index, r.err)
			}
			snapshots = append(snapshots, r.snapshot)
		case <-time.After(timeout):
			t.Fatalf("timed out waiting for concurrent inlineWorkspaceSnapshot calls (%d/%d returned)", i, callerCount)
		}
	}

	workspaceID := inlineWorkspaceID(agentDefinition, "file")
	seen := make(map[string]bool)
	for _, snapshot := range snapshots {
		if snapshot.ID != workspaceID || snapshot.SnapshotID == "" || seen[snapshot.SnapshotID] {
			t.Fatalf("snapshot identity = %#v", snapshot)
		}
		seen[snapshot.SnapshotID] = true
		contentRoot, err := workspaces.FileWorkspaceContentRoot(bridge.config, domain.WorkspaceConfig{ID: workspaceID, Type: "file", ConfigJSON: snapshot.ConfigJSON, SnapshotID: snapshot.SnapshotID})
		if err != nil {
			t.Fatalf("resolve materialized file workspace content root: %v", err)
		}
		for dir := 0; dir < 8; dir++ {
			for file := 0; file < 40; file++ {
				want := strings.Repeat(fmt.Sprintf("dir-%d-file-%d\n", dir, file), 64)
				got, err := os.ReadFile(filepath.Join(contentRoot, fmt.Sprintf("dir-%d", dir), fmt.Sprintf("file-%d.txt", file)))
				if err != nil {
					t.Fatalf("read materialized workspace content dir=%d file=%d: %v", dir, file, err)
				}
				if string(got) != want {
					t.Fatalf("materialized workspace content dir=%d file=%d = %q, want %q", dir, file, got, want)
				}
			}
		}
		if err := workspaces.ReleaseTransientSnapshot(ctx, bridge.config, snapshot); err != nil {
			t.Fatal(err)
		}
	}
}

// Concurrent full provisioning consumes and releases only its own generation.
func TestSchedulerSandboxRunnerConcurrentEnsureConsumesIndependentGenerations(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	provisioner := workspaces.NewProvisioner(bridge.config, bridge.configDB, bridge.store)
	publisher := &schedulerSessionPublisherFake{}
	runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config:           bridge.config,
		Store:            bridge.store,
		ConfigDB:         bridge.configDB,
		WorkspaceEnsurer: provisioner,
		Driver:           &concurrentInlineWorkspaceDriver{fakeRPCSandboxDriver: driver},
		Cap:              nil,
		VolumeResolver:   nil,
		Streams:          bridge.streams,
		Publisher:        publisher,
		CapTokens:        nil,
		AgentExecutor:    bridge.agentExecutor,
	})

	projectSource := t.TempDir()
	const dirCount, fileCount = 6, 30
	for dir := 0; dir < dirCount; dir++ {
		subdir := filepath.Join(projectSource, fmt.Sprintf("dir-%d", dir))
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatalf("create source subdir %d: %v", dir, err)
		}
		for file := 0; file < fileCount; file++ {
			content := strings.Repeat(fmt.Sprintf("dir-%d-file-%d\n", dir, file), 64)
			if err := os.WriteFile(filepath.Join(subdir, fmt.Sprintf("file-%d.txt", file)), []byte(content), 0o644); err != nil {
				t.Fatalf("write source fixture dir=%d file=%d: %v", dir, file, err)
			}
		}
	}

	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID:            "scheduler-inline-file-ensure-race",
		Name:          "Scheduler Inline File Ensure Race",
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.SchedulerSandboxPolicyNew,
	}}
	scheduler = createNativeTestSchedulerWithWorkspace(t, ctx, bridge.configDB, scheduler, projectSource, &compose.WorkspaceSpec{
		Provider: "file",
		Path:     ".",
		Name:     "my-local-workspace-ensure-race",
	})

	const callerCount = 12
	const timeout = 20 * time.Second
	type result struct {
		index   int
		sandbox *domain.Sandbox
		err     error
	}
	results := make(chan result, callerCount)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callerCount)
	for index := 0; index < callerCount; index++ {
		go func(index int) {
			ready.Done()
			<-start
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			request := domain.SchedulerAgentRequest{
				BindingTriggerID: fmt.Sprintf("trigger-ensure-race-%d", index),
				SandboxPolicy:    domain.SchedulerSandboxPolicyNew,
			}
			sandbox, _, err := runner.Ensure(runCtx, scheduler, request, false)
			results <- result{index: index, sandbox: sandbox, err: err}
		}(index)
	}
	ready.Wait()
	close(start)

	var sandboxes []*domain.Sandbox
	for i := 0; i < callerCount; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("concurrent Ensure call %d returned error: %v, want the shared file workspace directory read (workspaceEnsurer.Ensure) to be serialized against concurrent materialization", r.index, r.err)
			}
			sandboxes = append(sandboxes, r.sandbox)
		case <-time.After(timeout):
			t.Fatalf("timed out waiting for concurrent Ensure calls (%d/%d returned)", i, callerCount)
		}
	}

	for _, sandbox := range sandboxes {
		root, err := workspaces.FileWorkspaceContentRoot(bridge.config, domain.WorkspaceConfig{ID: sandbox.Workspace.ID, Type: "file", ConfigJSON: sandbox.Workspace.ConfigJSON, SnapshotID: sandbox.Workspace.SnapshotID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("ready generation remains: %v", err)
		}
		for dir := 0; dir < dirCount; dir++ {
			for file := 0; file < fileCount; file++ {
				want := strings.Repeat(fmt.Sprintf("dir-%d-file-%d\n", dir, file), 64)
				got, err := os.ReadFile(filepath.Join(sandbox.Summary.WorkspacePath, fmt.Sprintf("dir-%d", dir), fmt.Sprintf("file-%d.txt", file)))
				if err != nil {
					t.Fatalf("sandbox %s: read copied workspace content dir=%d file=%d: %v", sandbox.Summary.ID, dir, file, err)
				}
				if string(got) != want {
					t.Fatalf("sandbox %s: copied workspace content dir=%d file=%d = %q, want %q", sandbox.Summary.ID, dir, file, got, want)
				}
			}
		}
	}
}

// TestSchedulerSandboxRunnerEnsureReleasesFileWorkspaceLockOnEnsurerPanic
// covers a third finding from the same review: fileWorkspaceReadLock's
// unlock was called manually right after workspaceEnsurer.Ensure instead of
// via defer. If that call panics (an unexpected error from the underlying
// copy/driver/store layers), the manual unlock never runs and the
// per-workspace-id lock stays held forever — every later scheduler
// Ensure/Resume for that agent's inline file workspace would then block on
// the lock permanently, a lasting availability failure. This drives an
// ensurer that panics on its first call, recovers (as the caller would),
// and asserts a second Ensure call for the same workspace still completes
// instead of hanging on the leaked lock.
func TestSchedulerSandboxRunnerEnsureReleasesFileWorkspaceLockOnEnsurerPanic(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	var calls int32
	ensurer := &recordingSchedulerWorkspaceEnsurer{ensure: func(context.Context, *domain.Sandbox) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			panic("simulated workspaceEnsurer.Ensure panic")
		}
		return nil
	}}
	publisher := &schedulerSessionPublisherFake{}
	runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config:           bridge.config,
		Store:            bridge.store,
		ConfigDB:         bridge.configDB,
		WorkspaceEnsurer: ensurer,
		Driver:           driver,
		Cap:              nil,
		VolumeResolver:   nil,
		Streams:          bridge.streams,
		Publisher:        publisher,
		CapTokens:        nil,
		AgentExecutor:    bridge.agentExecutor,
	})

	projectSource := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectSource, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write source fixture file: %v", err)
	}

	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID:            "scheduler-inline-file-panic",
		Name:          "Scheduler Inline File Panic",
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.SchedulerSandboxPolicyNew,
	}}
	scheduler = createNativeTestSchedulerWithWorkspace(t, ctx, bridge.configDB, scheduler, projectSource, &compose.WorkspaceSpec{
		Provider: "file",
		Path:     ".",
		Name:     "my-local-workspace-panic",
	})

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("expected first Ensure call's workspaceEnsurer.Ensure to panic")
			}
		}()
		_, _, _ = runner.Ensure(ctx, scheduler, domain.SchedulerAgentRequest{BindingTriggerID: "trigger-panic-1", SandboxPolicy: domain.SchedulerSandboxPolicyNew}, false)
		t.Fatalf("Ensure returned instead of panicking")
	}()

	done := make(chan error, 1)
	go func() {
		_, _, err := runner.Ensure(ctx, scheduler, domain.SchedulerAgentRequest{BindingTriggerID: "trigger-panic-2", SandboxPolicy: domain.SchedulerSandboxPolicyNew}, false)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second Ensure call after ensurer panic returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("second Ensure call for the same workspace hung, want the per-workspace lock released after the first call's ensurer panic")
	}
}

type concurrentInlineWorkspaceDriver struct {
	*fakeRPCSandboxDriver
	mu sync.Mutex
}

func (d *concurrentInlineWorkspaceDriver) StartSandboxVM(ctx context.Context, sandbox *domain.Sandbox) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fakeRPCSandboxDriver.StartSandboxVM(ctx, sandbox)
}
