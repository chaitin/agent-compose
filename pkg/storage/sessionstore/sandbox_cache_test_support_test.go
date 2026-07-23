package sessionstore

import (
	"context"
	"database/sql"
	"fmt"

	storagesqlite "agent-compose/pkg/storage/sqlite"

	_ "modernc.org/sqlite"
)

func openSandboxCache(path string) (*sandboxCache, bool, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, false, fmt.Errorf("open data database for sandbox listing cache: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := storagesqlite.Migrate(context.Background(), db); err != nil {
		return nil, false, closeSandboxCacheDB(db, fmt.Errorf("migrate data database for sandbox listing cache: %w", err))
	}
	idx, needsRebuild, err := openSandboxCacheDB(context.Background(), db)
	if err != nil {
		return nil, false, closeSandboxCacheDB(db, err)
	}
	idx.ownsDB = true
	return idx, needsRebuild, nil
}
