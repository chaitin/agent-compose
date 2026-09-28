package sqlite

import (
	"context"
	"testing"
)

func TestEventPublisherProjectMigrationBackfillsSchedulerEvents(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:4]); err != nil {
		t.Fatalf("apply v4 prefix: %v", err)
	}
	seedManagedV4Fixture(t, db)
	if err := applyMigrationSet(ctx, db, chain[:16]); err != nil {
		t.Fatalf("apply v16 prefix: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO event(id, topic, source, correlation_id, payload_hash, payload_json, dispatch_status, publisher_type, publisher_id, created_at) VALUES('pending-scheduler-event', 'workflow.x.ready', 'scheduler', 'c1', 'h1', '{}', 'pending', 'scheduler', 'scheduler-1', 3000)`,
		`INSERT INTO event(id, topic, source, correlation_id, payload_hash, payload_json, dispatch_status, publisher_type, publisher_id, created_at) VALUES('removed-scheduler-event', 'workflow.x.ready', 'scheduler', 'c2', 'h2', '{}', 'pending', 'scheduler', 'scheduler-removed', 3001)`,
		`INSERT INTO event(id, topic, source, correlation_id, payload_hash, payload_json, dispatch_status, publisher_type, publisher_id, created_at) VALUES('webhook-event', 'webhook.github.push', 'webhook', 'c3', 'h3', '{}', 'pending', 'webhook', 'scheduler-1', 3002)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed event with %q: %v", statement, err)
		}
	}
	if err := applyMigrationSet(ctx, db, chain[:17]); err != nil {
		t.Fatalf("apply event publisher project migration: %v", err)
	}
	for eventID, want := range map[string]string{
		"pending-scheduler-event": "project-1",
		"removed-scheduler-event": "",
		"webhook-event":           "",
		"topic-event-1":           "",
	} {
		var got string
		if err := db.QueryRowContext(ctx, `SELECT publisher_project_id FROM event WHERE id = ?`, eventID).Scan(&got); err != nil {
			t.Fatalf("read %s publisher project: %v", eventID, err)
		}
		if got != want {
			t.Fatalf("%s publisher project = %q, want %q", eventID, got, want)
		}
	}
}
