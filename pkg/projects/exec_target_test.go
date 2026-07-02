package projects

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestResolveExecTargetSessionBySessionID(t *testing.T) {
	ctx := context.Background()
	configDB, store := newExecTargetTestStores(t)
	session := createExecTargetTestSession(t, ctx, store, model.VMStatusRunning)

	resolved, runID, err := ResolveExecTargetSession(ctx, configDB, store, &agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_SessionId{SessionId: session.Summary.ID},
	})
	if err != nil {
		t.Fatalf("ResolveExecTargetSession returned error: %v", err)
	}
	if resolved.Summary.ID != session.Summary.ID || runID != "" {
		t.Fatalf("resolved session/run = %s/%s, want %s/empty", resolved.Summary.ID, runID, session.Summary.ID)
	}
}

func TestResolveExecTargetSessionByRunIDRequiresRunningSession(t *testing.T) {
	ctx := context.Background()
	configDB, store := newExecTargetTestStores(t)
	createExecTargetTestProject(t, ctx, configDB, "project-exec", "exec")
	session := createExecTargetTestSession(t, ctx, store, model.VMStatusPending)
	createExecTargetTestRun(t, ctx, configDB, "run-stopped", "project-exec", "exec", "reviewer", session.Summary.ID)

	_, _, err := ResolveExecTargetSession(ctx, configDB, store, &agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_RunId{RunId: "run-stopped"},
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "session "+session.Summary.ID+" for run run-stopped is not running") {
		t.Fatalf("ResolveExecTargetSession stopped run error = %v, code %s", err, connect.CodeOf(err))
	}
}

func TestResolveExecTargetSessionByRunIDNotFound(t *testing.T) {
	ctx := context.Background()
	configDB, store := newExecTargetTestStores(t)

	_, _, err := ResolveExecTargetSession(ctx, configDB, store, &agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_RunId{RunId: "missing-run"},
	})
	if connect.CodeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), "run missing-run not found") {
		t.Fatalf("ResolveExecTargetSession missing run error = %v, code %s", err, connect.CodeOf(err))
	}
}

func TestResolveExecTargetSessionSelector(t *testing.T) {
	ctx := context.Background()
	configDB, store := newExecTargetTestStores(t)
	createExecTargetTestProject(t, ctx, configDB, "project-exec", "exec")
	session := createExecTargetTestSession(t, ctx, store, model.VMStatusRunning)
	createExecTargetTestRun(t, ctx, configDB, "run-exec", "project-exec", "exec", "reviewer", session.Summary.ID)

	resolved, runID, err := ResolveExecTargetSession(ctx, configDB, store, &agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_Selector{Selector: &agentcomposev2.ExecSessionSelector{
			ProjectId: "project-exec",
			AgentName: "reviewer",
		}},
	})
	if err != nil {
		t.Fatalf("ResolveExecTargetSession selector returned error: %v", err)
	}
	if resolved.Summary.ID != session.Summary.ID || runID != "run-exec" {
		t.Fatalf("resolved selector session/run = %s/%s, want %s/run-exec", resolved.Summary.ID, runID, session.Summary.ID)
	}
}

func TestResolveExecTargetSessionSelectorErrorsWhenNoRunningSession(t *testing.T) {
	ctx := context.Background()
	configDB, store := newExecTargetTestStores(t)
	createExecTargetTestProject(t, ctx, configDB, "project-exec", "exec")
	session := createExecTargetTestSession(t, ctx, store, model.VMStatusPending)
	createExecTargetTestRun(t, ctx, configDB, "run-pending", "project-exec", "exec", "reviewer", session.Summary.ID)

	_, _, err := ResolveExecTargetSession(ctx, configDB, store, &agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_Selector{Selector: &agentcomposev2.ExecSessionSelector{
			ProjectId: "project-exec",
			AgentName: "reviewer",
		}},
	})
	if connect.CodeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), "no running session found for project exec agent reviewer") {
		t.Fatalf("ResolveExecTargetSession no running selector error = %v, code %s", err, connect.CodeOf(err))
	}
}

