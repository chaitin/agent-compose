package configstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestSandboxStoreCreatesSchemaAndUpsertsSummary(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	createdAt := time.Date(2026, time.July, 17, 8, 0, 0, 123456789, time.UTC)
	updatedAt := createdAt.Add(time.Minute + 123*time.Nanosecond)
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "sandbox-record-id",
		ShortID:       "sandbox-reco",
		Title:         "recorded sandbox",
		TriggerSource: "api",
		Driver:        "docker",
		VMStatus:      domain.VMStatusPending,
		GuestImage:    "guest:latest",
		PullPolicy:    "if-not-present",
		RuntimeRef:    "agent-compose-sandbox-reco",
		WorkspacePath: "/data/sandboxes/sandbox-record-id/workspace",
		ProxyPath:     "/jupyter/sandbox-record-id/lab",
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		CellCount:     2,
		EventCount:    3,
	}}
	if err := store.UpsertSandbox(ctx, sandbox); err != nil {
		t.Fatalf("upsert sandbox: %v", err)
	}
	var initialTagsJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT tags_json FROM sandboxes WHERE id = ?`, sandbox.Summary.ID).Scan(&initialTagsJSON); err != nil {
		t.Fatalf("query initial tags: %v", err)
	}
	if initialTagsJSON != `[]` {
		t.Fatalf("initial tags_json = %q, want []", initialTagsJSON)
	}

	originalCreatedAt := sandbox.Summary.CreatedAt
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	sandbox.Summary.CreatedAt = createdAt.Add(time.Hour)
	sandbox.Summary.UpdatedAt = updatedAt.Add(time.Minute + 321*time.Nanosecond)
	sandbox.Summary.Tags = []domain.SandboxTag{{Name: "origin", Value: "test"}}
	if err := store.UpsertSandbox(ctx, sandbox); err != nil {
		t.Fatalf("update sandbox: %v", err)
	}
	newestUpdatedAt := sandbox.Summary.UpdatedAt
	sandbox.Summary.VMStatus = domain.VMStatusStopped
	sandbox.Summary.UpdatedAt = updatedAt.Add(30 * time.Second)
	if err := store.UpsertSandbox(ctx, sandbox); err != nil {
		t.Fatalf("apply stale sandbox update: %v", err)
	}

	var status, tagsJSON string
	var createdNanos, updatedNanos int64
	err := store.db.QueryRowContext(ctx, `SELECT vm_status, tags_json, created_at, updated_at FROM sandboxes WHERE id = ?`, sandbox.Summary.ID).
		Scan(&status, &tagsJSON, &createdNanos, &updatedNanos)
	if err != nil {
		t.Fatalf("query sandbox: %v", err)
	}
	if status != domain.VMStatusRunning {
		t.Fatalf("status = %q, want %q", status, domain.VMStatusRunning)
	}
	if tagsJSON != `[{"name":"origin","value":"test"}]` {
		t.Fatalf("tags_json = %q", tagsJSON)
	}
	if createdNanos != originalCreatedAt.UnixNano() || updatedNanos != newestUpdatedAt.UnixNano() {
		t.Fatalf("timestamps = (%d, %d), want (%d, %d)", createdNanos, updatedNanos, originalCreatedAt.UnixNano(), newestUpdatedAt.UnixNano())
	}

	loaded, err := store.GetSandboxSummary(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("get sandbox summary: %v", err)
	}
	if loaded.VMStatus != domain.VMStatusRunning || !loaded.CreatedAt.Equal(originalCreatedAt) || !loaded.UpdatedAt.Equal(newestUpdatedAt) {
		t.Fatalf("loaded summary = %#v", loaded)
	}
}

func TestSandboxStoreRejectsInvalidSandbox(t *testing.T) {
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(context.Background()); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if err := store.UpsertSandbox(context.Background(), nil); err == nil {
		t.Fatal("upsert nil sandbox succeeded")
	}
	if err := store.UpsertSandbox(context.Background(), &domain.Sandbox{}); err == nil {
		t.Fatal("upsert sandbox without id succeeded")
	}
}

func TestSandboxStoreListsAndDeletesSummaries(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	baseTime := time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC)
	for index, item := range []struct {
		id     string
		driver string
		status string
	}{
		{id: "sandbox-a", driver: "docker", status: domain.VMStatusRunning},
		{id: "sandbox-b", driver: "docker", status: domain.VMStatusRunning},
		{id: "sandbox-c", driver: "boxlite", status: domain.VMStatusStopped},
	} {
		sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
			ID:        item.id,
			ShortID:   item.id,
			Driver:    item.driver,
			VMStatus:  item.status,
			CreatedAt: baseTime.Add(time.Duration(index) * time.Nanosecond),
			UpdatedAt: baseTime.Add(time.Duration(index) * time.Nanosecond),
		}}
		if err := store.UpsertSandbox(ctx, sandbox); err != nil {
			t.Fatalf("upsert %s: %v", item.id, err)
		}
	}

	first, err := store.ListSandboxSummaries(ctx, domain.SandboxSummaryListOptions{Driver: "DOCKER", VMStatus: "running", Limit: 1})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first.Sandboxes) != 1 || first.Sandboxes[0].ID != "sandbox-b" || !first.HasMore {
		t.Fatalf("first page = %#v", first)
	}
	second, err := store.ListSandboxSummaries(ctx, domain.SandboxSummaryListOptions{
		Driver:          "docker",
		VMStatus:        domain.VMStatusRunning,
		BeforeUpdatedAt: first.Sandboxes[0].UpdatedAt,
		BeforeID:        first.Sandboxes[0].ID,
		Limit:           1,
	})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second.Sandboxes) != 1 || second.Sandboxes[0].ID != "sandbox-a" || second.HasMore {
		t.Fatalf("second page = %#v", second)
	}

	if err := store.DeleteSandbox(ctx, "sandbox-b"); err != nil {
		t.Fatalf("delete sandbox: %v", err)
	}
	if _, err := store.GetSandboxSummary(ctx, "sandbox-b"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get deleted sandbox error = %v, want sql.ErrNoRows", err)
	}
}

func TestSandboxStoreListRejectsIncompleteCursor(t *testing.T) {
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(context.Background()); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	for name, options := range map[string]domain.SandboxSummaryListOptions{
		"missing id":        {BeforeUpdatedAt: time.Now().UTC()},
		"missing timestamp": {BeforeID: "sandbox-cursor"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.ListSandboxSummaries(context.Background(), options); err == nil {
				t.Fatal("list sandboxes accepted an incomplete cursor")
			}
		})
	}
}
