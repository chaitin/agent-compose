package sessionstore

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

func openSandboxCache(path string) (*sandboxCache, bool, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, false, fmt.Errorf("open data database for sandbox listing cache: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	idx, needsRebuild, err := openSandboxCacheDB(context.Background(), db)
	if err != nil {
		return nil, false, closeSandboxCacheDB(db, err)
	}
	idx.ownsDB = true
	return idx, needsRebuild, nil
}
