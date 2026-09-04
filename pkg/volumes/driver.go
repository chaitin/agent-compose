package volumes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type Driver interface {
	Name() string
	Create(context.Context, domain.VolumeRecord) (domain.VolumeRecord, error)
	Inspect(context.Context, domain.VolumeRecord) (domain.VolumeRecord, error)
	Remove(context.Context, domain.VolumeRecord) error
	ResolveMountSource(context.Context, domain.VolumeRecord) (string, error)
}

type LocalDriver struct {
	DataRoot string
}

func NewLocalDriver(config *appconfig.Config) LocalDriver {
	root := ""
	if config != nil {
		root = config.DataRoot
	}
	return LocalDriver{DataRoot: root}
}

func (d LocalDriver) Name() string {
	return domain.VolumeDriverLocal
}

func (d LocalDriver) Create(_ context.Context, record domain.VolumeRecord) (domain.VolumeRecord, error) {
	record.Driver = d.Name()
	path := strings.TrimSpace(record.Path)
	if path == "" {
		path = d.dataPath(record.ID)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("resolve local volume path %s: %w", path, err)
	}
	if err := os.MkdirAll(absPath, 0o755); err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("create local volume path %s: %w", absPath, err)
	}
	record.Path = absPath
	return record, nil
}

func (d LocalDriver) Inspect(_ context.Context, record domain.VolumeRecord) (domain.VolumeRecord, error) {
	path, err := d.ResolveMountSource(context.Background(), record)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	record.Path = path
	return record, nil
}

func (d LocalDriver) Remove(_ context.Context, record domain.VolumeRecord) error {
	path := strings.TrimSpace(record.Path)
	if path == "" {
		path = d.dataPath(record.ID)
	}
	if path == "" {
		return fmt.Errorf("local volume path is required")
	}
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("resolve local volume path %s: %w", path, err)
	}
	managedRoot, err := filepath.Abs(filepath.Clean(filepath.Join(strings.TrimSpace(d.DataRoot), "volumes", domain.VolumeDriverLocal)))
	if err != nil || strings.TrimSpace(d.DataRoot) == "" {
		return fmt.Errorf("local volume data root is required")
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("resolve local volume path %s: %w", absPath, err)
	}
	resolvedRoot, err := canonicalizePathAllowMissing(managedRoot)
	if err != nil {
		return fmt.Errorf("resolve local volume data root %s: %w", managedRoot, err)
	}
	if err := pathWithinRoot(resolvedRoot, resolvedPath); err != nil {
		return fmt.Errorf("refuse to remove local volume path %s: %w", absPath, err)
	}
	removePath := resolvedPath
	if managedPath := d.dataPath(record.ID); managedPath != "" {
		absManagedPath, managedErr := filepath.Abs(filepath.Clean(managedPath))
		if managedErr == nil && absPath == absManagedPath {
			volumePath := filepath.Dir(absManagedPath)
			resolvedVolumePath, resolveErr := canonicalizePathAllowMissing(volumePath)
			if resolveErr == nil && pathWithinRoot(resolvedRoot, resolvedVolumePath) == nil {
				removePath = resolvedVolumePath
			}
		}
	}
	if err := os.RemoveAll(removePath); err != nil {
		return fmt.Errorf("remove local volume path %s: %w", removePath, err)
	}
	return nil
}

func (d LocalDriver) ResolveMountSource(_ context.Context, record domain.VolumeRecord) (string, error) {
	path := strings.TrimSpace(record.Path)
	if path == "" {
		path = d.dataPath(record.ID)
	}
	if path == "" {
		return "", fmt.Errorf("local volume path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve local volume path %s: %w", path, err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat local volume path %s: %w", absPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("local volume path %s is not a directory", absPath)
	}
	return absPath, nil
}

func canonicalizePathAllowMissing(path string) (string, error) {
	path = filepath.Clean(path)
	missing := make([]string, 0)
	for {
		_, err := os.Lstat(path)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", os.ErrNotExist
		}
		missing = append(missing, filepath.Base(path))
		path = parent
	}
}

func pathWithinRoot(root, candidate string) error {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return err
	}
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == "." {
		return fmt.Errorf("path is outside managed root")
	}
	return nil
}

func (d LocalDriver) dataPath(volumeID string) string {
	root := strings.TrimSpace(d.DataRoot)
	if root == "" || strings.TrimSpace(volumeID) == "" {
		return ""
	}
	return filepath.Join(root, "volumes", domain.VolumeDriverLocal, strings.TrimSpace(volumeID), "data")
}
