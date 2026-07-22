package testutil

import (
	"fmt"
	"os"
	"testing"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	storagesqlite "agent-compose/pkg/storage/sqlite"
)

// OpenConfigStore opens a migrated test database from the dependencies in di.
// The database is closed automatically when the test completes.
func OpenConfigStore(t testing.TB, di do.Injector) (*configstore.ConfigStore, error) {
	t.Helper()
	config := do.MustInvoke[*appconfig.Config](di)
	database, err := openDatabase(t, config)
	if err != nil {
		return nil, err
	}
	return configstore.FromDB(database.DB()), nil
}

// OpenStores opens config and sandbox stores over one migrated test database.
// Both stores and the database are closed automatically with the test.
func OpenStores(t testing.TB, config *appconfig.Config) (*configstore.ConfigStore, *sessionstore.Store, error) {
	t.Helper()
	database, err := openDatabase(t, config)
	if err != nil {
		return nil, nil, err
	}
	sandboxes, err := sessionstore.NewWithDatabase(config, database.DB())
	if err != nil {
		return nil, nil, err
	}
	t.Cleanup(func() {
		if err := sandboxes.Close(); err != nil {
			t.Errorf("close sandbox store: %v", err)
		}
	})
	return configstore.FromDB(database.DB()), sandboxes, nil
}

func openDatabase(t testing.TB, config *appconfig.Config) (*storagesqlite.Database, error) {
	t.Helper()
	if err := os.MkdirAll(config.DataRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create test data root: %w", err)
	}
	database, err := storagesqlite.Open(config.DbAddr, config.DbTimeout)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	return database, nil
}
