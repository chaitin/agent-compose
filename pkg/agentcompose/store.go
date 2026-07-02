package agentcompose

import (
	appconfig "agent-compose/internal/config"
	driverpkg "agent-compose/internal/driver"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/samber/do/v2"
)

type Store struct {
	config *appconfig.Config
}

func NewStore(di do.Injector) (*Store, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create session root: %w", err)
	}
	return &Store{config: config}, nil
}

func cloneSessionWorkspace(item *SessionWorkspace) *SessionWorkspace {
	if item == nil {
		return nil
	}
	copy := *item
	return &copy
}

func (s *Store) CreateSession(_ context.Context, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource string, workspace *SessionWorkspace, envItems []SessionEnvVar, tags []SessionTag) (*Session, error) {
	now := time.Now().UTC()
	id := uuid.NewString()
	sessionDir := s.sessionDir(id)
	workspaceDir := filepath.Join(sessionDir, "workspace")
	proxyPath := strings.TrimRight(s.config.JupyterProxyBasePath, "/") + "/" + id + "/lab"
	driver, err := driverpkg.ResolveSessionRuntimeDriver(driver, s.config.RuntimeDriver)
	if err != nil {
		return nil, err
	}
	guestImage = driverpkg.ResolveSessionGuestImage(guestImage, "", driverpkg.DefaultGuestImageForDriver(s.config, driver))

	for _, dir := range []string{
		sessionDir,
		filepath.Join(sessionDir, "context"),
		filepath.Join(sessionDir, "home"),
		filepath.Join(sessionDir, "runtime"),
		filepath.Join(sessionDir, "workspace"),
		filepath.Join(sessionDir, "state"),
		filepath.Join(sessionDir, "logs"),
		filepath.Join(sessionDir, "vm"),
		filepath.Join(sessionDir, "proxy"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create session dir %s: %w", dir, err)
		}
	}

	session := &Session{
		Summary: SessionSummary{
			ID:            id,
			Title:         strings.TrimSpace(title),
			TriggerSource: normalizeSessionTriggerSource(triggerSource, tags),
			Driver:        driver,
			VMStatus:      VMStatusPending,
			GuestImage:    guestImage,
			RuntimeRef:    "agent-compose-" + id,
			WorkspacePath: workspaceDir,
			ProxyPath:     proxyPath,
			CreatedAt:     now,
			UpdatedAt:     now,
			Tags:          append([]SessionTag(nil), tags...),
		},
		BaseWorkspace: strings.TrimSpace(baseWorkspace),
		WorkspaceID:   strings.TrimSpace(workspaceID),
		Workspace:     cloneSessionWorkspace(workspace),
		EnvItems:      append([]SessionEnvVar(nil), envItems...),
	}

	if session.Summary.Title == "" {
		session.Summary.Title = "agent-compose Session " + now.Format("2006-01-02 15:04")
	}

	hostPort, err := s.allocateHostPort()
	if err != nil {
		return nil, err
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
		ProxyPath:  session.Summary.ProxyPath,
		GuestHost:  "127.0.0.1",
		HostPort:   hostPort,
		GuestPort:  s.config.JupyterGuestPort,
		JupyterURL: fmt.Sprintf("http://127.0.0.1:%d/lab", hostPort),
		Token:      uuid.NewString(),
	}
	if err := s.SaveProxyState(session.Summary.ID, proxyState); err != nil {
		return nil, err
	}
	if err := s.saveSession(session); err != nil {
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

func (s *Store) GetSession(_ context.Context, id string) (*Session, error) {
	session, err := s.loadSession(strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	s.hydrateSessionGuestImage(session)
	return session, nil
}

func (s *Store) ListSessions(_ context.Context, options SessionListOptions) (SessionListResult, error) {
	entries, err := os.ReadDir(s.config.SessionRoot)
	if err != nil {
		return SessionListResult{}, fmt.Errorf("read session root: %w", err)
	}
	var sessions []*Session
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		session, err := s.loadSession(entry.Name())
		if err != nil {
			continue
		}
		s.hydrateSessionGuestImage(session)
		if !sessionMatchesListOptions(session, options) {
			continue
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Summary.UpdatedAt.After(sessions[j].Summary.UpdatedAt)
	})
	totalCount := len(sessions)
	offset, limit := normalizeSessionListBounds(options.Offset, options.Limit)
	page := paginateSessions(sessions, offset, limit)
	result := SessionListResult{
		Sessions:   page,
		TotalCount: totalCount,
		HasMore:    offset+len(page) < totalCount,
		NextOffset: offset + len(page),
	}
	if result.NextOffset > totalCount {
		result.NextOffset = totalCount
	}
	return result, nil
}

func (s *Store) UpdateSession(_ context.Context, session *Session) error {
	s.hydrateSessionGuestImage(session)
	session.Summary.UpdatedAt = time.Now().UTC()
	return s.saveSession(session)
}

func (s *Store) AddCell(_ context.Context, session *Session, cell NotebookCell) error {
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
	return s.UpdateSession(context.Background(), session)
}

func (s *Store) ListCells(_ context.Context, id string) ([]NotebookCell, error) {
	return s.loadCells(id)
}

func (s *Store) AddAgentRun(_ context.Context, sessionID string, run AgentRun) error {
	cell := NotebookCell{
		ID:             run.ID,
		Type:           CellTypeAgent,
		Source:         run.Message,
		Output:         run.Output,
		ExitCode:       run.ExitCode,
		Success:        run.Success,
		Running:        run.Running,
		CreatedAt:      run.CreatedAt,
		Agent:          run.Agent,
		AgentSessionID: run.AgentSessionID,
		StopReason:     run.StopReason,
	}
	session, err := s.loadSession(sessionID)
	if err != nil {
		return err
	}
	if err := s.AddCell(context.Background(), session, cell); err != nil {
		return err
	}
	session, err = s.loadSession(sessionID)
	if err == nil {
		timelineCells, loadErr := s.loadCells(sessionID)
		if loadErr == nil {
			session.Summary.CellCount = len(timelineCells)
		}
		_ = s.UpdateSession(context.Background(), session)
	}
	return nil
}

func (s *Store) AddEvent(_ context.Context, sessionID string, event SessionEvent) error {
	events, err := s.loadEvents(sessionID)
	if err != nil {
		return err
	}
	events = append(events, event)
	if err := s.saveEvents(sessionID, events); err != nil {
		return err
	}
	session, err := s.loadSession(sessionID)
	if err == nil {
		session.Summary.EventCount = len(events)
		_ = s.UpdateSession(context.Background(), session)
	}
	return nil
}

func (s *Store) ListEvents(_ context.Context, id string) ([]SessionEvent, error) {
	return s.loadEvents(id)
}

func (s *Store) sessionDir(id string) string {
	return filepath.Join(s.config.SessionRoot, id)
}

func (s *Store) hydrateSessionGuestImage(session *Session) {
	if session == nil {
		return
	}
	if strings.TrimSpace(session.Summary.GuestImage) != "" {
		return
	}
	if vmState, err := s.GetVMState(session.Summary.ID); err == nil {
		session.Summary.GuestImage = driverpkg.ResolveSessionGuestImage("", vmState.Image, driverpkg.DefaultGuestImageForDriver(s.config, session.Summary.Driver))
		return
	}
	session.Summary.GuestImage = driverpkg.ResolveSessionGuestImage("", "", driverpkg.DefaultGuestImageForDriver(s.config, session.Summary.Driver))
}

func (s *Store) vmStatePath(id string) string {
	return filepath.Join(s.sessionDir(id), "vm", "runtime.json")
}

func (s *Store) legacyVMStatePath(id string) string {
	return filepath.Join(s.sessionDir(id), "vm", "boxlite.json")
}

func (s *Store) proxyStatePath(id string) string {
	return filepath.Join(s.sessionDir(id), "proxy", "jupyter.json")
}

func (s *Store) loadSession(id string) (*Session, error) {
	path := filepath.Join(s.sessionDir(id), "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session metadata %s: %w", id, err)
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("decode session metadata %s: %w", id, err)
	}
	session.Summary.TriggerSource = normalizeSessionTriggerSource(session.Summary.TriggerSource, session.Summary.Tags)
	driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, s.config.RuntimeDriver)
	if err != nil {
		return nil, fmt.Errorf("session metadata %s has invalid driver: %w", id, err)
	}
	session.Summary.Driver = driver
	return &session, nil
}

