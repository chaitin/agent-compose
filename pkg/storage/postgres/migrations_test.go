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
	if migrations[0].version != baselineMigrationVersion {
		t.Fatalf("first migration version = %d", migrations[0].version)
	}
	for _, migration := range migrations {
		if migration.version <= baselineMigrationVersion {
			continue
		}
		sqliteMigration, ok, err := storagesqlite.EmbeddedMigrationAt(migration.version)
		if err != nil {
			t.Fatalf("read SQLite migration %d: %v", migration.version, err)
		}
		if !ok {
			t.Fatalf("PostgreSQL migration %06d has no SQLite counterpart", migration.version)
		}
		if sqliteMigration.Name != migration.name {
			t.Fatalf("migration %06d name = %q in SQLite, %q in PostgreSQL", migration.version, sqliteMigration.Name, migration.name)
		}
	}
}
