package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

const agentSkillsUpdatePrefix = ".skills-update-"
const agentSkillsUpdateJournalName = ".update.json"
const agentSkillsUpdateOwnerName = ".agent-compose-skills-update"
const agentSkillsUpdateOwner = "agent-compose skills update v1\n"

type agentSkillsFileIdentity struct {
	Exists bool   `json:"exists"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
type agentSkillsUpdateOperation struct {
	Target string                  `json:"target"`
	Stage  string                  `json:"stage"`
	Old    agentSkillsFileIdentity `json:"old"`
	New    agentSkillsFileIdentity `json:"new"`
}
type agentSkillsUpdateJournal struct {
	Version          int                          `json:"version"`
	Committed        bool                         `json:"committed"`
	HadManifest      bool                         `json:"had_manifest"`
	PreviousManifest []byte                       `json:"previous_manifest,omitempty"`
	Operations       []agentSkillsUpdateOperation `json:"operations"`
}
type agentSkillsUpdate struct {
	root         string
	session      *domain.Sandbox
	nextManifest []byte
	journal      agentSkillsUpdateJournal
}

func lockAgentSkills(ctx context.Context, parent string) (func(), error) {
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(parent, ".skills.lock")
	if err := validateAgentSkillsControlFile(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}

func agentSkillsPathIdentity(path string) (agentSkillsFileIdentity, error) {
	info, err := os.Lstat(path)
	if isAgentSkillMissing(err) {
		return agentSkillsFileIdentity{}, nil
	}
	if err != nil {
		return agentSkillsFileIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return agentSkillsFileIdentity{}, fmt.Errorf("file identity unavailable for agent skills path %s", path)
	}
	return agentSkillsFileIdentity{Exists: true, Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}, nil
}

func (u *agentSkillsUpdate) target(target string) (string, error) {
	if target == "claude" {
		return filepath.Join(HostSandboxDir(u.session), "home", ".claude", "skills"), nil
	}
	name, ok := strings.CutPrefix(target, "skill:")
	if !ok {
		return "", fmt.Errorf("invalid skills update target %q", target)
	}
	if err := validateAgentSkillName(name); err != nil {
		return "", err
	}
	return filepath.Join(HostAgentSkillsDir(u.session), name), nil
}

func (u *agentSkillsUpdate) addOperation(target, stage string) error {
	path, err := u.target(target)
	if err != nil {
		return err
	}
	old, err := agentSkillsPathIdentity(path)
	if err != nil {
		return err
	}
	next, err := agentSkillsPathIdentity(filepath.Join(u.root, stage))
	if err != nil {
		return err
	}
	if !old.Exists && !next.Exists {
		return nil
	}
	u.journal.Operations = append(u.journal.Operations, agentSkillsUpdateOperation{Target: target, Stage: stage, Old: old, New: next})
	return nil
}

func (u *agentSkillsUpdate) save() error {
	data, err := json.Marshal(u.journal)
	if err != nil {
		return err
	}
	return writeAgentSkillsAtomicFile(filepath.Join(u.root, agentSkillsUpdateJournalName), data)
}

func (u *agentSkillsUpdate) rollback(operations agentSkillsFileOperations) error {
	var rollbackErr error
	for i := len(u.journal.Operations) - 1; i >= 0; i-- {
		rollbackErr = errors.Join(rollbackErr, u.rollbackOperation(u.journal.Operations[i], operations))
	}
	if rollbackErr != nil {
		return rollbackErr
	}
	path := filepath.Join(HostAgentSkillsDir(u.session), agentSkillsManifestFileName)
	if u.journal.HadManifest {
		return writeAgentSkillsAtomicFile(path, u.journal.PreviousManifest)
	}
	if err := os.Remove(path); err != nil && !isAgentSkillMissing(err) {
		return err
	}
	return nil
}

func (u *agentSkillsUpdate) rollbackOperation(change agentSkillsUpdateOperation, operations agentSkillsFileOperations) error {
	if filepath.Base(change.Stage) != change.Stage || change.Stage == "." || change.Stage == ".." || change.Stage == "" {
		return fmt.Errorf("invalid agent skills staging name")
	}
	target, err := u.target(change.Target)
	if err != nil {
		return err
	}
	staged := filepath.Join(u.root, change.Stage)
	targetID, err := agentSkillsPathIdentity(target)
	if err != nil {
		return err
	}
	stageID, err := agentSkillsPathIdentity(staged)
	if err != nil {
		return err
	}
	// The staged inode identifies whether an interrupted atomic rename happened;
	// no partially written progress counter is needed between filesystem calls.
	if change.New.Exists {
		if stageID == change.New || targetID == change.Old {
			return nil
		}
		if targetID != change.New {
			return fmt.Errorf("cannot safely roll back changed skills target %s", change.Target)
		}
		if change.Old.Exists {
			if stageID != change.Old {
				return fmt.Errorf("original skills backup is unavailable for %s", change.Target)
			}
			return operations.exchange(target, staged)
		}
		return os.Rename(target, staged)
	}
	if targetID == change.Old {
		return nil
	}
	if stageID != change.Old || targetID.Exists {
		return fmt.Errorf("cannot safely restore removed skills target %s", change.Target)
	}
	return os.Rename(staged, target)
}

func recoverAgentSkillsUpdates(session *domain.Sandbox, operations agentSkillsFileOperations) error {
	parent := filepath.Dir(HostAgentSkillsDir(session))
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), agentSkillsUpdatePrefix) {
			continue
		}
		root := filepath.Join(parent, entry.Name())
		if !entry.IsDir() {
			continue
		}
		owned, err := agentSkillsOwnedMarker(filepath.Join(root, agentSkillsUpdateOwnerName), agentSkillsUpdateOwner)
		if err != nil {
			return err
		}
		if !owned {
			// A familiar prefix is not ownership. Preserve user-created paths and
			// a process interrupted between creating a directory and marking it.
			continue
		}
		journalPath := filepath.Join(root, agentSkillsUpdateJournalName)
		if err := validateAgentSkillsControlFile(journalPath); err != nil {
			return err
		}
		data, err := os.ReadFile(journalPath)
		if isAgentSkillMissing(err) {
			if err := workspaces.RemoveOwnedDirectory(root); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		update := agentSkillsUpdate{root: root, session: session}
		if err := json.Unmarshal(data, &update.journal); err != nil {
			return fmt.Errorf("read agent skills recovery record: %w", err)
		}
		if update.journal.Version != 1 {
			return fmt.Errorf("unsupported agent skills recovery record version")
		}
		if !update.journal.Committed {
			if err := update.rollback(operations); err != nil {
				return err
			}
		}
		if err := update.cleanup(); err != nil {
			return err
		}
	}
	return nil
}

func agentSkillsOwnedMarker(path, expected string) (bool, error) {
	info, err := os.Lstat(path)
	if isAgentSkillMissing(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(expected)) {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return string(data) == expected, nil
}
