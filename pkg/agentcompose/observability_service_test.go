package agentcompose

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestArtifactServiceListsGetsAndReadsRunArtifacts(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	artifactDir := t.TempDir()
	resultPath := filepath.Join(artifactDir, "service-result.json")
	if err := os.WriteFile(resultPath, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatalf("write result artifact: %v", err)
	}
	run := createObservabilityRun(t, ctx, store, ProjectRunRecord{
		RunID:        "run-artifacts",
		ProjectID:    "project-artifacts",
		ProjectName:  "demo",
		TargetType:   "service",
		TargetName:   "echo",
		Status:       ProjectRunStatusSucceeded,
		ResultJSON:   `{"artifacts":{"result":"` + resultPath + `"},"runtimeArtifacts":{"trace":"/guest/trace.json"},"metrics":{"durationMs":"7"}}`,
		ArtifactsDir: artifactDir,
	})

	listed, err := service.ListArtifacts(ctx, connect.NewRequest(&agentcomposev2.ListArtifactsRequest{ProjectId: run.ProjectID, RunId: run.RunID, Limit: 10}))
	if err != nil {
		t.Fatalf("ListArtifacts returned error: %v", err)
	}
	if listed.Msg.GetTotalCount() != 3 || len(listed.Msg.GetArtifacts()) != 3 {
		t.Fatalf("ListArtifacts response = %#v", listed.Msg)
	}
	var resultArtifact *agentcomposev2.Artifact
	for _, artifact := range listed.Msg.GetArtifacts() {
		if artifact.GetName() == "cell.result" {
			resultArtifact = artifact
		}
	}
	if resultArtifact == nil || resultArtifact.GetSizeBytes() == 0 || !strings.HasPrefix(resultArtifact.GetDigest(), "sha256:") {
		t.Fatalf("result artifact = %#v", resultArtifact)
	}
	got, err := service.GetArtifact(ctx, connect.NewRequest(&agentcomposev2.GetArtifactRequest{ArtifactId: resultArtifact.GetArtifactId()}))
	if err != nil {
		t.Fatalf("GetArtifact returned error: %v", err)
	}
	if got.Msg.GetArtifact().GetPath() != resultPath {
		t.Fatalf("GetArtifact path = %q", got.Msg.GetArtifact().GetPath())
	}
	read, err := service.ReadArtifact(ctx, connect.NewRequest(&agentcomposev2.ReadArtifactRequest{ArtifactId: resultArtifact.GetArtifactId()}))
	if err != nil {
		t.Fatalf("ReadArtifact returned error: %v", err)
	}
	if string(read.Msg.GetContent()) != `{"ok":true}` {
		t.Fatalf("ReadArtifact content = %q", string(read.Msg.GetContent()))
	}
}

func TestArtifactServiceWritesAndDeletesRunArtifact(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	artifactDir := t.TempDir()
	run := createObservabilityRun(t, ctx, store, ProjectRunRecord{
		RunID:        "run-write-artifact",
		ProjectID:    "project-artifacts",
		ProjectName:  "demo",
		TargetType:   "service",
		TargetName:   "echo",
		Status:       ProjectRunStatusSucceeded,
		ResultJSON:   `{"metrics":{"durationMs":"5"}}`,
		ArtifactsDir: artifactDir,
	})

	written, err := service.WriteArtifact(ctx, connect.NewRequest(&agentcomposev2.WriteArtifactRequest{
		RunId:       run.RunID,
		Name:        "report",
		Path:        "reports/result.json",
		ContentType: "application/json",
		Content:     []byte(`{"ok":true}`),
		Metadata:    map[string]string{"kind": "summary"},
	}))
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	artifact := written.Msg.GetArtifact()
	if artifact.GetName() != "cell.report" || artifact.GetContentType() != "application/json" || artifact.GetMetadata()["kind"] != "summary" {
		t.Fatalf("WriteArtifact artifact = %#v", artifact)
	}
	if artifact.GetPath() != filepath.Join(artifactDir, "reports", "result.json") {
		t.Fatalf("WriteArtifact path = %q", artifact.GetPath())
	}
	read, err := service.ReadArtifact(ctx, connect.NewRequest(&agentcomposev2.ReadArtifactRequest{ArtifactId: artifact.GetArtifactId()}))
	if err != nil {
		t.Fatalf("ReadArtifact returned error: %v", err)
	}
	if string(read.Msg.GetContent()) != `{"ok":true}` {
		t.Fatalf("ReadArtifact content = %q", string(read.Msg.GetContent()))
	}

	deleted, err := service.DeleteArtifact(ctx, connect.NewRequest(&agentcomposev2.DeleteArtifactRequest{ArtifactId: artifact.GetArtifactId()}))
	if err != nil {
		t.Fatalf("DeleteArtifact returned error: %v", err)
	}
	if !deleted.Msg.GetDeleted() {
		t.Fatalf("DeleteArtifact deleted = false")
	}
	if _, err := os.Stat(artifact.GetPath()); !os.IsNotExist(err) {
		t.Fatalf("deleted artifact stat error = %v, want not exist", err)
	}
	listed, err := service.ListArtifacts(ctx, connect.NewRequest(&agentcomposev2.ListArtifactsRequest{RunId: run.RunID, Limit: 10}))
	if err != nil {
		t.Fatalf("ListArtifacts returned error: %v", err)
	}
	for _, item := range listed.Msg.GetArtifacts() {
		if item.GetArtifactId() == artifact.GetArtifactId() {
			t.Fatalf("deleted artifact still listed: %#v", listed.Msg.GetArtifacts())
		}
	}
}

