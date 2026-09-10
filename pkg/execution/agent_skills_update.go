package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

type agentSkillsProjection struct {
	current  map[string]ResolvedAgentSkill
	names    []string
	previous agentSkillsManifest
}

func prepareAgentSkillsUpdate(ctx context.Context, session *domain.Sandbox, projection agentSkillsProjection, operations agentSkillsFileOperations) (update *agentSkillsUpdate, err error) {
	current, names := projection.current, projection.names
	skillsDir := HostAgentSkillsDir(session)
	oldManifest, readErr := os.ReadFile(filepath.Join(skillsDir, agentSkillsManifestFileName))
	if readErr != nil && !isAgentSkillMissing(readErr) {
		return nil, readErr
	}
	fingerprints := make(map[string]string, len(current))
	for name, skill := range current {
		fingerprints[name] = skill.Fingerprint
	}
	nextManifest, err := agentSkillsManifestBytes(agentSkillsManifest{Version: 2, Names: names, Fingerprints: fingerprints})
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(filepath.Dir(skillsDir), agentSkillsUpdatePrefix)
	if err != nil {
		return nil, err
	}
	update = &agentSkillsUpdate{root: root, session: session, nextManifest: nextManifest, journal: agentSkillsUpdateJournal{Version: 1, PreviousManifest: oldManifest, HadManifest: readErr == nil}}
	defer func() {
		if err != nil {
			err = errors.Join(err, workspaces.RemoveOwnedDirectory(root))
		}
	}()
	if err = os.WriteFile(filepath.Join(root, agentSkillsUpdateOwnerName), []byte(agentSkillsUpdateOwner), 0o600); err != nil {
		return nil, err
	}
	if err = stageAgentSkillsChanges(ctx, update, projection, operations); err != nil {
		return nil, err
	}
	if err = stageClaudeSkillsProjection(ctx, update, projection, operations); err != nil {
		return nil, err
	}
	if len(update.journal.Operations) == 0 && bytes.Equal(oldManifest, nextManifest) {
		return nil, workspaces.RemoveOwnedDirectory(root)
	}
	if err = update.save(); err != nil {
		return nil, err
	}
	return update, nil
}

func stageAgentSkillsChanges(ctx context.Context, update *agentSkillsUpdate, projection agentSkillsProjection, operations agentSkillsFileOperations) error {
	current, names, previous := projection.current, projection.names, projection.previous
	skillsDir := HostAgentSkillsDir(update.session)
	for _, name := range names {
		target := filepath.Join(skillsDir, name)
		if _, err := os.Lstat(target); err == nil && !slices.Contains(previous.Names, name) {
			return fmt.Errorf("agent skill path %s already exists and is not managed by agent-compose", target)
		} else if err != nil && !isAgentSkillMissing(err) {
			return err
		}
		actual, fingerprintErr := FingerprintAgentSkill(ctx, target)
		if fingerprintErr == nil && actual == current[name].Fingerprint {
			continue
		}
		if fingerprintErr != nil && !isAgentSkillMissing(fingerprintErr) && !errors.Is(fingerprintErr, errInvalidAgentSkillEntry) {
			return fmt.Errorf("inspect existing agent skill %s: %w", name, fingerprintErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		stage := "skill-" + name
		if err := copyAgentSkillToStaging(ctx, current[name], filepath.Join(update.root, stage), operations); err != nil {
			return err
		}
		if err := update.addOperation("skill:"+name, stage); err != nil {
			return err
		}
	}
	for _, name := range previous.Names {
		if validateAgentSkillName(name) != nil {
			continue
		}
		if _, ok := current[name]; ok {
			continue
		}
		if err := update.addOperation("skill:"+name, "removed-"+name); err != nil {
			return err
		}
	}
	return nil
}

func (u *agentSkillsUpdate) publish(ctx context.Context, operations agentSkillsFileOperations) error {
	for _, change := range u.journal.Operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		target, err := u.target(change.Target)
		if err != nil {
			return err
		}
		staged := filepath.Join(u.root, change.Stage)
		switch {
		case !change.New.Exists:
			err = os.Rename(target, staged)
		case !change.Old.Exists:
			err = os.Rename(staged, target)
		default:
			err = operations.exchange(staged, target)
			if err != nil {
				err = fmt.Errorf("atomic skill publication is unavailable; existing data was preserved: %w", err)
			}
		}
		if err != nil {
			return fmt.Errorf("publish %s: %w", change.Target, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeAgentSkillsAtomicFile(filepath.Join(HostAgentSkillsDir(u.session), agentSkillsManifestFileName), u.nextManifest)
}

func (u *agentSkillsUpdate) abort(cause error, operations agentSkillsFileOperations) error {
	if err := u.rollback(operations); err != nil {
		return errors.Join(cause, fmt.Errorf("skills rollback will retry on next preparation: %w", err))
	}
	return errors.Join(cause, u.cleanup())
}

func (u *agentSkillsUpdate) commit() error  { u.journal.Committed = true; return u.save() }
func (u *agentSkillsUpdate) cleanup() error { return workspaces.RemoveOwnedDirectory(u.root) }