func TestResolveExecTargetSessionSelectorErrorsWhenAmbiguous(t *testing.T) {
	ctx := context.Background()
	configDB, store := newExecTargetTestStores(t)
	createExecTargetTestProject(t, ctx, configDB, "project-exec", "exec")
	sessionA := createExecTargetTestSession(t, ctx, store, model.VMStatusRunning)
	sessionB := createExecTargetTestSession(t, ctx, store, model.VMStatusRunning)
	createExecTargetTestRun(t, ctx, configDB, "run-one", "project-exec", "exec", "reviewer", sessionA.Summary.ID)
	createExecTargetTestRun(t, ctx, configDB, "run-two", "project-exec", "exec", "reviewer", sessionB.Summary.ID)

	_, _, err := ResolveExecTargetSession(ctx, configDB, store, &agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_Selector{Selector: &agentcomposev2.ExecSessionSelector{
			ProjectId: "project-exec",
			AgentName: "reviewer",
		}},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "multiple running sessions found for project exec agent reviewer") {
		t.Fatalf("ResolveExecTargetSession ambiguous selector error = %v, code %s", err, connect.CodeOf(err))
	}
	if !strings.Contains(err.Error(), sessionA.Summary.ID) || !strings.Contains(err.Error(), sessionB.Summary.ID) {
		t.Fatalf("ambiguous selector error %q missing session ids %s/%s", err.Error(), sessionA.Summary.ID, sessionB.Summary.ID)
	}
}

func TestResolveExecTargetSessionRequiresTarget(t *testing.T) {
	ctx := context.Background()
	configDB, store := newExecTargetTestStores(t)

	_, _, err := ResolveExecTargetSession(ctx, configDB, store, &agentcomposev2.ExecRequest{})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "exec target is required") {
		t.Fatalf("ResolveExecTargetSession missing target error = %v, code %s", err, connect.CodeOf(err))
	}
}

func newExecTargetTestStores(t *testing.T) (*ConfigStore, *Store) {
	t.Helper()
	root := t.TempDir()
	configDB, err := storage.NewConfigStoreFromConfig(&appconfig.Config{DataRoot: root})
	if err != nil {
		t.Fatalf("NewConfigStoreFromConfig returned error: %v", err)
	}
	t.Cleanup(func() { _ = configDB.DB().Close() })
	store, err := storage.NewStoreFromConfig(&appconfig.Config{
		SessionRoot:          t.TempDir(),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		JupyterProxyBasePath: "/agent-compose/session",
	})
	if err != nil {
		t.Fatalf("NewStoreFromConfig returned error: %v", err)
	}
	return configDB, store
}

func createExecTargetTestProject(t *testing.T, ctx context.Context, store *ConfigStore, projectID, name string) {
	t.Helper()
	if _, err := store.UpsertProject(ctx, ProjectRecord{ID: projectID, Name: name}); err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
}

func createExecTargetTestSession(t *testing.T, ctx context.Context, store *Store, status string) *model.Session {
	t.Helper()
	session, err := store.CreateSession(ctx, "Exec Target", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", model.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = status
	if err := store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	return session
}

func createExecTargetTestRun(t *testing.T, ctx context.Context, store *ConfigStore, runID, projectID, projectName, agentName, sessionID string) {
	t.Helper()
	if _, err := store.CreateProjectRun(ctx, ProjectRunRecord{
		RunID:           runID,
		ProjectID:       projectID,
		ProjectName:     projectName,
		ProjectRevision: 1,
		AgentName:       agentName,
		ManagedAgentID:  "agent-" + agentName,
		Source:          ProjectRunSourceManual,
		Status:          ProjectRunStatusRunning,
		SessionID:       sessionID,
		ResultJSON:      "{}",
		Driver:          driverpkg.RuntimeDriverBoxlite,
		ImageRef:        "guest:latest",
	}); err != nil {
		t.Fatalf("CreateProjectRun(%s) returned error: %v", runID, err)
	}
}
