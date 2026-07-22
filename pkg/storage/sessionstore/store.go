package sessionstore

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"

	"github.com/google/uuid"
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
	sandboxLocks          sync.Map
	cacheDependencyMu     sync.RWMutex
	cacheDependencyLocker CacheDependencyLocker
	index                 *sandboxCache
	indexRepairMu         sync.Mutex
	indexDirty            atomic.Bool
}

type CacheDependencyLocker interface {
	WithLockContext(context.Context, func() error) error
}

func NewWithConfig(config *appconfig.Config) (*Store, error) {
	if err := os.MkdirAll(config.SandboxRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create sandbox root: %w", err)
	}
	dbPath := strings.TrimSpace(config.DbAddr)
	if dbPath == "" {
		dbPath = filepath.Join(config.SandboxRoot, "data.db")
	}
	index, _, err := openSandboxCache(dbPath)
	if err != nil {
		return newStoreWithIndex(config, nil, dbPath, err)
	}
	return newStoreWithIndex(config, index, dbPath, nil)
}

// NewWithDatabase constructs a Store using the daemon's shared data.db
// connection. The caller owns db; closing the Store only releases index-owned
// resources and never closes the shared database.
func NewWithDatabase(config *appconfig.Config, db *sql.DB) (*Store, error) {
	index, _, err := openSandboxCacheDB(context.Background(), db)
	return newStoreWithIndex(config, index, config.DbAddr, err)
}

func newStoreWithIndex(config *appconfig.Config, index *sandboxCache, dbPath string, indexErr error) (*Store, error) {
	if err := os.MkdirAll(config.SandboxRoot, 0o755); err != nil {
		return nil, closeSandboxCacheAfterStoreInitFailure(index, fmt.Errorf("create sandbox root: %w", err))
	}
	store := &Store{config: config}
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
		if err := store.recreateSandboxCache(context.Background(), err); err != nil {
			slog.Warn("sandbox listing cache recovery failed; using filesystem listing", "database", dbPath, "error", err)
			return store, nil
		}
	}
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

func (s *Store) recreateSandboxCache(ctx context.Context, cause error) error {
	slog.Warn("sandbox listing cache reconciliation failed; rebuilding cache table", "error", cause)
	index := s.index
	if index == nil || index.db == nil {
		return fmt.Errorf("recreate sandbox listing cache: database is unavailable")
	}
	if err := resetSandboxCacheSchema(ctx, index.db); err != nil {
		return err
	}
	if err := s.completeIndexRebuild(ctx); err != nil {
		return fmt.Errorf("reconcile recreated sandbox listing cache: %w", err)
	}
	return nil
}

func FromConfig(config *appconfig.Config) *Store {
	return &Store{config: config}
}

// Close releases database resources owned by compatibility stores. Stores
// created with NewWithDatabase leave the caller-owned shared database open.
func (s *Store) Close() error {
	if s.index == nil {
		return nil
	}
	return s.index.Close()
}

// Shutdown adapts Close to the samber/do Shutdowner interface.
func (s *Store) Shutdown() error {
	return s.Close()
}

// rebuildIndex repopulates the sandbox listing cache from the filesystem and, only if it
// runs to completion, stamps the schema version so the index is treated as
// current. An interrupted rebuild (crash or transient read/upsert error)
// leaves the version unstamped so the next startup retries it rather than
// serving a partially-populated index.
func (s *Store) rebuildIndex(ctx context.Context) {
	if err := s.completeIndexRebuild(ctx); err != nil {
		slog.Warn("sandbox listing cache rebuild incomplete; retrying on next startup", "error", err)
	}
}

func (s *Store) completeIndexRebuild(ctx context.Context) error {
	if err := s.runIndexRebuild(ctx); err != nil {
		return err
	}
	return s.index.markComplete(ctx)
}

func (s *Store) ensureIndexCurrent(ctx context.Context) error {
	if !s.indexDirty.Load() {
		return nil
	}
	s.indexRepairMu.Lock()
	defer s.indexRepairMu.Unlock()
	if !s.indexDirty.Load() {
		return nil
	}
	if err := s.completeIndexRebuild(ctx); err != nil {
		return fmt.Errorf("repair sandbox listing cache: %w", err)
	}
	s.indexDirty.Store(false)
	return nil
}

