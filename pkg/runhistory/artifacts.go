package runhistory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

type SandboxDirs interface {
	SandboxDir(string) string
}

type FSArtifacts struct {
	DataRoot           string
	SandboxRoot        string
	SandboxArchiveRoot string
	SandboxDirs        SandboxDirs
}

func (a FSArtifacts) validate(ctx context.Context, scope Scope) error {
	// Sandbox files also contain notebook, home, and audit state. Their own
	// removal lifecycle must finish first; never unlink cells from a retained
	// or shared sandbox just to hide a run from history.
	for _, sandboxID := range scope.SandboxIDs {
		if !identity.IsID(sandboxID) || a.SandboxDirs == nil {
			return artifactPrecondition("cannot verify sandbox removal", nil)
		}
		path := a.SandboxDirs.SandboxDir(sandboxID)
		if path == "" {
			return artifactPrecondition("sandbox directory is unavailable", nil)
		}
		if err := requireAbsent(path); err != nil {
			return err
		}
		if a.SandboxArchiveRoot != "" {
			if err := requireAbsent(filepath.Join(a.SandboxArchiveRoot, sandboxID)); err != nil {
				return err
			}
		}
	}
	if err := requireDatedSandboxesAbsent(ctx, a.SandboxRoot, scope.SandboxIDs); err != nil {
		return err
	}
	for _, run := range scope.Runs {
		for _, recorded := range []string{run.LogsPath, run.ArtifactsDir} {
			if recorded == "" {
				continue
			}
			if ownsRunDirectory(run) && recorded == run.LogsPath {
				// The run log lives in the run's own directory, which this
				// removal deletes; it has no sandbox to be verified against.
				if err := a.verifyRunLogPath(run); err != nil {
					return err
				}
				continue
			}
			if run.SandboxID == "" || a.SandboxDirs == nil {
				return artifactPrecondition("run artifact has no verifiable sandbox owner", nil)
			}
			if err := a.verifyRunArtifactAbsent(run.SandboxID, recorded); err != nil {
				return err
			}
		}
	}
	if scope.SchedulerRun == nil {
		return ctx.Err()
	}
	run := scope.SchedulerRun
	if !identity.IsID(run.SchedulerID) || !identity.IsID(run.ID) || strings.TrimSpace(a.DataRoot) == "" {
		return artifactPrecondition("cannot verify scheduler artifact owner", nil)
	}
	rootPath, err := filepath.Abs(a.DataRoot)
	if err != nil {
		return err
	}
	relative := filepath.Join("schedulers", run.SchedulerID, "runs", run.ID)
	target := filepath.Join(rootPath, relative)
	if run.ArtifactsDir != "" {
		recorded, err := filepath.Abs(run.ArtifactsDir)
		if err != nil || recorded != target {
			return artifactPrecondition("recorded scheduler artifacts do not match the canonical run path", err)
		}
	}
	return ctx.Err()
}

func (a FSArtifacts) verifyRunLogPath(run domain.ProjectRunRecord) error {
	if strings.TrimSpace(a.DataRoot) == "" {
		return artifactPrecondition("cannot verify run log owner", nil)
	}
	root, err := filepath.Abs(a.DataRoot)
	if err != nil {
		return err
	}
	recorded, err := filepath.Abs(run.LogsPath)
	if err != nil {
		return artifactPrecondition("recorded run log does not match the canonical run path", err)
	}
	canonical, pathErr := runlog.RunPath(root, run.RunID)
	if pathErr != nil || recorded != canonical {
		return artifactPrecondition("recorded run log does not match the canonical run path", pathErr)
	}
	return nil
}

func requireAbsent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return artifactPrecondition("sandbox data or audit archive remains; complete sandbox removal before removing history", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("verify sandbox material removal: %w", err)
	}
	return nil
}

func (a FSArtifacts) verifyRunArtifactAbsent(sandboxID, recorded string) error {
	// The sandbox store names directories by the canonical id; history rows
	// written before identity unification may carry the "sha256:" form.
	if hash, err := identity.Hash(sandboxID); err == nil {
		sandboxID = hash
	}
	path, err := filepath.Abs(recorded)
	if err != nil {
		return err
	}
	base, err := filepath.Abs(a.SandboxDirs.SandboxDir(sandboxID))
	if err != nil {
		return err
	}
	if a.SandboxRoot != "" {
		root, err := filepath.Abs(a.SandboxRoot)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || !filepath.IsLocal(relative) {
			return artifactPrecondition("recorded run artifact is outside the sandbox root", nil)
		}
		parts := strings.Split(relative, string(filepath.Separator))
		switch {
		case len(parts) > 1 && parts[0] == sandboxID:
			base = filepath.Join(root, sandboxID)
		case len(parts) > 4 && parts[3] == sandboxID:
			if _, err := time.Parse("2006/01/02", strings.Join(parts[:3], "/")); err != nil {
				return artifactPrecondition("invalid sandbox date partition", err)
			}
			base = filepath.Join(root, filepath.Join(parts[:4]...))
		default:
			return artifactPrecondition("recorded run artifact does not match its sandbox id", nil)
		}
	}
	relative, err := filepath.Rel(base, path)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return artifactPrecondition("recorded run artifact is outside its sandbox", nil)
	}
	// The sandbox layout cache forgets a dated path after removal. Validate
	// the recorded base independently instead of requiring today's fallback
	// flat path to match historical paths.
	if err := requireAbsent(base); err != nil {
		return err
	}
	return requireAbsent(path)
}

func artifactPrecondition(message string, cause error) error {
	return domain.ClassifyError(domain.ErrFailedPrecondition, message, cause)
}
