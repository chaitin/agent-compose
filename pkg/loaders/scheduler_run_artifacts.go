package loaders

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"agent-compose/pkg/identity"
)

func (a FSArtifacts) InspectRunArtifacts(loaderID, runID, recordedDir string) (SchedulerRunArtifactInfo, error) {
	target, err := a.safeRunArtifactsPath(loaderID, runID, recordedDir)
	if err != nil {
		return SchedulerRunArtifactInfo{}, err
	}
	info := SchedulerRunArtifactInfo{Path: target}
	entry, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return info, nil
		}
		return SchedulerRunArtifactInfo{}, fmt.Errorf("inspect scheduler run artifacts: %w", err)
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return SchedulerRunArtifactInfo{}, fmt.Errorf("scheduler run artifacts path must not be a symlink")
	}
	if !entry.IsDir() {
		return SchedulerRunArtifactInfo{}, fmt.Errorf("scheduler run artifacts path is not a directory")
	}
	info.Exists = true
	if err := filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			fileInfo, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if fileInfo.Size() > 0 {
				info.Bytes += uint64(fileInfo.Size())
			}
		}
		return nil
	}); err != nil {
		return SchedulerRunArtifactInfo{}, fmt.Errorf("measure scheduler run artifacts: %w", err)
	}
	return info, nil
}

func (a FSArtifacts) RemoveRunArtifacts(loaderID, runID, recordedDir string) (SchedulerRunArtifactInfo, error) {
	info, err := a.InspectRunArtifacts(loaderID, runID, recordedDir)
	if err != nil || !info.Exists {
		return info, err
	}
	if err := os.RemoveAll(info.Path); err != nil {
		return info, fmt.Errorf("remove scheduler run artifacts: %w", err)
	}
	return info, nil
}

func (a FSArtifacts) safeRunArtifactsPath(loaderID, runID, recordedDir string) (string, error) {
	loaderID = strings.TrimSpace(loaderID)
	runID = strings.TrimSpace(runID)
	if !identity.IsID(loaderID) || !identity.IsID(runID) {
		return "", fmt.Errorf("loader and run ids must be complete resource ids")
	}
	if strings.TrimSpace(a.DataRoot) == "" {
		return "", fmt.Errorf("scheduler run artifacts data root is empty")
	}
	dataRoot, err := filepath.Abs(a.DataRoot)
	if err != nil {
		return "", fmt.Errorf("resolve scheduler run artifacts data root: %w", err)
	}
	runsRoot := filepath.Join(dataRoot, "loaders", loaderID, "runs")
	runsRoot, err = filepath.Abs(runsRoot)
	if err != nil {
		return "", fmt.Errorf("resolve scheduler run artifacts root: %w", err)
	}
	target, err := filepath.Abs(filepath.Join(runsRoot, runID))
	if err != nil {
		return "", fmt.Errorf("resolve scheduler run artifacts path: %w", err)
	}
	relative, err := filepath.Rel(runsRoot, target)
	if err != nil || relative != runID || filepath.Base(target) != runID {
		return "", fmt.Errorf("scheduler run artifacts path escapes the runs root")
	}
	if recordedDir = strings.TrimSpace(recordedDir); recordedDir != "" {
		recorded, absErr := filepath.Abs(recordedDir)
		if absErr != nil || filepath.Clean(recorded) != filepath.Clean(target) {
			return "", fmt.Errorf("recorded artifacts directory does not match the canonical run path")
		}
	}
	if err := rejectSymlinkPathBelowRoot(dataRoot, filepath.Dir(target)); err != nil {
		return "", err
	}
	return target, nil
}

func rejectSymlinkPathBelowRoot(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("scheduler run artifacts parent escapes the data root")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		entry, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return fmt.Errorf("inspect scheduler run artifacts parent: %w", statErr)
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("scheduler run artifacts parent must not contain symlinks")
		}
		if !entry.IsDir() {
			return fmt.Errorf("scheduler run artifacts parent is not a directory")
		}
	}
	return nil
}