// runIndexRebuild does the actual repopulation. It returns a non-nil error when
// the rebuild did not fully finish (context cancelled, root unreadable, an index
// upsert failed, or reconcile failed), which the caller uses to decide whether
// the index may be marked complete.
func (s *Store) runIndexRebuild(ctx context.Context) error {
	if s.index == nil {
		return nil
	}

	entries, err := os.ReadDir(s.config.SandboxRoot)
	if err != nil {
		return fmt.Errorf("read sandbox root: %w", err)
	}
	validIDs := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || entry.Name() == ".lifecycle" {
			continue
		}
		sandbox, loadErr := s.loadSandbox(entry.Name())
		if loadErr != nil {
			// Not a loadable sandbox (corrupt/foreign dir): skip, not a failure.
			continue
		}
		if upsertErr := s.index.Reconcile(ctx, sandbox); upsertErr != nil {
			return fmt.Errorf("upsert %s: %w", sandbox.Summary.ID, upsertErr)
		}
		validIDs[sandbox.Summary.ID] = struct{}{}
	}

	// Rows are retained only for metadata that was successfully loaded. A
	// directory with missing or malformed metadata is not a listable sandbox.
	rows, err := s.index.db.QueryContext(ctx, `SELECT id FROM sandboxes`)
	if err != nil {
		return sandboxCacheError("query rows during reconcile", err)
	}
	var orphans []string
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return sandboxCacheError("scan row during reconcile", errors.Join(scanErr, closeSandboxCacheRows(rows)))
		}
		if _, ok := validIDs[id]; !ok {
			orphans = append(orphans, id)
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return sandboxCacheError("iterate rows during reconcile", errors.Join(rowsErr, closeSandboxCacheRows(rows)))
	}
	if err := closeSandboxCacheRows(rows); err != nil {
		return sandboxCacheError("close rows during reconcile", err)
	}
	for _, id := range orphans {
		if delErr := s.index.Delete(ctx, id); delErr != nil {
			return fmt.Errorf("prune sandbox listing cache row %s: %w", id, delErr)
		}
	}
	return nil
}

func cloneSandboxWorkspace(item *SandboxWorkspace) *SandboxWorkspace {
	if item == nil {
		return nil
	}
	copy := *item
	return &copy
}

type CreateSandboxOptions struct {
	JupyterEnabled   bool
	JupyterGuestPort int
	JupyterExpose    bool
	VolumeMounts     []domain.SandboxVolumeMount
}

func (s *Store) CreateSandbox(ctx context.Context, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource string, workspace *SandboxWorkspace, envItems []SandboxEnvVar, tags []SandboxTag) (*Sandbox, error) {
	return s.CreateSandboxWithOptions(ctx, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource, workspace, envItems, tags, CreateSandboxOptions{})
}

func (s *Store) CreateSandboxWithOptions(ctx context.Context, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource string, workspace *SandboxWorkspace, envItems []SandboxEnvVar, tags []SandboxTag, options CreateSandboxOptions) (*Sandbox, error) {
	return s.createSandboxWithCacheDependencyLock(ctx, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource, workspace, envItems, tags, options)
}

func (s *Store) createSandboxWithCacheDependencyLock(ctx context.Context, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource string, workspace *SandboxWorkspace, envItems []SandboxEnvVar, tags []SandboxTag, options CreateSandboxOptions) (*Sandbox, error) {
	s.cacheDependencyMu.RLock()
	locker := s.cacheDependencyLocker
	s.cacheDependencyMu.RUnlock()
	if locker == nil {
		return s.createSandboxWithOptions(title, baseWorkspace, driver, guestImage, workspaceID, triggerSource, workspace, envItems, tags, options)
	}
	var sandbox *Sandbox
	err := locker.WithLockContext(ctx, func() error {
		var err error
		sandbox, err = s.createSandboxWithOptions(title, baseWorkspace, driver, guestImage, workspaceID, triggerSource, workspace, envItems, tags, options)
		return err
	})
	return sandbox, err
}