func (s *Store) saveSession(session *Session) error {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session metadata: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.sessionDir(session.Summary.ID), "metadata.json"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write session metadata: %w", err)
	}
	return nil
}

func (s *Store) loadCells(id string) ([]NotebookCell, error) {
	path := filepath.Join(s.sessionDir(id), "state", "cells.json")
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
		migrated[index] = hydrateRunningCellArtifacts(filepath.Join(s.sessionDir(id), "state", "cells", migrated[index].ID), migrated[index])
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
	if err := os.WriteFile(filepath.Join(s.sessionDir(id), "state", "cells.json"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write cells: %w", err)
	}
	return nil
}

func (s *Store) loadAgentRuns(id string) ([]AgentRun, error) {
	path := filepath.Join(s.sessionDir(id), "state", "agent_runs.json")
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
			ID:             run.ID,
			Type:           CellTypeAgent,
			Source:         run.Message,
			Output:         run.Output,
			ExitCode:       run.ExitCode,
			Success:        run.Success,
			CreatedAt:      run.CreatedAt,
			Agent:          run.Agent,
			AgentSessionID: run.AgentSessionID,
			StopReason:     run.StopReason,
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

func (s *Store) loadEvents(id string) ([]SessionEvent, error) {
	path := filepath.Join(s.sessionDir(id), "state", "events.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read events: %w", err)
	}
	var events []SessionEvent
	if len(data) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return events, nil
}

func (s *Store) saveEvents(id string, events []SessionEvent) error {
	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return fmt.Errorf("encode events: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.sessionDir(id), "state", "events.json"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write events: %w", err)
	}
	return nil
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
	driver, err := driverpkg.ResolveSessionRuntimeDriver(state.Driver, s.config.RuntimeDriver)
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
