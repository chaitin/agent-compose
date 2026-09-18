package sandboxstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

const (
	VMStatusPending = domain.VMStatusPending
	VMStatusRunning = domain.VMStatusRunning
	VMStatusStopped = domain.VMStatusStopped
	VMStatusFailed  = domain.VMStatusFailed

	CellTypeAgent = execution.CellTypeAgent

	sandboxCacheWriteTimeout = 5 * time.Second
)

type (
	SandboxTag         = domain.SandboxTag
	SandboxEnvVar      = domain.SandboxEnvVar
	SandboxSummary     = domain.SandboxSummary
	SandboxListOptions = domain.SandboxListOptions
	SandboxListResult  = domain.SandboxListResult
	SandboxWorkspace   = domain.SandboxWorkspace
	Sandbox            = domain.Sandbox
	NotebookCell       = domain.NotebookCell
	SandboxEvent       = domain.SandboxEvent
	AgentRun           = domain.AgentRun
	VMState            = domain.VMState
	ProxyState         = domain.ProxyState
)

type Store struct {
	config                *appconfig.Config
	layout                *sandboxLayout
	now                   func() time.Time
	database              *storagesqlite.Database
	sandboxLocks          sync.Map
	cacheDependencyMu     sync.RWMutex
	cacheDependencyLocker CacheDependencyLocker
	index                 *sandboxCache
	indexRepairs          *sandboxIndexRepairs
	projectResolver       SandboxProjectResolver
}

type CacheDependencyLocker interface {
	WithLockContext(context.Context, func() error) error
}

// NewWithConfig constructs a standalone Store with its own application
// database. Production composition should use NewWithDatabase so every store
// shares the dependency-injected database.
func NewWithConfig(config *appconfig.Config) (*Store, error) {
	if err := os.MkdirAll(config.SandboxRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create sandbox root: %w", err)
	}
	dbPath := strings.TrimSpace(config.DbAddr)
	if dbPath == "" {
		dbPath = filepath.Join(config.SandboxRoot, "data.db")
	}
	database, err := storagesqlite.OpenWithMaxOpenConns(dbPath, config.DbTimeout, config.EffectiveSQLiteMaxOpenConns())
	if err != nil {
		return newStoreWithIndex(config, storeIndexInit{DBPath: dbPath, IndexErr: err})
	}
	index, _, indexErr := openSandboxCacheDB(context.Background(), database.DB())
	store, err := newStoreWithIndex(config, storeIndexInit{Index: index, DBPath: dbPath, IndexErr: indexErr})
	if err != nil {
		return nil, errors.Join(err, database.Close())
	}
	if indexErr != nil {
		if err := database.Close(); err != nil {
			return nil, fmt.Errorf("close unavailable standalone sandbox database: %w", err)
		}
		return store, nil
	}
	store.database = database
	return store, nil
}

// NewWithDatabase constructs a Store using the application's shared data.db
// connection. The caller owns db; closing the Store only releases index-owned
// resources and never closes the shared database. The database must have been
// opened with storage/sqlite.Open or successfully passed through
// storage/sqlite.Migrate before this function is called.
func NewWithDatabase(config *appconfig.Config, db *sql.DB, projectResolvers ...SandboxProjectResolver) (*Store, error) {
	index, _, err := openSandboxCacheDB(context.Background(), db)
	var projectResolver SandboxProjectResolver
	if len(projectResolvers) > 0 {
		projectResolver = projectResolvers[0]
	}
	return newStoreWithIndex(config, storeIndexInit{Index: index, DBPath: config.DbAddr, IndexErr: err, ProjectResolver: projectResolver})
}

// storeIndexInit bundles the sandbox listing cache and the outcome of trying to open it.
type storeIndexInit struct {
	Index           *sandboxCache
	DBPath          string
	IndexErr        error
	ProjectResolver SandboxProjectResolver
}

func newStoreWithIndex(config *appconfig.Config, init storeIndexInit) (*Store, error) {
	index, dbPath, indexErr, projectResolver := init.Index, init.DBPath, init.IndexErr, init.ProjectResolver
	if err := os.MkdirAll(config.SandboxRoot, 0o755); err != nil {
		return nil, closeSandboxCacheAfterStoreInitFailure(index, fmt.Errorf("create sandbox root: %w", err))
	}
	store := &Store{
		config:          config,
		layout:          newSandboxLayout(config.SandboxRoot),
		now:             time.Now,
		projectResolver: projectResolver,
	}
	if _, err := store.layout.discover(); err != nil {
		return nil, closeSandboxCacheAfterStoreInitFailure(index, fmt.Errorf("discover sandbox directories: %w", err))
	}
	if err := store.backfillOwnershipRecords(); err != nil {
		return nil, closeSandboxCacheAfterStoreInitFailure(index, err)
	}
	if indexErr != nil {
		slog.Warn("sandbox listing cache unavailable; using filesystem listing", "database", dbPath, "error", indexErr)
		return store, nil
	}
	store.index = index
	// The filesystem is authoritative. Reconcile on every startup, including
	// when the schema version is current, to repair a process exit between a
	// metadata commit and its write-through index update.
	if err := store.completeIndexRebuild(context.Background()); err != nil {
		if !errors.Is(err, errSandboxCache) {
			if closeErr := index.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close sandbox listing cache after reconciliation failure: %w", closeErr))
			}
			return nil, fmt.Errorf("reconcile sandbox listing cache: %w", err)
		}
		if err := store.retrySandboxCacheRebuild(context.Background(), err); err != nil {
			slog.Warn("sandbox listing cache recovery failed; using filesystem listing", "database", dbPath, "error", err)
			store.index = nil
			return store, nil
		}
	}
	store.startIndexRepairs()
	return store, nil
}

func closeSandboxCacheAfterStoreInitFailure(index *sandboxCache, operationErr error) error {
	if index == nil {
		return operationErr
	}
	if err := index.Close(); err != nil {
		return errors.Join(operationErr, fmt.Errorf("close sandbox listing cache after store initialization failure: %w", err))
	}
	return operationErr
}

func FromConfig(config *appconfig.Config) *Store {
	layout := newSandboxLayout(config.SandboxRoot)
	// Compatibility stores do not return construction errors. Prime known paths
	// when possible so ID-addressed reads work before the first filesystem list;
	// listing still performs discovery and reports any filesystem error.
	_, _ = layout.discover()
	return &Store{config: config, layout: layout, now: time.Now}
}

// Close releases database resources owned by compatibility stores. Stores
// created with NewWithDatabase leave the caller-owned shared database open.
func (s *Store) Close() error {
	if s.indexRepairs != nil {
		s.indexRepairs.stop()
	}
	var err error
	if s.index != nil {
		err = s.index.Close()
	}
	if s.database != nil {
		err = errors.Join(err, s.database.Close())
	}
	return err
}

// Shutdown adapts Close to the samber/do Shutdowner interface.
func (s *Store) Shutdown() error {
	return s.Close()
}

func (s *Store) SetCacheDependencyLocker(locker CacheDependencyLocker) {
	s.cacheDependencyMu.Lock()
	defer s.cacheDependencyMu.Unlock()
	s.cacheDependencyLocker = locker
}