func (s *Store) createSandboxWithOptions(title, baseWorkspace, driver, guestImage, workspaceID, triggerSource string, workspace *SandboxWorkspace, envItems []SandboxEnvVar, tags []SandboxTag, options CreateSandboxOptions) (*Sandbox, error) {
	now := time.Now().UTC()
	workspaceID = strings.TrimSpace(workspaceID)
	id := identity.NewRandomID(identity.ResourceSandbox)
	shortID := identity.ShortID(id)
	sandboxDir := s.sandboxDir(id)
	workspaceDir := filepath.Join(sandboxDir, "workspace")
	proxyPath := strings.TrimRight(s.config.JupyterProxyBasePath, "/") + "/" + id + "/lab"
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(driver, s.config.RuntimeDriver)
	if err != nil {
		return nil, err
	}
	guestImage = driverpkg.ResolveSandboxGuestImage(guestImage, "", driverpkg.DefaultGuestImageForDriver(s.config, driver))
	var workspaceProvisioning *domain.SandboxWorkspaceProvisioning
	if workspace != nil || workspaceID != "" {
		workspaceProvisioning = &domain.SandboxWorkspaceProvisioning{
			Version:   domain.SandboxWorkspaceProvisioningVersion,
			Status:    domain.SandboxWorkspaceProvisioningStatusPending,
			UpdatedAt: now,
		}
	}

	for _, dir := range []string{
		sandboxDir,
		filepath.Join(sandboxDir, "context"),
		filepath.Join(sandboxDir, "home"),
		filepath.Join(sandboxDir, "runtime"),
		filepath.Join(sandboxDir, "workspace"),
		filepath.Join(sandboxDir, "state"),
		filepath.Join(sandboxDir, "logs"),
		filepath.Join(sandboxDir, "vm"),
		filepath.Join(sandboxDir, "proxy"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create sandbox dir %s: %w", dir, err)
		}
	}

	session := &Sandbox{
		Summary: SandboxSummary{
			ID:            id,
			ShortID:       shortID,
			Title:         strings.TrimSpace(title),
			TriggerSource: domain.NormalizeSandboxTriggerSource(triggerSource, tags),
			Driver:        driver,
			VMStatus:      VMStatusPending,
			GuestImage:    guestImage,
			RuntimeRef:    "agent-compose-" + shortID,
			WorkspacePath: workspaceDir,
			ProxyPath:     proxyPath,
			CreatedAt:     now,
			UpdatedAt:     now,
			Tags:          append([]SandboxTag(nil), tags...),
		},
		BaseWorkspace:         strings.TrimSpace(baseWorkspace),
		WorkspaceID:           workspaceID,
		Workspace:             cloneSandboxWorkspace(workspace),
		WorkspaceProvisioning: workspaceProvisioning,
		EnvItems:              append([]SandboxEnvVar(nil), envItems...),
		VolumeMounts:          domain.NormalizeSandboxVolumeMounts(options.VolumeMounts),
	}

	if session.Summary.Title == "" {
		session.Summary.Title = "agent-compose Sandbox " + now.Format("2006-01-02 15:04")
	}

	vmState := VMState{
		Driver:      session.Summary.Driver,
		Mode:        session.Summary.Driver,
		BoxName:     session.Summary.RuntimeRef,
		Image:       guestImage,
		RuntimeHome: driverpkg.RuntimeHomeForDriver(s.config, driver),
	}
	if driver == driverpkg.RuntimeDriverBoxlite {
		vmState.Registry = s.config.ImageRegistry
	}
	if err := s.saveVMState(session.Summary.ID, vmState); err != nil {
		return nil, err
	}
	proxyState := ProxyState{
		ProxyPath: session.Summary.ProxyPath,
		GuestHost: "127.0.0.1",
		Enabled:   options.JupyterEnabled,
		Exposed:   options.JupyterExpose,
	}
	if proxyState.Enabled {
		guestPort := options.JupyterGuestPort
		if guestPort == 0 {
			guestPort = s.config.JupyterGuestPort
		}
		if driver != driverpkg.RuntimeDriverDocker {
			hostPort, err := s.allocateHostPort()
			if err != nil {
				return nil, err
			}
			proxyState.HostPort = hostPort
		}
		proxyState.GuestPort = guestPort
		proxyState.JupyterURL = session.Summary.ProxyPath
		proxyState.Token = uuid.NewString()
	}
	if err := s.SaveProxyState(session.Summary.ID, proxyState); err != nil {
		return nil, err
	}
	if err := s.saveSandbox(session); err != nil {
		return nil, err
	}
	if err := s.saveCells(id, nil); err != nil {
		return nil, err
	}
	if err := s.saveEvents(id, nil); err != nil {
		return nil, err
	}
	if err := sessions.WriteOwnershipRecord(s.config.SandboxRoot, sessions.OwnershipRecord{
		Version:        sessions.OwnershipRecordVersion,
		SandboxID:      session.Summary.ID,
		Driver:         session.Summary.Driver,
		RuntimeID:      session.Summary.RuntimeRef,
		SandboxPath:    sandboxDir,
		LifecycleState: "active",
		OwnedResources: []sessions.OwnedResource{
			{Kind: "runtime", Identity: session.Summary.RuntimeRef},
			{Kind: "sandbox-directory", Path: sandboxDir},
		},
		CacheDependencies: []sessions.CacheDependency{{Domain: "runtime-image", Identity: guestImage}},
	}); err != nil {
		return nil, fmt.Errorf("write sandbox ownership record: %w", err)
	}

	s.recordIndex(session)
	return session, nil
}

// recordIndex mirrors a committed sandbox summary into the queryable index.
// Request cancellation cannot undo committed metadata, so the cache write uses
// its own bounded context. A failure marks the index dirty for repair before
// the next list query. Callers updating an existing sandbox hold its sandbox
// lock through this call; creation uses a new ID that is not published until
// recordIndex returns. This keeps metadata load and cache upsert ordered with
// RemoveSandbox without holding the global cache repair lock during disk I/O.
func (s *Store) recordIndex(session *Sandbox) {
	if s.index == nil || session == nil {
		return
	}
	indexed, err := s.loadSandbox(session.Summary.ID)
	if err != nil {
		s.indexDirty.Store(true)
		slog.Warn("load committed sandbox for index failed", "sandbox_id", session.Summary.ID, "error", err)
		return
	}
	s.indexRepairMu.Lock()
	defer s.indexRepairMu.Unlock()
	indexCtx, cancel := context.WithTimeout(context.Background(), sandboxCacheWriteTimeout)
	defer cancel()
	if err := s.index.Upsert(indexCtx, indexed); err != nil {
		s.indexDirty.Store(true)
		slog.Warn("sandbox listing cache upsert failed", "sandbox_id", session.Summary.ID, "error", err)
	}
}

