package postgres

import (
	"testing"

	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

// TestPostgresMigrationsPairWithSQLite pins the maintenance rule: after the
// baseline, every PostgreSQL migration must have a SQLite migration with the
// same version and name so the two chains cannot drift silently.
func TestPostgresMigrationsPairWithSQLite(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("load PostgreSQL migrations: %v", err)
	}
	if migrations[0].Version != baselineMigrationVersion {
		t.Fatalf("first migration version = %d", migrations[0].Version)
	}
	for _, migration := range migrations {
		if migration.Version <= baselineMigrationVersion {
			continue
		}
		sqliteMigration, ok, err := storagesqlite.EmbeddedMigrationAt(migration.Version)
		if err != nil {
			t.Fatalf("read SQLite migration %d: %v", migration.Version, err)
		}
		if !ok {
			t.Fatalf("PostgreSQL migration %06d has no SQLite counterpart", migration.Version)
		}
		if sqliteMigration.Name != migration.Name {
			t.Fatalf("migration %06d name = %q in SQLite, %q in PostgreSQL", migration.Version, sqliteMigration.Name, migration.Name)
		}
	}
}
