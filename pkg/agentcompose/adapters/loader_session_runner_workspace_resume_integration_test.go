package adapters

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	driverpkg "agent-compose/pkg/driver"
	testutil "agent-compose/pkg/internal/testutil"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/workspaces"
)

func TestIntegrationLoaderStickyResumePreservesReadyFileWorkspace(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	const (
		workspaceID = "loader-sticky-resume-workspace"
		loaderID    = "loader-sticky-resume"
		triggerID   = "loader-sticky-trigger"
	)

	sourceRoot, err := workspaces.DefaultFileWorkspaceContentRoot(bridge.config, workspaceID)
	if err != nil {
		t.Fatalf("resolve file workspace source: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "docs"), 0o755); err != nil {
		t.Fatalf("create file workspace source: %v", err)
	}
	writeIntegrationWorkspaceFile(t, filepath.Join(sourceRoot, "editable.txt"), "loader source v1\n", 0o644)
	writeIntegrationWorkspaceFile(t, filepath.Join(sourceRoot, "template.txt"), "remove in sandbox\n", 0o644)
	writeIntegrationWorkspaceFile(t, filepath.Join(sourceRoot, "docs", "guide.txt"), "guide v1\n", 0o644)

	workspaceConfig, err := bridge.configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       "Loader Sticky Resume Workspace",
		Type:       "file",
		ConfigJSON: workspaces.DefaultFileConfigJSON(bridge.config, workspaceID),
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	originalWorkspace := &domain.SandboxWorkspace{
		ID:         workspaceConfig.ID,
		Name:       workspaceConfig.Name,
		Type:       workspaceConfig.Type,
		ConfigJSON: workspaceConfig.ConfigJSON,
	}
	loader := domain.Loader{Summary: domain.LoaderSummary{
		ID:            loaderID,
		Name:          "Loader Sticky Resume",
		WorkspaceID:   workspaceID,
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.LoaderSandboxPolicySticky,
	}}
	request := domain.LoaderAgentRequest{BindingTriggerID: triggerID}
	publisher := &loaderSessionPublisherFake{}
	runner := NewLoaderSandboxRunner(
		bridge.config,
		bridge.store,
		bridge.configDB,
		bridge.workspaceEnsurer,
		driver,
		nil,
		nil,
		bridge.streams,
		publisher,
		nil,
		bridge.agentExecutor,
	)

	created, eventType, err := runner.Ensure(ctx, loader, request, false)
	if err != nil {
		t.Fatalf("first sticky Ensure returned error: %v", err)
	}
	if eventType != "loader.sandbox.created" {
		t.Fatalf("first sticky Ensure event type = %q, want %q", eventType, "loader.sandbox.created")
	}
	if created.Summary.ID == "" {
		t.Fatal("first sticky Ensure returned an empty sandbox ID")
	}
	assertIntegrationWorkspaceReady(t, created, "after loader create")
	if !reflect.DeepEqual(created.Workspace, originalWorkspace) {
		t.Fatalf("created workspace snapshot = %#v, want %#v", created.Workspace, originalWorkspace)
	}
	sandboxID := created.Summary.ID
	workspaceRoot := created.Summary.WorkspacePath
	readyUpdatedAt := created.WorkspaceProvisioning.UpdatedAt
	bindingBefore, ok, err := bridge.configDB.GetLoaderBinding(ctx, loaderID, triggerID)
	if err != nil {
		t.Fatalf("GetLoaderBinding after create returned error: %v", err)
	}
	if !ok || bindingBefore.SandboxID != sandboxID {
		t.Fatalf("loader binding after create = %#v ok=%v, want sandbox %q", bindingBefore, ok, sandboxID)
	}
	if got := len(driver.startCalls); got != 1 || driver.startCalls[0] != sandboxID {
		t.Fatalf("driver start calls after create = %#v, want [%q]", driver.startCalls, sandboxID)
	}

	writeIntegrationWorkspaceFile(t, filepath.Join(workspaceRoot, "editable.txt"), "loader-generated content\n", 0o600)
	if err := os.Remove(filepath.Join(workspaceRoot, "template.txt")); err != nil {
		t.Fatalf("remove template file from sandbox workspace: %v", err)
	}
	if err := os.Mkdir(filepath.Join(workspaceRoot, "generated"), 0o750); err != nil {
		t.Fatalf("create generated sandbox directory: %v", err)
	}
	writeIntegrationWorkspaceFile(t, filepath.Join(workspaceRoot, "generated", "result.txt"), "loader result\n", 0o640)
	if err := os.Symlink(filepath.Join("generated", "result.txt"), filepath.Join(workspaceRoot, "result-link")); err != nil {
		t.Fatalf("create sandbox workspace symlink: %v", err)
	}
	beforeShutdown, err := testutil.WorkspaceManifest(workspaceRoot)
	if err != nil {
		t.Fatalf("manifest loader workspace before shutdown: %v", err)
	}

	writeIntegrationWorkspaceFile(t, filepath.Join(sourceRoot, "editable.txt"), "conflicting loader source v2\n", 0o644)
	writeIntegrationWorkspaceFile(t, filepath.Join(sourceRoot, "template.txt"), "source v2 would revive this file\n", 0o644)
	writeIntegrationWorkspaceFile(t, filepath.Join(sourceRoot, "docs", "guide.txt"), "conflicting guide v2\n", 0o644)
	writeIntegrationWorkspaceFile(t, filepath.Join(sourceRoot, "source-only-v2.txt"), "must not contaminate resumed sandbox\n", 0o644)

	if err := runner.Shutdown(ctx, sandboxID); err != nil {
		t.Fatalf("Shutdown sticky sandbox returned error: %v", err)
	}
	stopped, err := bridge.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		t.Fatalf("GetSandbox after loader Shutdown returned error: %v", err)
	}
	if stopped.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("sandbox VM status after loader Shutdown = %q, want %q", stopped.Summary.VMStatus, domain.VMStatusStopped)
	}
	assertIntegrationWorkspaceReady(t, stopped, "after loader shutdown")
	if !stopped.WorkspaceProvisioning.UpdatedAt.Equal(readyUpdatedAt) {
		t.Fatalf("ready UpdatedAt after loader Shutdown = %s, want %s", stopped.WorkspaceProvisioning.UpdatedAt, readyUpdatedAt)
	}
	if !reflect.DeepEqual(stopped.Workspace, originalWorkspace) {
		t.Fatalf("stopped workspace snapshot = %#v, want %#v", stopped.Workspace, originalWorkspace)
	}
	if info, statErr := os.Stat(workspaceRoot); statErr != nil || !info.IsDir() {
		t.Fatalf("loader Shutdown removed workspace %q: info=%#v err=%v", workspaceRoot, info, statErr)
	}
	afterShutdown, err := testutil.WorkspaceManifest(workspaceRoot)
	if err != nil {
		t.Fatalf("manifest loader workspace after shutdown: %v", err)
	}
	if !reflect.DeepEqual(afterShutdown, beforeShutdown) {
		t.Fatalf("loader Shutdown changed workspace manifest:\n got: %#v\nwant: %#v", afterShutdown, beforeShutdown)
	}
	if got := len(driver.stopCalls); got != 1 || driver.stopCalls[0] != sandboxID {
		t.Fatalf("driver stop calls after loader Shutdown = %#v, want [%q]", driver.stopCalls, sandboxID)
	}

	resumeStartChecks := 0
	driver.onStart = func(sandbox *domain.Sandbox) {
		resumeStartChecks++
		if sandbox.Summary.ID != sandboxID {
			t.Errorf("sandbox ID at resume driver start = %q, want %q", sandbox.Summary.ID, sandboxID)
		}
		assertIntegrationWorkspaceReady(t, sandbox, "at loader resume driver start")
		if !sandbox.WorkspaceProvisioning.UpdatedAt.Equal(readyUpdatedAt) {
			t.Errorf("ready UpdatedAt at resume driver start = %s, want %s", sandbox.WorkspaceProvisioning.UpdatedAt, readyUpdatedAt)
		}
		if !reflect.DeepEqual(sandbox.Workspace, originalWorkspace) {
			t.Errorf("workspace snapshot at resume driver start = %#v, want %#v", sandbox.Workspace, originalWorkspace)
		}
		atStart, manifestErr := testutil.WorkspaceManifest(sandbox.Summary.WorkspacePath)
		if manifestErr != nil {
			t.Errorf("manifest loader workspace at resume driver start: %v", manifestErr)
			return
		}
		if !reflect.DeepEqual(atStart, beforeShutdown) {
			t.Errorf("loader workspace changed before resume driver start:\n got: %#v\nwant: %#v", atStart, beforeShutdown)
		}
	}
	resumed, eventType, err := runner.Ensure(ctx, loader, request, false)
	if err != nil {
		t.Fatalf("second sticky Ensure returned error: %v", err)
	}
	if eventType != "loader.sandbox.resumed" {
		t.Fatalf("second sticky Ensure event type = %q, want %q", eventType, "loader.sandbox.resumed")
	}
	if resumed.Summary.ID != sandboxID {
		t.Fatalf("second sticky Ensure sandbox ID = %q, want original %q", resumed.Summary.ID, sandboxID)
	}
	if resumeStartChecks != 1 {
		t.Fatalf("resume driver start checks = %d, want 1", resumeStartChecks)
	}
	assertIntegrationWorkspaceReady(t, resumed, "after loader resume")
	if !resumed.WorkspaceProvisioning.UpdatedAt.Equal(readyUpdatedAt) {
		t.Fatalf("ready UpdatedAt after loader resume = %s, want %s", resumed.WorkspaceProvisioning.UpdatedAt, readyUpdatedAt)
	}
	if !reflect.DeepEqual(resumed.Workspace, originalWorkspace) {
		t.Fatalf("resumed workspace snapshot = %#v, want %#v", resumed.Workspace, originalWorkspace)
	}
	afterResume, err := testutil.WorkspaceManifest(workspaceRoot)
	if err != nil {
		t.Fatalf("manifest loader workspace after resume: %v", err)
	}
	if !reflect.DeepEqual(afterResume, beforeShutdown) {
		t.Fatalf("loader workspace changed across sticky resume:\n got: %#v\nwant: %#v", afterResume, beforeShutdown)
	}
	if _, statErr := os.Lstat(filepath.Join(workspaceRoot, "source-only-v2.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("resumed loader workspace contains source-only v2 file, stat error = %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(workspaceRoot, "template.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("resumed loader workspace revived deleted template file, stat error = %v", statErr)
	}

	bindingAfter, ok, err := bridge.configDB.GetLoaderBinding(ctx, loaderID, triggerID)
	if err != nil {
		t.Fatalf("GetLoaderBinding after resume returned error: %v", err)
	}
	if !ok || !reflect.DeepEqual(bindingAfter, bindingBefore) {
		t.Fatalf("loader binding changed across sticky resume:\n got: %#v ok=%v\nwant: %#v", bindingAfter, ok, bindingBefore)
	}
	if got := driver.startCalls; !reflect.DeepEqual(got, []string{sandboxID, sandboxID}) {
		t.Fatalf("driver start calls = %#v, want two starts for %q", got, sandboxID)
	}
	if got := driver.stopCalls; !reflect.DeepEqual(got, []string{sandboxID}) {
		t.Fatalf("driver stop calls = %#v, want one stop for %q", got, sandboxID)
	}
	listed, err := bridge.store.ListSandboxes(ctx, domain.SandboxListOptions{})
	if err != nil {
		t.Fatalf("ListSandboxes after sticky resume returned error: %v", err)
	}
	if listed.TotalCount != 1 || len(listed.Sandboxes) != 1 || listed.Sandboxes[0].Summary.ID != sandboxID {
		t.Fatalf("sandboxes after sticky resume = %#v total=%d, want only %q", listed.Sandboxes, listed.TotalCount, sandboxID)
	}

	events, err := bridge.store.ListEvents(ctx, sandboxID)
	if err != nil {
		t.Fatalf("ListEvents after sticky resume returned error: %v", err)
	}
	eventCounts := map[string]int{}
	for _, event := range events {
		eventCounts[event.Type]++
	}
	for _, eventName := range []string{"sandbox.created", "sandbox.stopped", "sandbox.resumed"} {
		if eventCounts[eventName] != 1 {
			t.Fatalf("sandbox lifecycle event counts = %#v, want one %q", eventCounts, eventName)
		}
	}
	wantTopics := []string{
		"agent-compose.session.created",
		"agent-compose.session.stopped",
		"agent-compose.session.resumed",
	}
	gotTopics := make([]string, len(publisher.events))
	for i, event := range publisher.events {
		gotTopics[i] = event.Topic
	}
	if !reflect.DeepEqual(gotTopics, wantTopics) {
		t.Fatalf("loader lifecycle topics = %#v, want %#v", gotTopics, wantTopics)
	}
}