func (s *Store) deleteIndexRow(id string) error {
	if s.index == nil {
		return nil
	}
	s.indexRepairMu.Lock()
	defer s.indexRepairMu.Unlock()
	indexCtx, cancel := context.WithTimeout(context.Background(), sandboxCacheWriteTimeout)
	defer cancel()
	if err := s.index.Delete(indexCtx, id); err != nil {
		s.indexDirty.Store(true)
		return fmt.Errorf("delete sandbox listing cache row %s: %w", id, err)
	}
	return nil
}

func (s *Store) SetCacheDependencyLocker(locker CacheDependencyLocker) {
	s.cacheDependencyMu.Lock()
	defer s.cacheDependencyMu.Unlock()
	s.cacheDependencyLocker = locker
}

func (s *Store) GetSandbox(_ context.Context, id string) (*Sandbox, error) {
	session, err := s.loadSandbox(strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	s.hydrateSandboxGuestImage(session)
	return session, nil
}

// ListSandboxes answers from the sandbox listing cache: filtering, sorting, and
// pagination run as an indexed SQL query over all sandboxes, then only the
// resulting page is loaded from disk for full fidelity (guest image, counts,
// tags, reclamation state). Keyset pagination loads at most a page; legacy
// offset pagination also validates the rows before the requested offset so
// stale index entries cannot shift the observable page.
func (s *Store) ListSandboxes(ctx context.Context, options SandboxListOptions) (SandboxListResult, error) {
	if s.index == nil {
		return s.listSandboxesFromFilesystem(ctx, options)
	}
	if err := s.ensureIndexCurrent(ctx); err != nil {
		return SandboxListResult{}, err
	}
	offset, limit := domain.NormalizeSandboxListBounds(options.Offset, options.Limit)
	queryOffset := 0
	skipped := 0
	var page []*Sandbox
	total := 0
	for len(page) < limit {
		query := options
		query.Offset = queryOffset
		query.Limit = listRowsNeeded(offset-skipped, limit-len(page))
		indexed, indexedTotal, err := s.index.list(ctx, query, s.sandboxDir)
		if err != nil {
			return SandboxListResult{}, err
		}
		// TotalCount reflects the reconciled cache view. Supported writes update
		// it synchronously; out-of-band filesystem removals are deducted when a
		// page encounters and prunes the stale row, or on the next startup.
		total = indexedTotal
		if len(indexed) == 0 {
			break
		}
		ghosts := 0
		for _, item := range indexed {
			full, loadErr := s.loadSandbox(item.Summary.ID)
			if loadErr != nil {
				if err := s.deleteIndexRow(item.Summary.ID); err != nil {
					return SandboxListResult{}, fmt.Errorf("prune unreadable sandbox listing cache row %s: %w", item.Summary.ID, err)
				}
				ghosts++
				continue
			}
			s.hydrateSandboxGuestImage(full)
			queryOffset++
			if skipped < offset {
				skipped++
				continue
			}
			page = append(page, full)
		}
		total -= ghosts
	}
	nextOffset := total
	if offset < total {
		nextOffset = offset + len(page)
	}
	return SandboxListResult{
		Sandboxes:  page,
		TotalCount: total,
		HasMore:    nextOffset < total,
		NextOffset: nextOffset,
	}, nil
}

func listRowsNeeded(skip, page int) int {
	maxInt := int(^uint(0) >> 1)
	if skip > maxInt-page {
		return maxInt
	}
	return skip + page
}

func (s *Store) UpdateSandbox(_ context.Context, session *Sandbox) error {
	s.hydrateSandboxGuestImage(session)
	session.Summary.UpdatedAt = time.Now().UTC()
	unlock := s.lockSandbox(session.Summary.ID)
	defer unlock()
	if err := s.saveSandboxPreservingCounts(session); err != nil {
		return err
	}
	s.recordIndex(session)
	return nil
}

func (s *Store) RemoveSandbox(_ context.Context, id string) error {
	id = strings.TrimSpace(id)
	if err := validateSandboxIDForRemove(id); err != nil {
		return err
	}
	path := s.sandboxDir(id)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("stat sandbox dir %s: %w", id, err)
	}

	unlock := s.lockSandbox(id)
	defer unlock()

	if err := driverpkg.CleanupBoxliteVolumeBridgeMounts(path); err != nil {
		return fmt.Errorf("cleanup session mounts %s: %w", id, err)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove sandbox dir %s: %w", id, err)
	}
	if err := s.deleteIndexRow(id); err != nil {
		slog.Warn("failed to delete sandbox listing cache row after removing authoritative metadata", "sandbox_id", id, "error", err)
	}
	s.sandboxLocks.Delete(sandboxLockKey(id))
	return nil
}

