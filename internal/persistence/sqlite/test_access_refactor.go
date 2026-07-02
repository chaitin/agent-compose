package sqlite

import (
	"context"
	"database/sql"
)

func NewConfigStoreForDB(db *sql.DB) *ConfigStore {
	return &ConfigStore{db: db}
}

func (s *ConfigStore) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *ConfigStore) InitSchemaForTest() error {
	return s.initSchema(context.Background())
}

func StoredUnixMillisecondThreshold() int64 {
	return storedUnixMillisecondThreshold
}

func (s *ConfigStore) EnsureLoaderSchemaForTest() error {
	return s.ensureLoaderSchema(context.Background())
}
