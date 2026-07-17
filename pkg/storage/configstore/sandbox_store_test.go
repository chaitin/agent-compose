package configstore

import (
	"context"
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

	createdAt := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
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
		Tags:          []domain.SandboxTag{{Name: "origin", Value: "test"}},
	}}
	if err := store.UpsertSandbox(ctx, sandbox); err != nil {
		t.Fatalf("upsert sandbox: %v", err)
	}

	sandbox.Summary.VMStatus = domain.VMStatusRunning
	sandbox.Summary.UpdatedAt = updatedAt.Add(time.Minute)
	if err := store.UpsertSandbox(ctx, sandbox); err != nil {
		t.Fatalf("update sandbox: %v", err)
	}

	var status, tagsJSON string
	var createdMillis, updatedMillis int64
	err := store.db.QueryRowContext(ctx, `SELECT vm_status, tags_json, created_at, updated_at FROM sandboxes WHERE id = ?`, sandbox.Summary.ID).
		Scan(&status, &tagsJSON, &createdMillis, &updatedMillis)
	if err != nil {
		t.Fatalf("query sandbox: %v", err)
	}
	if status != domain.VMStatusRunning {
		t.Fatalf("status = %q, want %q", status, domain.VMStatusRunning)
	}
	if tagsJSON != `[{"name":"origin","value":"test"}]` {
		t.Fatalf("tags_json = %q", tagsJSON)
	}
	if createdMillis != createdAt.UnixMilli() || updatedMillis != sandbox.Summary.UpdatedAt.UnixMilli() {
		t.Fatalf("timestamps = (%d, %d), want (%d, %d)", createdMillis, updatedMillis, createdAt.UnixMilli(), sandbox.Summary.UpdatedAt.UnixMilli())
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