func (s *Store) backfillOwnershipRecords() error {
	entries, err := os.ReadDir(s.config.SandboxRoot)
	if err != nil {
		return fmt.Errorf("read sandbox root for lifecycle backfill: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".lifecycle" {
			continue
		}
		sandbox, loadErr := s.loadSandbox(entry.Name())
		if loadErr != nil {
			continue
		}
		if _, recordErr := sessions.ReadOwnershipRecord(s.config.SandboxRoot, sandbox.Summary.ID); recordErr == nil {
			continue
		} else if !os.IsNotExist(recordErr) {
			// A corrupt or unsupported record is evidence we cannot safely replace.
			continue
		}
		record := sessions.OwnershipRecord{
			Version: sessions.OwnershipRecordVersion, SandboxID: sandbox.Summary.ID,
			Driver: sandbox.Summary.Driver, RuntimeID: sandbox.Summary.RuntimeRef,
			SandboxPath: s.sandboxDir(sandbox.Summary.ID), LifecycleState: "active",
			OwnedResources:    []sessions.OwnedResource{{Kind: "runtime", Identity: sandbox.Summary.RuntimeRef}, {Kind: "sandbox-directory", Path: s.sandboxDir(sandbox.Summary.ID)}},
			CacheDependencies: []sessions.CacheDependency{{Domain: "runtime-image", Identity: sandbox.Summary.GuestImage}},
		}
		if writeErr := sessions.WriteOwnershipRecord(s.config.SandboxRoot, record); writeErr != nil {
			return fmt.Errorf("backfill sandbox ownership %s: %w", sandbox.Summary.ID, writeErr)
		}
	}
	return nil
}

func (s *Store) AddCell(_ context.Context, session *Sandbox, cell NotebookCell) error {
	cells, err := s.loadCells(session.Summary.ID)
	if err != nil {
		return err
	}
	updated := false
	if strings.TrimSpace(cell.ID) != "" {
		for index := range cells {
			if cells[index].ID != cell.ID {
				continue
			}
			cells[index] = cell
			updated = true
			break
		}
	}
	if !updated {
		cells = append(cells, cell)
	}
	if err := s.saveCells(session.Summary.ID, cells); err != nil {
		return err
	}
	timelineCells, err := s.loadCells(session.Summary.ID)
	if err != nil {
		return err
	}
	session.Summary.CellCount = len(timelineCells)
	return s.UpdateSandbox(context.Background(), session)
}

func (s *Store) ListCells(_ context.Context, id string) ([]NotebookCell, error) {
	return s.loadCells(id)
}

func (s *Store) AddAgentRun(_ context.Context, sessionID string, run AgentRun) error {
	cell := NotebookCell{
		ID:            run.ID,
		Type:          CellTypeAgent,
		Source:        run.Message,
		Output:        run.Output,
		ExitCode:      run.ExitCode,
		Success:       run.Success,
		Running:       run.Running,
		CreatedAt:     run.CreatedAt,
		Agent:         run.Agent,
		AgentThreadID: run.AgentThreadID,
		StopReason:    run.StopReason,
	}
	session, err := s.loadSandbox(sessionID)
	if err != nil {
		return err
	}
	if err := s.AddCell(context.Background(), session, cell); err != nil {
		return err
	}
	session, err = s.loadSandbox(sessionID)
	if err == nil {
		timelineCells, loadErr := s.loadCells(sessionID)
		if loadErr == nil {
			session.Summary.CellCount = len(timelineCells)
		}
		_ = s.UpdateSandbox(context.Background(), session)
	}
	return nil
}

func (s *Store) AddEvent(_ context.Context, sessionID string, event SandboxEvent) error {
	unlock := s.lockSandbox(sessionID)
	defer unlock()

	jsonlExisted, err := s.eventsJSONLExists(sessionID)
	if err != nil {
		return err
	}
	legacyCount := 0
	if !jsonlExisted {
		events, err := s.loadLegacyEvents(sessionID)
		if err != nil {
			return err
		}
		legacyCount = len(events)
	}

	if err := s.appendEvent(sessionID, event); err != nil {
		return err
	}

	session, err := s.loadSandbox(sessionID)
	if err != nil {
		slog.Warn("load sandbox summary after committed event append failed", "sandbox_id", sessionID, "error", err)
		return nil
	}
	nextCount := session.Summary.EventCount + 1
	if !jsonlExisted && legacyCount >= session.Summary.EventCount {
		nextCount = legacyCount + 1
	}
	session.Summary.EventCount = nextCount
	if err := s.persistEventSandboxSummary(session); err != nil {
		slog.Warn("sandbox summary update after committed event append failed", "sandbox_id", sessionID, "error", err)
	}
	return nil
}

func (s *Store) persistEventSandboxSummary(session *Sandbox) error {
	s.hydrateSandboxGuestImage(session)
	session.Summary.UpdatedAt = time.Now().UTC()
	if err := s.saveSandboxPreservingCounts(session); err != nil {
		return fmt.Errorf("save sandbox summary after event append: %w", err)
	}
	s.recordIndex(session)
	return nil
}

