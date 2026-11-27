package configstore

import (
	"context"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
	"path/filepath"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestIntegrationRunLogHealthSurvivesStaleStatusUpdates(t *testing.T) {
	for _, events := range []bool{false, true} {
		name := "plain"
		if events {
			name = "transaction"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storagesqlite.Open(filepath.Join(t.TempDir(), "data.db"), 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store := FromDB(db.DB())
			project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "p", Name: "project"})
			if err != nil {
				t.Fatal(err)
			}
			agent := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: project.ID, AgentName: "worker"})
			stale, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{RunID: "r", ProjectID: project.ID, AgentID: agent, AgentName: "worker", Status: domain.ProjectRunStatusRunning})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.MarkProjectRunLogIncomplete(ctx, stale.RunID); err != nil {
				t.Fatal(err)
			}
			// A status update carrying the stale false must not clear the mark.
			stale.Status = domain.ProjectRunStatusRunning
			if events {
				_, err = store.UpdateProjectRunWithEvents(ctx, stale, []domain.ProjectRunEventRecord{{ID: "done", RunID: stale.RunID, Kind: domain.ProjectRunEventKindStatus}})
			} else {
				_, err = store.UpdateProjectRun(ctx, stale)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.GetProjectRun(ctx, stale.RunID)
			if err != nil || !got.LogIncomplete || got.Status != domain.ProjectRunStatusRunning {
				t.Fatalf("run=%+v err=%v", got, err)
			}
			got.Status = domain.ProjectRunStatusSucceeded
			if _, err := store.UpdateProjectRun(ctx, got); err != nil {
				t.Fatal(err)
			}
			terminal, err := store.GetProjectRun(ctx, got.RunID)
			if err != nil || !terminal.LogIncomplete {
				t.Fatalf("terminal run lost its log fault: %+v %v", terminal, err)
			}
		})
	}
}
