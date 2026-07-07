package volumes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
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
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove local volume path %s: %w", path, err)
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

func (d LocalDriver) dataPath(volumeID string) string {
	root := strings.TrimSpace(d.DataRoot)
	if root == "" || strings.TrimSpace(volumeID) == "" {
		return ""
	}
	return filepath.Join(root, "volumes", domain.VolumeDriverLocal, strings.TrimSpace(volumeID), "data")
}