func TestArtifactServiceDeleteMissingIndexedFileStillRemovesIndex(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	artifactDir := t.TempDir()
	missingPath := filepath.Join(artifactDir, "missing.json")
	run := createObservabilityRun(t, ctx, store, ProjectRunRecord{
		RunID:        "run-delete-missing-artifact",
		ProjectID:    "project-artifacts",
		ProjectName:  "demo",
		TargetType:   "service",
		TargetName:   "echo",
		Status:       ProjectRunStatusSucceeded,
		ResultJSON:   `{"artifacts":{"missing":"` + missingPath + `"}}`,
		ArtifactsDir: artifactDir,
	})

	deleted, err := service.DeleteArtifact(ctx, connect.NewRequest(&agentcomposev2.DeleteArtifactRequest{ArtifactId: run.RunID + ":cell.missing"}))
	if err != nil {
		t.Fatalf("DeleteArtifact returned error: %v", err)
	}
	if !deleted.Msg.GetDeleted() {
		t.Fatalf("DeleteArtifact deleted = false")
	}
	listed, err := service.ListArtifacts(ctx, connect.NewRequest(&agentcomposev2.ListArtifactsRequest{RunId: run.RunID, Limit: 10}))
	if err != nil {
		t.Fatalf("ListArtifacts returned error: %v", err)
	}
	for _, item := range listed.Msg.GetArtifacts() {
		if item.GetArtifactId() == run.RunID+":cell.missing" {
			t.Fatalf("deleted missing-file artifact still listed: %#v", listed.Msg.GetArtifacts())
		}
	}
}