func (s *Store) ListEvents(_ context.Context, id string) ([]SandboxEvent, error) {
	unlock := s.lockSandbox(id)
	defer unlock()
	return s.loadEvents(id)
}

func (s *Store) sandboxDir(id string) string {
	return filepath.Join(s.config.SandboxRoot, sandboxDirName(id))
}

func (s *Store) SandboxDir(id string) string {
	return s.sandboxDir(id)
}

func validateSandboxIDForRemove(id string) error {
	if id == "" {
		return fmt.Errorf("sandbox id is required")
	}
	if id == "." || id == ".." || filepath.Base(id) != id {
		return fmt.Errorf("invalid sandbox id %q", id)
	}
	return nil
}

func sandboxDirName(id string) string {
	if hash, err := identity.Hash(id); err == nil {
		return hash
	}
	return id
}

func (s *Store) lockSandbox(id string) func() {
	value, _ := s.sandboxLocks.LoadOrStore(sandboxLockKey(id), &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func sandboxLockKey(id string) string {
	return sandboxDirName(id)
}

func (s *Store) hydrateSandboxGuestImage(session *Sandbox) {
	if session == nil {
		return
	}
	if strings.TrimSpace(session.Summary.GuestImage) != "" {
		return
	}
	if vmState, err := s.GetVMState(session.Summary.ID); err == nil {
		session.Summary.GuestImage = driverpkg.ResolveSandboxGuestImage("", vmState.Image, driverpkg.DefaultGuestImageForDriver(s.config, session.Summary.Driver))
		return
	}
	session.Summary.GuestImage = driverpkg.ResolveSandboxGuestImage("", "", driverpkg.DefaultGuestImageForDriver(s.config, session.Summary.Driver))
}

func (s *Store) vmStatePath(id string) string {
	return filepath.Join(s.sandboxDir(id), "vm", "runtime.json")
}

func (s *Store) VMStatePath(id string) string {
	return s.vmStatePath(id)
}

func (s *Store) legacyVMStatePath(id string) string {
	return filepath.Join(s.sandboxDir(id), "vm", "boxlite.json")
}

func (s *Store) LegacyVMStatePath(id string) string {
	return s.legacyVMStatePath(id)
}

func (s *Store) proxyStatePath(id string) string {
	return filepath.Join(s.sandboxDir(id), "proxy", "jupyter.json")
}

func (s *Store) ProxyStatePath(id string) string {
	return s.proxyStatePath(id)
}

func (s *Store) loadSandbox(id string) (*Sandbox, error) {
	path := filepath.Join(s.sandboxDir(id), "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session metadata %s: %w", id, err)
	}
	var session Sandbox
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("decode session metadata %s: %w", id, err)
	}
	// WorkspacePath is derived from the active sandbox root. Persisted absolute
	// paths may refer to the filesystem namespace of an older daemon process.
	session.Summary.WorkspacePath = filepath.Join(s.sandboxDir(id), "workspace")
	session.Summary.TriggerSource = domain.NormalizeSandboxTriggerSource(session.Summary.TriggerSource, session.Summary.Tags)
	if strings.TrimSpace(session.Summary.ShortID) == "" {
		session.Summary.ShortID = identity.ShortID(session.Summary.ID)
	}
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(session.Summary.Driver, s.config.RuntimeDriver)
	if err != nil {
		return nil, fmt.Errorf("session metadata %s has invalid driver: %w", id, err)
	}
	session.Summary.Driver = driver
	return &session, nil
}

func (s *Store) LoadSandbox(id string) (*Sandbox, error) {
	return s.loadSandbox(id)
}

func (s *Store) saveSandbox(session *Sandbox) error {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session metadata: %w", err)
	}
	if err := writeFileAtomically(
		filepath.Join(s.sandboxDir(session.Summary.ID), "metadata.json"),
		append(data, '\n'),
		0o644,
	); err != nil {
		return fmt.Errorf("write session metadata: %w", err)
	}
	return nil
}

func writeFileAtomically(path string, data []byte, perm fs.FileMode) (returnErr error) {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if err := os.Remove(temporaryPath); err != nil && !os.IsNotExist(err) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary file %s: %w", temporaryPath, err))
		}
	}()

	if err := temporary.Chmod(perm); err != nil {
		return fmt.Errorf("set temporary file mode for %s: %w", path, err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary file for %s: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary file for %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %s with temporary file: %w", path, err)
	}
	return nil
}

func (s *Store) saveSandboxPreservingCounts(session *Sandbox) error {
	existing, err := s.loadSandboxCounts(session.Summary.ID)
	if err != nil {
		return err
	}
	if existing.CellCount > session.Summary.CellCount {
		session.Summary.CellCount = existing.CellCount
	}
	if existing.EventCount > session.Summary.EventCount {
		session.Summary.EventCount = existing.EventCount
	}
	return s.saveSandbox(session)
}

