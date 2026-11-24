package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactDatabaseURL(t *testing.T) {
	got := redactDatabaseURL("postgres://user:supersecret@db.example.com:5432/agent_compose?sslmode=require")
	if strings.Contains(got, "supersecret") || strings.Contains(got, "user") {
		t.Fatalf("redactDatabaseURL() = %q, leaked credentials", got)
	}
	if got != "db.example.com:5432/agent_compose" {
		t.Fatalf("redactDatabaseURL() = %q", got)
	}
}

func TestWriteDatabaseStatus(t *testing.T) {
	var builder strings.Builder
	err := writeDatabaseStatus(&builder, dbStatusOutput{
		Driver:  "postgres",
		Target:  "db.example.com:5432/agent_compose",
		Applied: 1,
		Latest:  1,
		Migrations: []dbMigrationStatusOut{
			{Version: 1, Name: "000001_postgres_baseline", Applied: true},
		},
	})
	if err != nil {
		t.Fatalf("writeDatabaseStatus() error = %v", err)
	}
	output := builder.String()
	for _, want := range []string{"driver: postgres", "target: db.example.com:5432/agent_compose", "000001_postgres_baseline applied"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output %q does not contain %q", output, want)
		}
	}
}

func TestSQLiteAppliedVersionMissingDatabase(t *testing.T) {
	version, err := sqliteAppliedVersion(context.Background(), filepath.Join(t.TempDir(), "missing.db"))
	if err != nil {
		t.Fatalf("sqliteAppliedVersion() error = %v", err)
	}
	if version != 0 {
		t.Fatalf("sqliteAppliedVersion() = %d, want 0", version)
	}
}
