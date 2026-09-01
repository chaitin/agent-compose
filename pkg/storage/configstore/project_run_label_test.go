package configstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestCreateProjectRunPersistsAndRoundTripsLabels(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-labels", Name: "labels"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: "project-labels", AgentName: "worker"})

	labels := map[string]string{"env": "prod", "team": "platform"}
	created, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-labeled", ProjectID: "project-labels", AgentName: "worker", AgentID: agentID,
		Status: domain.ProjectRunStatusRunning, Labels: labels,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if len(created.Labels) != 2 || created.Labels["env"] != "prod" || created.Labels["team"] != "platform" {
		t.Fatalf("created run labels = %#v, want %#v", created.Labels, labels)
	}

	fetched, err := store.GetProjectRun(ctx, "run-labeled")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if len(fetched.Labels) != 2 || fetched.Labels["env"] != "prod" || fetched.Labels["team"] != "platform" {
		t.Fatalf("fetched run labels = %#v, want %#v", fetched.Labels, labels)
	}

	unlabeled, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-unlabeled", ProjectID: "project-labels", AgentName: "worker", AgentID: agentID,
		Status: domain.ProjectRunStatusRunning,
	})
	if err != nil {
		t.Fatalf("create unlabeled run: %v", err)
	}
	if unlabeled.Labels != nil {
		t.Fatalf("unlabeled created run labels = %#v, want nil", unlabeled.Labels)
	}
	fetchedUnlabeled, err := store.GetProjectRun(ctx, "run-unlabeled")
	if err != nil {
		t.Fatalf("get unlabeled run: %v", err)
	}
	if fetchedUnlabeled.Labels != nil {
		t.Fatalf("fetched unlabeled run labels = %#v, want nil", fetchedUnlabeled.Labels)
	}
}

func TestListProjectRunsByOptionsFiltersByLabelsWithAndSemantics(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-label-filter", Name: "label-filter"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: "project-label-filter", AgentName: "worker"})

	runs := map[string]map[string]string{
		"run-prod-platform": {"env": "prod", "team": "platform"},
		"run-prod-search":   {"env": "prod", "team": "search"},
		"run-staging":       {"env": "staging", "team": "platform"},
		"run-no-labels":     nil,
	}
	for runID, labels := range runs {
		if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
			RunID: runID, ProjectID: "project-label-filter", AgentName: "worker", AgentID: agentID,
			Status: domain.ProjectRunStatusRunning, Labels: labels,
		}); err != nil {
			t.Fatalf("create run %s: %v", runID, err)
		}
	}

	tests := []struct {
		name   string
		labels map[string]string
		want   map[string]bool
	}{
		{name: "no filter", labels: nil, want: runIDs("run-prod-platform", "run-prod-search", "run-staging", "run-no-labels")},
		{name: "single label", labels: map[string]string{"env": "prod"}, want: runIDs("run-prod-platform", "run-prod-search")},
		{name: "multi label AND", labels: map[string]string{"env": "prod", "team": "platform"}, want: runIDs("run-prod-platform")},
		{name: "no match", labels: map[string]string{"team": "search", "env": "staging"}, want: map[string]bool{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := domain.ProjectRunListOptions{ProjectID: "project-label-filter", Labels: tt.labels, Limit: 50}
			got, err := store.ListProjectRunsByOptions(ctx, options)
			if err != nil {
				t.Fatalf("list runs: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("run count = %d, want %d: %#v", len(got), len(tt.want), got)
			}
			for _, run := range got {
				if !tt.want[run.RunID] {
					t.Errorf("unexpected run %q", run.RunID)
				}
			}
			total, err := store.CountProjectRuns(ctx, options)
			if err != nil || total != len(tt.want) {
				t.Fatalf("count = %d, want %d (err=%v)", total, len(tt.want), err)
			}
		})
	}
}