func (s *Store) loadSandboxCounts(id string) (SandboxSummary, error) {
	path := filepath.Join(s.sandboxDir(id), "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SandboxSummary{}, nil
		}
		return SandboxSummary{}, fmt.Errorf("read session metadata %s: %w", id, err)
	}
	var session Sandbox
	if err := json.Unmarshal(data, &session); err != nil {
		return SandboxSummary{}, fmt.Errorf("decode session metadata %s: %w", id, err)
	}
	return session.Summary, nil
}

func (s *Store) saveEventCount(id string, eventCount int) error {
	session, err := s.loadSandbox(id)
	if err != nil {
		return err
	}
	session.Summary.EventCount = eventCount
	s.hydrateSandboxGuestImage(session)
	session.Summary.UpdatedAt = time.Now().UTC()
	if err := s.saveSandbox(session); err != nil {
		return err
	}
	s.recordIndex(session)
	return nil
}

func (s *Store) SaveSandbox(session *Sandbox) error {
	unlock := s.lockSandbox(session.Summary.ID)
	defer unlock()
	if err := s.saveSandboxPreservingCounts(session); err != nil {
		return err
	}
	s.recordIndex(session)
	return nil
}

func (s *Store) loadCells(id string) ([]NotebookCell, error) {
	path := filepath.Join(s.sandboxDir(id), "state", "cells.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cells: %w", err)
	}
	var cells []NotebookCell
	if len(data) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(data, &cells); err != nil {
		return nil, fmt.Errorf("decode cells: %w", err)
	}
	migrated, changed, err := s.mergeLegacyAgentRuns(id, cells)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := s.saveCells(id, migrated); err != nil {
			return nil, err
		}
	}
	for index := range migrated {
		migrated[index] = hydrateRunningCellArtifacts(filepath.Join(s.sandboxDir(id), "state", "cells", migrated[index].ID), migrated[index])
	}
	return migrated, nil
}

func hydrateRunningCellArtifacts(cellDir string, cell NotebookCell) NotebookCell {
	if !cell.Running || strings.TrimSpace(cell.ID) == "" {
		return cell
	}
	loadArtifact := func(name, current string) string {
		data, err := os.ReadFile(filepath.Join(cellDir, name))
		if err != nil {
			return current
		}
		value := string(data)
		if len(value) <= len(current) {
			return current
		}
		return value
	}
	cell.Stdout = loadArtifact("stdout.txt", cell.Stdout)
	cell.Stderr = loadArtifact("stderr.txt", cell.Stderr)
	cell.Output = loadArtifact("output.txt", firstNonEmpty(cell.Output, cell.Stdout+cell.Stderr))
	return cell
}

func (s *Store) saveCells(id string, cells []NotebookCell) error {
	data, err := json.MarshalIndent(cells, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cells: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.sandboxDir(id), "state", "cells.json"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write cells: %w", err)
	}
	return nil
}

func (s *Store) SaveCells(id string, cells []NotebookCell) error {
	return s.saveCells(id, cells)
}

func (s *Store) loadAgentRuns(id string) ([]AgentRun, error) {
	path := filepath.Join(s.sandboxDir(id), "state", "agent_runs.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agent runs: %w", err)
	}
	var runs []AgentRun
	if len(data) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(data, &runs); err != nil {
		return nil, fmt.Errorf("decode agent runs: %w", err)
	}
	return runs, nil
}

func (s *Store) mergeLegacyAgentRuns(id string, cells []NotebookCell) ([]NotebookCell, bool, error) {
	runs, err := s.loadAgentRuns(id)
	if err != nil {
		return nil, false, err
	}
	if len(runs) == 0 {
		sort.SliceStable(cells, func(i, j int) bool {
			if cells[i].CreatedAt.Equal(cells[j].CreatedAt) {
				return cells[i].ID < cells[j].ID
			}
			return cells[i].CreatedAt.Before(cells[j].CreatedAt)
		})
		return cells, false, nil
	}
	seen := make(map[string]struct{}, len(cells))
	merged := append([]NotebookCell(nil), cells...)
	for _, cell := range merged {
		seen[cell.ID] = struct{}{}
	}
	changed := false
	for _, run := range runs {
		if _, ok := seen[run.ID]; ok {
			continue
		}
		merged = append(merged, NotebookCell{
			ID:            run.ID,
			Type:          CellTypeAgent,
			Source:        run.Message,
			Output:        run.Output,
			ExitCode:      run.ExitCode,
			Success:       run.Success,
			CreatedAt:     run.CreatedAt,
			Agent:         run.Agent,
			AgentThreadID: run.AgentThreadID,
			StopReason:    run.StopReason,
		})
		changed = true
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].CreatedAt.Equal(merged[j].CreatedAt) {
			return merged[i].ID < merged[j].ID
		}
		return merged[i].CreatedAt.Before(merged[j].CreatedAt)
	})
	return merged, changed, nil
}

