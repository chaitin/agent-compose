package storage

import (
	appconfig "agent-compose/pkg/config"
	"path/filepath"
	"testing"
)

func newTestConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	root := t.TempDir()
	store, err := NewConfigStoreFromConfig(&appconfig.Config{
		DataRoot: root,
		DbAddr:   filepath.Join(root, "data.db"),
	})
	if err != nil {
		t.Fatalf("NewConfigStoreFromConfig returned error: %v", err)
	}
	t.Cleanup(func() { _ = store.db.Close() })
	return store
}