func TestInsertProjectRunLabelsTxRejectsInvalidKeysAndValues(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-invalid-labels", Name: "invalid-labels"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: "project-invalid-labels", AgentName: "worker"})

	tests := []struct {
		name    string
		runID   string
		labels  map[string]string
		wantErr string // empty means the labels must be accepted as-is
	}{
		{name: "empty key", runID: "run-empty-key", labels: map[string]string{"": "value"}, wantErr: "must not be empty"},
		{name: "whitespace key", runID: "run-whitespace-key", labels: map[string]string{"   ": "value"}, wantErr: "must not be empty"},
		{name: "oversized key", runID: "run-oversized-key", labels: map[string]string{strings.Repeat("k", maxProjectRunLabelKeyLen+1): "value"}, wantErr: "exceeds"},
		{name: "oversized value", runID: "run-oversized-value", labels: map[string]string{"key": strings.Repeat("v", maxProjectRunLabelValueLen+1)}, wantErr: "exceeds"},
		{name: "max length key accepted", runID: "run-max-key", labels: map[string]string{strings.Repeat("k", maxProjectRunLabelKeyLen): "value"}},
		{name: "max length value accepted", runID: "run-max-value", labels: map[string]string{"key": strings.Repeat("v", maxProjectRunLabelValueLen)}},
		{name: "empty value accepted", runID: "run-empty-value", labels: map[string]string{"key": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
				RunID: tt.runID, ProjectID: "project-invalid-labels", AgentName: "worker", AgentID: agentID,
				Status: domain.ProjectRunStatusRunning, Labels: tt.labels,
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("create run error = %v, want substring %q", err, tt.wantErr)
				}
				if _, err := store.GetProjectRun(ctx, tt.runID); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("run survived rejected label insert: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("create run: %v", err)
			}
			for key, value := range tt.labels {
				got, ok := created.Labels[key]
				if !ok || got != value {
					t.Fatalf("created run label %q = (%q, present=%v), want (%q, present=true)", key, got, ok, value)
				}
			}
			fetched, err := store.GetProjectRun(ctx, tt.runID)
			if err != nil {
				t.Fatalf("get run: %v", err)
			}
			for key, value := range tt.labels {
				got, ok := fetched.Labels[key]
				if !ok || got != value {
					t.Fatalf("fetched run label %q = (%q, present=%v), want (%q, present=true)", key, got, ok, value)
				}
			}
		})
	}
}

func TestCreateProjectRunWithEventsRetryPreservesOriginalLabels(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-retry-labels", Name: "retry-labels"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: "project-retry-labels", AgentName: "worker"})

	run := domain.ProjectRunRecord{
		RunID: "run-retry-labels", ProjectID: "project-retry-labels", AgentName: "worker", AgentID: agentID,
		Status: domain.ProjectRunStatusRunning, Labels: map[string]string{"attempt": "first"},
	}
	first, err := store.CreateProjectRunWithEvents(ctx, run, nil)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if first.Labels["attempt"] != "first" {
		t.Fatalf("first create labels = %#v, want attempt=first", first.Labels)
	}

	retry := run
	retry.Labels = map[string]string{"attempt": "second"}
	second, err := store.CreateProjectRunWithEvents(ctx, retry, nil)
	if err != nil {
		t.Fatalf("retry create: %v", err)
	}
	if second.Labels["attempt"] != "first" {
		t.Fatalf("retry create labels = %#v, want original attempt=first preserved", second.Labels)
	}

	stored, err := store.GetProjectRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if len(stored.Labels) != 1 || stored.Labels["attempt"] != "first" {
		t.Fatalf("stored labels = %#v, want only attempt=first", stored.Labels)
	}
}

func TestInsertProjectRunLabelsTxUpsertsOnConflict(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	store := FromDB(db)
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-upsert-labels", Name: "upsert-labels"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: "project-upsert-labels", AgentName: "worker"})
	if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-upsert-labels", ProjectID: "project-upsert-labels", AgentName: "worker", AgentID: agentID,
		Status: domain.ProjectRunStatusRunning, Labels: map[string]string{"env": "staging"},
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	if err := withTx(ctx, db, func(tx *sql.Tx) error {
		return insertProjectRunLabelsTx(ctx, tx, "run-upsert-labels", map[string]string{"env": "prod"})
	}); err != nil {
		t.Fatalf("upsert label: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_run_label WHERE run_id = ? AND key = ?`, "run-upsert-labels", "env").Scan(&count); err != nil {
		t.Fatalf("count label rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("label row count = %d, want 1 (no duplicate row on conflict)", count)
	}

	stored, err := store.GetProjectRun(ctx, "run-upsert-labels")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if stored.Labels["env"] != "prod" {
		t.Fatalf("label after upsert = %q, want prod", stored.Labels["env"])
	}
}

func withTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