func (s *Store) loadEvents(id string) ([]SandboxEvent, error) {
	events, err := s.loadLegacyEvents(id)
	if err != nil {
		return nil, err
	}
	jsonlEvents, err := s.loadJSONLEvents(id)
	if err != nil {
		return nil, err
	}
	return append(events, jsonlEvents...), nil
}

func (s *Store) eventsJSONPath(id string) string {
	return filepath.Join(s.sandboxDir(id), "state", "events.json")
}

func (s *Store) eventsJSONLPath(id string) string {
	return filepath.Join(s.sandboxDir(id), "state", "events.jsonl")
}

func (s *Store) eventsJSONLExists(id string) (bool, error) {
	_, err := os.Stat(s.eventsJSONLPath(id))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat events jsonl: %w", err)
}

func (s *Store) loadLegacyEvents(id string) ([]SandboxEvent, error) {
	path := s.eventsJSONPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read events: %w", err)
	}
	var events []SandboxEvent
	if len(data) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return events, nil
}

func (s *Store) loadJSONLEvents(id string) ([]SandboxEvent, error) {
	path := s.eventsJSONLPath(id)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read events jsonl: %w", err)
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReader(file)
	var events []SandboxEvent
	lineNumber := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNumber++
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				var event SandboxEvent
				if err := json.Unmarshal(line, &event); err != nil {
					return nil, fmt.Errorf("decode events %s line %d: %w", path, lineNumber, err)
				}
				events = append(events, event)
			}
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		return nil, fmt.Errorf("read events %s line %d: %w", path, lineNumber+1, readErr)
	}
	return events, nil
}

func (s *Store) appendEvent(id string, event SandboxEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	data = append(data, '\n')

	file, err := os.OpenFile(s.eventsJSONLPath(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open events jsonl: %w", err)
	}
	if n, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("append events jsonl: %w", err)
	} else if n != len(data) {
		_ = file.Close()
		return fmt.Errorf("append events jsonl: %w", io.ErrShortWrite)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close events jsonl: %w", err)
	}
	return nil
}

func (s *Store) saveEvents(id string, events []SandboxEvent) error {
	file, err := os.OpenFile(s.eventsJSONLPath(id), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("write events jsonl: %w", err)
	}
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("encode event: %w", err)
		}
		data = append(data, '\n')
		if n, err := file.Write(data); err != nil {
			_ = file.Close()
			return fmt.Errorf("write events jsonl: %w", err)
		} else if n != len(data) {
			_ = file.Close()
			return fmt.Errorf("write events jsonl: %w", io.ErrShortWrite)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close events jsonl: %w", err)
	}
	if err := os.Remove(s.eventsJSONPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove legacy events: %w", err)
	}
	return nil
}

func (s *Store) SaveEvents(id string, events []SandboxEvent) error {
	unlock := s.lockSandbox(id)
	defer unlock()
	if err := s.saveEvents(id, events); err != nil {
		return err
	}
	return s.saveEventCount(id, len(events))
}

func (s *Store) GetVMState(id string) (VMState, error) {
	var state VMState
	if err := s.readJSONFile(s.vmStatePath(id), &state); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return VMState{}, err
		}
		if legacyErr := s.readJSONFile(s.legacyVMStatePath(id), &state); legacyErr != nil {
			return VMState{}, legacyErr
		}
	}
	state.Driver = driverpkg.ResolveRuntimeDriver(firstNonEmpty(state.Driver, state.Mode))
	if err := driverpkg.ValidateRuntimeDriver(state.Driver); err != nil {
		return VMState{}, err
	}
	if strings.TrimSpace(state.RuntimeHome) == "" {
		state.RuntimeHome = driverpkg.RuntimeHomeForDriver(s.config, state.Driver)
	}
	return state, nil
}

func (s *Store) SaveVMState(id string, state VMState) error {
	return s.saveVMState(id, state)
}

func (s *Store) saveVMState(id string, state VMState) error {
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(state.Driver, s.config.RuntimeDriver)
	if err != nil {
		return err
	}
	state.Driver = driver
	state.Mode = driver
	if strings.TrimSpace(state.RuntimeHome) == "" {
		state.RuntimeHome = driverpkg.RuntimeHomeForDriver(s.config, driver)
	}
	return s.writeJSONFile(s.vmStatePath(id), state)
}

func (s *Store) GetProxyState(id string) (ProxyState, error) {
	var state ProxyState
	if err := s.readJSONFile(s.proxyStatePath(id), &state); err != nil {
		return ProxyState{}, err
	}
	return state, nil
}

func (s *Store) SaveProxyState(id string, state ProxyState) error {
	return s.writeJSONFile(s.proxyStatePath(id), state)
}

func (s *Store) AllocateHostPortForJupyter() (int, error) {
	return s.allocateHostPort()
}

func (s *Store) allocateHostPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate host port: %w", err)
	}
	defer func() { _ = listener.Close() }()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("allocate host port: unexpected addr %T", listener.Addr())
	}
	return addr.Port, nil
}

func (s *Store) readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (s *Store) writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
