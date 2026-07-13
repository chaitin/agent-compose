package sessionstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"

	"github.com/google/uuid"
	"github.com/samber/do/v2"
)

const (
	VMStatusPending = domain.VMStatusPending
	VMStatusRunning = domain.VMStatusRunning
	VMStatusStopped = domain.VMStatusStopped
	VMStatusFailed  = domain.VMStatusFailed

	CellTypeAgent = execution.CellTypeAgent
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
	config       *appconfig.Config
	sandboxLocks sync.Map
}

func NewStore(di do.Injector) (*Store, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	return NewWithConfig(config)
}

func NewWithConfig(config *appconfig.Config) (*Store, error) {
	if err := os.MkdirAll(config.SandboxRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create sandbox root: %w", err)
	}
	return &Store{config: config}, nil
}

func FromConfig(config *appconfig.Config) *Store {
	return &Store{config: config}
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

func (s *Store) CreateSandboxWithOptions(_ context.Context, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource string, workspace *SandboxWorkspace, envItems []SandboxEnvVar, tags []SandboxTag, options CreateSandboxOptions) (*Sandbox, error) {
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

	return session, nil
}

func (s *Store) GetSandbox(_ context.Context, id string) (*Sandbox, error) {
	session, err := s.loadSandbox(strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	s.hydrateSandboxGuestImage(session)
	return session, nil
}

func (s *Store) ListSandboxes(_ context.Context, options SandboxListOptions) (SandboxListResult, error) {
	entries, err := os.ReadDir(s.config.SandboxRoot)
	if err != nil {
		return SandboxListResult{}, fmt.Errorf("read sandbox root: %w", err)
	}
	var sessions []*Sandbox
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		session, err := s.loadSandbox(entry.Name())
		if err != nil {
			continue
		}
		s.hydrateSandboxGuestImage(session)
		if !domain.SandboxMatchesListOptions(session, options) {
			continue
		}
		if !options.BeforeUpdatedAt.IsZero() && (session.Summary.UpdatedAt.After(options.BeforeUpdatedAt) || (session.Summary.UpdatedAt.Equal(options.BeforeUpdatedAt) && session.Summary.ID >= options.BeforeID)) {
			continue
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Summary.UpdatedAt.Equal(sessions[j].Summary.UpdatedAt) {
			return sessions[i].Summary.ID > sessions[j].Summary.ID
		}
		return sessions[i].Summary.UpdatedAt.After(sessions[j].Summary.UpdatedAt)
	})
	totalCount := len(sessions)
	offset, limit := domain.NormalizeSandboxListBounds(options.Offset, options.Limit)
	page := domain.PaginateSandboxes(sessions, offset, limit)
	result := SandboxListResult{
		Sandboxes:  page,
		TotalCount: totalCount,
		HasMore:    offset+len(page) < totalCount,
		NextOffset: offset + len(page),
	}
	if result.NextOffset > totalCount {
		result.NextOffset = totalCount
	}
	return result, nil
}

func (s *Store) UpdateSandbox(_ context.Context, session *Sandbox) error {
	s.hydrateSandboxGuestImage(session)
	session.Summary.UpdatedAt = time.Now().UTC()
	unlock := s.lockSandbox(session.Summary.ID)
	defer unlock()
	return s.saveSandboxPreservingCounts(session)
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
	s.sandboxLocks.Delete(sandboxLockKey(id))
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
	if err == nil {
		nextCount := session.Summary.EventCount + 1
		if !jsonlExisted && legacyCount >= session.Summary.EventCount {
			nextCount = legacyCount + 1
		}
		session.Summary.EventCount = nextCount
		s.hydrateSandboxGuestImage(session)
		session.Summary.UpdatedAt = time.Now().UTC()
		_ = s.saveSandboxPreservingCounts(session)
	}
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
	if err := os.WriteFile(filepath.Join(s.sandboxDir(session.Summary.ID), "metadata.json"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write session metadata: %w", err)
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
	return s.saveSandbox(session)
}

func (s *Store) SaveSandbox(session *Sandbox) error {
	unlock := s.lockSandbox(session.Summary.ID)
	defer unlock()
	return s.saveSandboxPreservingCounts(session)
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