func TestArtifactServiceRejectsUnsafeWritePath(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	root := t.TempDir()
	run := createObservabilityRun(t, ctx, store, ProjectRunRecord{
		RunID:        "run-unsafe-artifact",
		ProjectID:    "project-artifacts",
		ProjectName:  "demo",
		TargetType:   "service",
		TargetName:   "echo",
		Status:       ProjectRunStatusSucceeded,
		ResultJSON:   `{}`,
		ArtifactsDir: root,
	})

	_, err := service.WriteArtifact(ctx, connect.NewRequest(&agentcomposev2.WriteArtifactRequest{
		RunId:   run.RunID,
		Name:    "escape",
		Path:    "../escape.txt",
		Content: []byte("nope"),
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("WriteArtifact unsafe path error = %v, want invalid argument", err)
	}

	_, err = service.WriteArtifact(ctx, connect.NewRequest(&agentcomposev2.WriteArtifactRequest{
		RunId:   run.RunID,
		Name:    "bad/name",
		Path:    "safe.txt",
		Content: []byte("nope"),
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("WriteArtifact unsafe name error = %v, want invalid argument", err)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	_, err = service.WriteArtifact(ctx, connect.NewRequest(&agentcomposev2.WriteArtifactRequest{
		RunId:   run.RunID,
		Name:    "escape",
		Path:    "link/escape.txt",
		Content: []byte("nope"),
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("WriteArtifact symlink escape error = %v, want invalid argument", err)
	}

	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "target-link.txt")); err != nil {
		t.Skipf("file symlink not supported: %v", err)
	}
	_, err = service.WriteArtifact(ctx, connect.NewRequest(&agentcomposev2.WriteArtifactRequest{
		RunId:   run.RunID,
		Name:    "target-link",
		Path:    "target-link.txt",
		Content: []byte("replacement"),
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("WriteArtifact target symlink error = %v, want invalid argument", err)
	}
	content, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if string(content) != "secret" {
		t.Fatalf("outside file content = %q, want unchanged", string(content))
	}
}

func TestArtifactServiceRejectsUnsafeReadPath(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	artifactDir := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside artifact: %v", err)
	}
	run := createObservabilityRun(t, ctx, store, ProjectRunRecord{
		RunID:        "run-unsafe-read-artifact",
		ProjectID:    "project-artifacts",
		ProjectName:  "demo",
		TargetType:   "service",
		TargetName:   "echo",
		Status:       ProjectRunStatusSucceeded,
		ResultJSON:   `{"artifacts":{"outside":"` + outsidePath + `"}}`,
		ArtifactsDir: artifactDir,
	})

	listed, err := service.ListArtifacts(ctx, connect.NewRequest(&agentcomposev2.ListArtifactsRequest{RunId: run.RunID, Limit: 10}))
	if err != nil {
		t.Fatalf("ListArtifacts returned error: %v", err)
	}
	if len(listed.Msg.GetArtifacts()) == 0 {
		t.Fatalf("ListArtifacts returned no artifacts")
	}
	_, err = service.ReadArtifact(ctx, connect.NewRequest(&agentcomposev2.ReadArtifactRequest{ArtifactId: run.RunID + ":cell.outside"}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("ReadArtifact outside path error = %v, want invalid argument", err)
	}
}

func TestArtifactServiceRejectsDirectoryRead(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	artifactDir := t.TempDir()
	run := createObservabilityRun(t, ctx, store, ProjectRunRecord{
		RunID:        "run-directory-read-artifact",
		ProjectID:    "project-artifacts",
		ProjectName:  "demo",
		TargetType:   "service",
		TargetName:   "echo",
		Status:       ProjectRunStatusSucceeded,
		ResultJSON:   `{}`,
		ArtifactsDir: artifactDir,
	})

	_, err := service.ReadArtifact(ctx, connect.NewRequest(&agentcomposev2.ReadArtifactRequest{ArtifactId: run.RunID + ":run.artifactsDir"}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("ReadArtifact directory error = %v, want invalid argument", err)
	}
}

func TestEventServicePublishesListsAndRunDetailIncludesEvents(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	run := createObservabilityRun(t, ctx, store, ProjectRunRecord{
		RunID:       "run-events",
		ProjectID:   "project-events",
		ProjectName: "demo",
		TargetType:  "service",
		TargetName:  "echo",
		Status:      ProjectRunStatusSucceeded,
		ResultJSON:  `{"metrics":{"durationMs":"9"}}`,
	})
	created, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:          "runtime.test.completed",
		Source:         TopicEventSourceLoader,
		CorrelationID:  "corr-events",
		PayloadJSON:    `{"ok":true}`,
		DispatchStatus: TopicEventDispatchPending,
		PublisherType:  TopicEventSourceLoader,
		PublisherID:    run.ProjectID,
		PublisherRunID: run.RunID,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}

	listed, err := service.ListEvents(ctx, connect.NewRequest(&agentcomposev2.ListEventsRequest{ProjectId: run.ProjectID, RunId: run.RunID, Limit: 10}))
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if len(listed.Msg.GetEvents()) != 1 || listed.Msg.GetEvents()[0].GetEventId() != created.ID {
		t.Fatalf("ListEvents response = %#v", listed.Msg)
	}
	published, err := service.PublishEvent(ctx, connect.NewRequest(&agentcomposev2.PublishEventRequest{ProjectId: run.ProjectID, Topic: "runtime.api.requested", PayloadJson: `{"manual":true}`}))
	if err != nil {
		t.Fatalf("PublishEvent returned error: %v", err)
	}
	if published.Msg.GetEvent().GetProjectId() != run.ProjectID || published.Msg.GetEvent().GetTopic() != "runtime.api.requested" {
		t.Fatalf("PublishEvent response = %#v", published.Msg)
	}
	detail, err := service.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{ProjectId: run.ProjectID, RunId: run.RunID}))
	if err != nil {
		t.Fatalf("GetRun returned error: %v", err)
	}
	if detail.Msg.GetRun().GetMetrics()["durationMs"] != "9" || len(detail.Msg.GetRun().GetEvents()) != 1 {
		t.Fatalf("GetRun detail = %#v", detail.Msg.GetRun())
	}
}

func TestEventServicePublishEventRequiresExistingProject(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()

	_, err := service.PublishEvent(ctx, connect.NewRequest(&agentcomposev2.PublishEventRequest{Topic: "runtime.api.requested", PayloadJson: `{}`}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("PublishEvent missing project error = %v, want invalid argument", err)
	}

	_, err = service.PublishEvent(ctx, connect.NewRequest(&agentcomposev2.PublishEventRequest{ProjectId: "project-missing", Topic: "runtime.api.requested", PayloadJson: `{}`}))
	if err == nil || connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("PublishEvent unknown project error = %v, want not found", err)
	}
}

func TestEventServiceWatchEventsSendsExistingEventsAndStopsAfterCancel(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	run := createObservabilityRun(t, ctx, store, ProjectRunRecord{
		RunID:       "run-watch-events",
		ProjectID:   "project-watch-events",
		ProjectName: "demo",
		TargetType:  "service",
		TargetName:  "echo",
		Status:      ProjectRunStatusSucceeded,
		ResultJSON:  `{}`,
	})
	created, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:          "runtime.watch.completed",
		Source:         TopicEventSourceLoader,
		CorrelationID:  "corr-watch-events",
		PayloadJSON:    `{"watched":true}`,
		DispatchStatus: TopicEventDispatchPending,
		PublisherType:  TopicEventSourceLoader,
		PublisherID:    run.ProjectID,
		PublisherRunID: run.RunID,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}

	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewEventServiceHandler(service)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := agentcomposev2connect.NewEventServiceClient(server.Client(), server.URL)
	watchCtx, cancel := context.WithCancel(context.Background())
	stream, err := client.WatchEvents(watchCtx, connect.NewRequest(&agentcomposev2.WatchEventsRequest{
		ProjectId: run.ProjectID,
		Topic:     created.Topic,
	}))
	if err != nil {
		t.Fatalf("WatchEvents returned error: %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("WatchEvents first receive error = %v", stream.Err())
	}
	event := stream.Msg()
	if event.GetEventId() != created.ID || event.GetProjectId() != run.ProjectID || event.GetTopic() != created.Topic {
		t.Fatalf("WatchEvents event = %#v", event)
	}

	cancel()
	done := make(chan error, 1)
	go func() {
		for stream.Receive() {
		}
		done <- stream.Err()
	}()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) && connect.CodeOf(err) != connect.CodeCanceled {
			t.Fatalf("WatchEvents after cancel error = %v", err)
		}
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			t.Fatalf("WatchEvents returned unimplemented after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchEvents did not stop after context cancel")
	}
}

func createObservabilityRun(t *testing.T, ctx context.Context, store *ConfigStore, run ProjectRunRecord) ProjectRunRecord {
	t.Helper()
	project, err := NewProjectRecordFromSpec(&compose.NormalizedProjectSpec{Name: run.ProjectName}, filepath.Join(t.TempDir(), "agent-compose.yml"))
	if err != nil {
		t.Fatalf("NewProjectRecordFromSpec returned error: %v", err)
	}
	project.ID = run.ProjectID
	project.Name = run.ProjectName
	if _, err := store.UpsertProject(ctx, project); err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	created, err := store.CreateProjectRun(ctx, run)
	if err != nil {
		t.Fatalf("CreateProjectRun returned error: %v", err)
	}
	return created
}
