package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runhistory"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
)

type historyRemoverFunc func(context.Context, runhistory.Identity) error

func (f historyRemoverFunc) RemoveRunHistory(ctx context.Context, id runhistory.Identity) error {
	return f(ctx, id)
}

func TestRunHistoryHandlerValidationAndWriteEffects(t *testing.T) {
	projectID := identity.NewID(identity.ResourceProject, "project")
	runID := identity.NewID(identity.ResourceRun, "run")
	for _, name := range []string{"short_id", "missing_kind", "unknown_kind", "missing_scheduler", "unavailable", "referenced", "partial_cleanup"} {
		t.Run(name, func(t *testing.T) {
			request := &agentcomposev2.RemoveRunHistoryRequest{ProjectId: projectID, RunId: runID, Kind: agentcomposev2.RunHistoryKind_RUN_HISTORY_KIND_RUN}
			code, effect := connect.CodeInvalidArgument, apierror.EffectNotApplied
			calls := 0
			h := NewProjectHandlerWithAgentModels(ProjectHandlerDeps{RunHistory: historyRemoverFunc(func(_ context.Context, id runhistory.Identity) error {
				calls++
				if id.ProjectID != projectID || id.RunID != runID {
					t.Fatalf("identity changed: %+v", id)
				}
				if name == "referenced" {
					return apierror.NotApplied(domain.ClassifyError(domain.ErrReferenced, "shared", nil))
				}
				return errors.New("interrupted artifact cleanup")
			})})
			switch name {
			case "short_id":
				request.RunId = runID[:12]
			case "missing_kind":
				request.Kind = agentcomposev2.RunHistoryKind_RUN_HISTORY_KIND_UNSPECIFIED
			case "unknown_kind":
				request.Kind = agentcomposev2.RunHistoryKind(100)
			case "missing_scheduler":
				request.Kind = agentcomposev2.RunHistoryKind_RUN_HISTORY_KIND_SCHEDULER_RUN
			case "unavailable":
				h.runHistory = nil
				code = connect.CodeFailedPrecondition
			case "referenced":
				code = connect.CodeFailedPrecondition
			case "partial_cleanup":
				code, effect = connect.CodeInternal, apierror.EffectUnknown
			}
			_, err := h.RemoveRunHistory(context.Background(), connect.NewRequest(request))
			detail, ok := errdetail.From(err)
			if connect.CodeOf(err) != code || !ok || EffectFromProto(detail.Effect) != effect {
				t.Fatalf("err=%v detail=%+v", err, detail)
			}
			if name != "referenced" && name != "partial_cleanup" && calls != 0 {
				t.Fatal("invalid request reached mutation")
			}
		})
	}
}

func TestIntegrationRunHistoryConnectDeletesRealStoreAndArtifacts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := storagesqlite.Open(filepath.Join(root, "data.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	store := configstore.FromDB(db.DB())
	projectID := identity.NewID(identity.ResourceProject, "wire-project")
	agentID := identity.NewID(identity.ResourceAgent, "worker")
	schedulerID := identity.NewID(identity.ResourceScheduler, "scheduler")
	runID := identity.NewID(identity.ResourceRun, "parent")
	childID := identity.NewID(identity.ResourceRun, "child")
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: projectID, Name: "wire-project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ID: agentID, ProjectID: projectID, AgentName: "worker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertProjectScheduler(ctx, domain.ProjectSchedulerRecord{ID: schedulerID, ProjectID: projectID, AgentName: "worker", SchedulerID: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSchedulerRun(ctx, domain.SchedulerRunSummary{ID: runID, SchedulerID: schedulerID, Status: domain.SchedulerRunStatusSucceeded, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{RunID: childID, ProjectID: projectID, AgentName: "worker", AgentID: agentID, SchedulerID: schedulerID, SchedulerRunID: runID, Status: domain.ProjectRunStatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "schedulers", schedulerID, "runs", runID)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "payload.json"), []byte("private input"), 0o600); err != nil {
		t.Fatal(err)
	}
	controller := schedulers.NewController(schedulers.ControllerDependencies{})
	h := NewProjectHandlerWithAgentModels(ProjectHandlerDeps{
		Store:      store,
		RunHistory: &runhistory.Remover{Store: store, Schedulers: controller, Artifacts: runhistory.FSArtifacts{DataRoot: root}},
	})
	_, handler := agentcomposev2connect.NewProjectServiceHandler(h)
	server := httptest.NewServer(handler)
	defer server.Close()
	client := agentcomposev2connect.NewProjectServiceClient(server.Client(), server.URL)
	req := &agentcomposev2.RemoveRunHistoryRequest{ProjectId: projectID, Kind: agentcomposev2.RunHistoryKind_RUN_HISTORY_KIND_SCHEDULER_RUN, RunId: runID, SchedulerId: schedulerID}
	for range 2 {
		if _, err := client.RemoveRunHistory(ctx, connect.NewRequest(req)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.GetProjectRun(ctx, childID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("child remains: %v", err)
	}
	if _, err := store.GetSchedulerRun(ctx, schedulerID, runID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("parent remains: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifacts remain: %v", err)
	}
	if _, err := store.GetProject(ctx, projectID); err != nil {
		t.Fatal("project removed:", err)
	}
}
