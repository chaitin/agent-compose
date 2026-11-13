package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// Exercise the production service graph and public HTTP API against persisted
// history. No sandbox driver or model is needed to remove completed history.
func TestE2EPublicRunHistoryRemoval(t *testing.T) {
	s := newSkillsPublicE2EService(t)
	db, err := storagesqlite.Open(filepath.Join(s.config.DataRoot, "data.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := configstore.FromDB(db.DB())
	projectID := identity.NewID(identity.ResourceProject, t.Name())
	agentID := identity.NewID(identity.ResourceAgent, projectID)
	schedulerID := identity.NewID(identity.ResourceScheduler, projectID)
	parentID := identity.NewID(identity.ResourceRun, "parent", projectID)
	childID := identity.NewID(identity.ResourceRun, "child", projectID)
	standaloneID := identity.NewID(identity.ResourceRun, "standalone", projectID)
	sandboxID := identity.NewID(identity.ResourceSandbox, projectID)
	if _, err := store.UpsertProject(s.ctx, domain.ProjectRecord{ID: projectID, Name: "run-history-e2e"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertProjectAgent(s.ctx, domain.ProjectAgentRecord{ID: agentID, ProjectID: projectID, AgentName: "worker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertProjectScheduler(s.ctx, domain.ProjectSchedulerRecord{ID: schedulerID, ProjectID: projectID, AgentName: "worker", SchedulerID: "main"}); err != nil {
		t.Fatal(err)
	}
	artifacts := filepath.Join(s.config.DataRoot, "schedulers", schedulerID, "runs", parentID)
	writeSkillsPublicE2EFile(t, filepath.Join(artifacts, "payload.json"), "private input")
	if err := store.CreateSchedulerRun(s.ctx, domain.SchedulerRunSummary{ID: parentID, SchedulerID: schedulerID, Status: domain.SchedulerRunStatusSucceeded, ArtifactsDir: artifacts, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []domain.ProjectRunRecord{
		{RunID: childID, ProjectID: projectID, AgentID: agentID, AgentName: "worker", SchedulerID: schedulerID, SchedulerRunID: parentID, SandboxID: sandboxID, Status: domain.ProjectRunStatusSucceeded},
		{RunID: standaloneID, ProjectID: projectID, AgentID: agentID, AgentName: "worker", Status: domain.ProjectRunStatusSucceeded},
	} {
		run.Labels = map[string]string{"test": "history"}
		if _, err := store.CreateProjectRun(s.ctx, run); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.AppendProjectRunEvent(s.ctx, domain.ProjectRunEventRecord{ID: "event-" + run.RunID, RunID: run.RunID, Kind: domain.ProjectRunEventKindAgentMessage, Text: "private output"}); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB().ExecContext(s.ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	request := &agentcomposev2.RemoveRunHistoryRequest{ProjectId: projectID, Kind: agentcomposev2.RunHistoryKind_RUN_HISTORY_KIND_SCHEDULER_RUN, SchedulerId: schedulerID, RunId: parentID}
	remove := func(request *agentcomposev2.RemoveRunHistoryRequest, want connect.Code) {
		t.Helper()
		_, err := s.projects.RemoveRunHistory(s.ctx, connect.NewRequest(request))
		if want == 0 && err != nil || want != 0 && (err == nil || connect.CodeOf(err) != want) {
			t.Fatalf("RemoveRunHistory: %v, want code %v", err, want)
		}
	}
	assertRefused := func() {
		t.Helper()
		remove(request, connect.CodeFailedPrecondition)
		assertSkillsPublicE2EFile(t, filepath.Join(artifacts, "payload.json"), "private input")
		if _, err := store.GetProjectRun(s.ctx, childID); err != nil {
			t.Fatalf("refusal removed child history: %v", err)
		}
	}
	remove(&agentcomposev2.RemoveRunHistoryRequest{ProjectId: projectID, Kind: agentcomposev2.RunHistoryKind_RUN_HISTORY_KIND_RUN, RunId: childID}, connect.CodeFailedPrecondition)
	remove(&agentcomposev2.RemoveRunHistoryRequest{ProjectId: projectID, Kind: agentcomposev2.RunHistoryKind_RUN_HISTORY_KIND_RUN, RunId: childID[:12]}, connect.CodeInvalidArgument)
	exec("UPDATE project_run SET status='running' WHERE run_id=?", childID)
	assertRefused()
	exec("UPDATE project_run SET status='succeeded' WHERE run_id=?", childID)
	exec("INSERT INTO project_run_completion(run_id,target_status,transition_json,cleanup_action,created_at,updated_at) VALUES(?,'succeeded','{}','none',1,1)", childID)
	assertRefused()
	exec("DELETE FROM project_run_completion WHERE run_id=?", childID)
	exec("UPDATE project_run SET sandbox_id=? WHERE run_id=?", sandboxID, standaloneID)
	assertRefused()
	exec("UPDATE project_run SET sandbox_id='' WHERE run_id=?", standaloneID)
	// Retained sandbox material must be removed through its lifecycle first.
	// This fixture represents old material without starting an external driver.
	// An unregistered date-partitioned directory has no metadata, so the
	// layout resolver falls back to a nonexistent flat path.
	sandboxDir := filepath.Join(s.config.SandboxRoot, "2026", "11", "12", sandboxID)
	writeSkillsPublicE2EFile(t, filepath.Join(sandboxDir, "keep.txt"), "retained")
	if s.store.SandboxDir(sandboxID) == sandboxDir {
		t.Fatal("fixture unexpectedly registered its dated sandbox")
	}
	assertRefused()
	assertSkillsPublicE2EFile(t, filepath.Join(sandboxDir, "keep.txt"), "retained")
	if err := os.RemoveAll(sandboxDir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateEvent(s.ctx, domain.TopicEventRecord{ID: "history-topic", Topic: "history.test", Source: domain.TopicEventSourceSystem, PayloadJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO event_delivery(event_id,scheduler_id,trigger_id,scheduler_run_id,status,created_at,updated_at) VALUES('history-topic',?,'trigger',?,'run_succeeded',1,1)", schedulerID, parentID)
	exec("INSERT INTO event_sandbox_link(event_id,sandbox_id,relation,scheduler_id,scheduler_run_id,created_at) VALUES('history-topic',?,'used',?,?,1)", sandboxID, schedulerID, parentID)
	exec("INSERT INTO scheduler_event(event_id,scheduler_id,scheduler_run_id,type,level,message,created_at) VALUES('history-event',?,?,'test','info','private',1)", schedulerID, parentID)
	for range 2 {
		remove(request, 0)
	}
	assertSkillsPublicE2EAbsent(t, artifacts)
	assertCount := func(table, column, value string, want int) {
		t.Helper()
		var got int
		if err := db.DB().QueryRowContext(s.ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+column+"=?", value).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: count=%d want=%d err=%v", table, got, want, err)
		}
	}
	for _, table := range []string{"project_run", "project_run_event", "project_run_label"} {
		assertCount(table, "run_id", childID, 0)
		assertCount(table, "run_id", standaloneID, 1)
	}
	assertCount("scheduler_run", "run_id", parentID, 0)
	for _, table := range []string{"scheduler_event", "event_delivery", "event_sandbox_link"} {
		assertCount(table, "scheduler_run_id", parentID, 0)
	}
	assertCount("event", "id", "history-topic", 1)
	assertCount("project", "id", projectID, 1)
	assertCount("project_scheduler", "id", schedulerID, 1)
	for range 2 {
		remove(&agentcomposev2.RemoveRunHistoryRequest{ProjectId: projectID, Kind: agentcomposev2.RunHistoryKind_RUN_HISTORY_KIND_RUN, RunId: standaloneID}, 0)
	}
	for _, table := range []string{"project_run", "project_run_event", "project_run_label"} {
		assertCount(table, "run_id", standaloneID, 0)
	}
}
