package volumes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	domain "agent-compose/pkg/model"
)

type Store interface {
	CreateVolume(context.Context, domain.VolumeRecord) (domain.VolumeRecord, error)
	UpdateVolume(context.Context, domain.VolumeRecord) (domain.VolumeRecord, error)
	GetVolume(context.Context, string) (domain.VolumeRecord, error)
	GetVolumeIfExists(context.Context, string) (domain.VolumeRecord, bool, error)
	ListVolumes(context.Context, domain.VolumeListOptions) ([]domain.VolumeRecord, error)
	RemoveVolume(context.Context, string) error
	DeleteVolume(context.Context, string) error
	FindVolumeConfigReferences(context.Context, string) ([]domain.VolumeReference, error)
}

type SandboxStore interface {
	ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error)
}

type ProjectVolumeStore interface {
	ReplaceProjectVolumes(ctx context.Context, projectID string, links map[string]domain.ProjectVolumeLink) error
	ListProjectVolumes(ctx context.Context, projectID string) (map[string]domain.VolumeRecord, error)
	RemoveProjectVolumes(ctx context.Context, projectID string) error
}

type Manager struct {
	Store     Store
	Sandboxes SandboxStore
	Project   ProjectVolumeStore
	Drivers   map[string]Driver
}

type BindResolver struct {
	ProjectRoot string
}

type ResolveOptions struct {
	ProjectRoot    string
	ProjectVolumes map[string]domain.VolumeRecord
}

type PruneResult struct {
	DryRun  bool
	Matched []domain.VolumeRecord
	Removed []domain.VolumeRecord
	Skipped []domain.VolumeRecord
}

func NewManager(store Store, drivers ...Driver) *Manager {
	manager := &Manager{Store: store, Drivers: make(map[string]Driver)}
	if project, ok := store.(ProjectVolumeStore); ok {
		manager.Project = project
	}
	for _, driver := range drivers {
		if driver == nil {
			continue
		}
		manager.Drivers[driver.Name()] = driver
	}
	return manager
}

func (m *Manager) Create(ctx context.Context, item domain.VolumeRecord) (domain.VolumeRecord, error) {
	if m == nil || m.Store == nil {
		return domain.VolumeRecord{}, fmt.Errorf("volume store is required")
	}
	item.Driver = domain.NormalizeVolumeDriver(item.Driver)
	driver, err := m.driver(item.Driver)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	generatedID := strings.TrimSpace(item.ID) == ""
	if generatedID {
		item.ID = uuid.NewString()
	}
	prepared, err := driver.Create(ctx, item)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	created, err := m.Store.CreateVolume(ctx, prepared)
	if err != nil {
		if generatedID && strings.TrimSpace(item.Path) == "" {
			_ = driver.Remove(ctx, prepared)
		}
		return domain.VolumeRecord{}, err
	}
	return created, nil
}

func (m *Manager) Ensure(ctx context.Context, item domain.VolumeRecord) (domain.VolumeRecord, bool, error) {
	if m == nil || m.Store == nil {
		return domain.VolumeRecord{}, false, fmt.Errorf("volume store is required")
	}
	name, err := domain.NormalizeVolumeName(item.Name)
	if err != nil {
		return domain.VolumeRecord{}, false, err
	}
	if existing, found, err := m.Store.GetVolumeIfExists(ctx, name); err != nil {
		return domain.VolumeRecord{}, false, err
	} else if found {
		return existing, false, nil
	}
	item.Name = name
	created, err := m.Create(ctx, item)
	return created, true, err
}

func (m *Manager) Inspect(ctx context.Context, nameOrID string) (domain.VolumeRecord, error) {
	if m == nil || m.Store == nil {
		return domain.VolumeRecord{}, fmt.Errorf("volume store is required")
	}
	item, err := m.Store.GetVolume(ctx, nameOrID)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	driver, err := m.driver(item.Driver)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	return driver.Inspect(ctx, item)
}

func (m *Manager) List(ctx context.Context, options domain.VolumeListOptions) ([]domain.VolumeRecord, error) {
	if m == nil || m.Store == nil {
		return nil, fmt.Errorf("volume store is required")
	}
	options.Driver = domain.NormalizeVolumeDriver(options.Driver)
	if strings.TrimSpace(options.Driver) == domain.VolumeDriverLocal || strings.TrimSpace(options.Driver) == "" {
		return m.Store.ListVolumes(ctx, options)
	}
	return nil, fmt.Errorf("volume driver %q is not configured", options.Driver)
}

func (m *Manager) ReplaceProjectVolumes(ctx context.Context, projectID string, links map[string]domain.ProjectVolumeLink) error {
	if m == nil || m.Project == nil {
		return fmt.Errorf("project volume store is required")
	}
	return m.Project.ReplaceProjectVolumes(ctx, projectID, links)
}

func (m *Manager) RemoveProjectVolumes(ctx context.Context, projectID string) error {
	if m == nil || m.Project == nil {
		return fmt.Errorf("project volume store is required")
	}
	return m.Project.RemoveProjectVolumes(ctx, projectID)
}

func (m *Manager) Remove(ctx context.Context, nameOrID string, force bool) error {
	if m == nil || m.Store == nil {
		return fmt.Errorf("volume store is required")
	}
	item, err := m.Store.GetVolume(ctx, nameOrID)
	if err != nil {
		return err
	}
	refs, err := m.findReferences(ctx, item.ID, referenceOptions{SkipConfig: force})
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return domain.ResourceError(domain.ErrReferenced, "volume", item.Name, fmt.Sprintf("volume %s is still referenced", item.Name), nil)
	}
	driver, err := m.driver(item.Driver)
	if err != nil {
		return err
	}
	if err := driver.Remove(ctx, item); err != nil {
		return err
	}
	if err := m.Store.DeleteVolume(ctx, item.ID); err != nil {
		return err
	}
	return nil
}

func (m *Manager) Prune(ctx context.Context, options domain.VolumeListOptions, force bool) (PruneResult, error) {
	items, err := m.List(ctx, options)
	if err != nil {
		return PruneResult{}, err
	}
	result := PruneResult{DryRun: !force}
	for _, item := range items {
		refs, err := m.findReferences(ctx, item.ID, referenceOptions{})
		if err != nil {
			return PruneResult{}, err
		}
		if len(refs) > 0 {
			result.Skipped = append(result.Skipped, item)
			continue
		}
		result.Matched = append(result.Matched, item)
		if !force {
			continue
		}
		if err := m.Remove(ctx, item.ID, false); err != nil {
			return PruneResult{}, err
		}
		result.Removed = append(result.Removed, item)
	}
	return result, nil
}

type referenceOptions struct {
	SkipConfig bool
}

func (m *Manager) findReferences(ctx context.Context, volumeID string, options referenceOptions) ([]domain.VolumeReference, error) {
	var refs []domain.VolumeReference
	if !options.SkipConfig {
		configRefs, err := m.Store.FindVolumeConfigReferences(ctx, volumeID)
		if err != nil {
			return nil, err
		}
		refs = append(refs, configRefs...)
	}
	sandboxRefs, err := m.findSandboxReferences(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	refs = append(refs, sandboxRefs...)
	return refs, nil
}

func (m *Manager) findSandboxReferences(ctx context.Context, volumeID string) ([]domain.VolumeReference, error) {
	volumeID = strings.TrimSpace(volumeID)
	if volumeID == "" || m == nil || m.Sandboxes == nil {
		return nil, nil
	}
	const pageSize = 500
	var refs []domain.VolumeReference
	for offset := 0; ; {
		result, err := m.Sandboxes.ListSandboxes(ctx, domain.SandboxListOptions{Offset: offset, Limit: pageSize})
		if err != nil {
			return nil, fmt.Errorf("list sandboxes for volume references: %w", err)
		}
		for _, sandbox := range result.Sandboxes {
			if sandbox == nil {
				continue
			}
			for _, mount := range sandbox.VolumeMounts {
				if strings.TrimSpace(mount.VolumeID) != volumeID {
					continue
				}
				refs = append(refs, domain.VolumeReference{
					ResourceType: "sandbox",
					ResourceID:   sandbox.Summary.ID,
					Name:         sandbox.Summary.Title,
				})
				break
			}
		}
		if !result.HasMore || len(result.Sandboxes) == 0 {
			break
		}
		if result.NextOffset > offset {
			offset = result.NextOffset
		} else {
			offset += len(result.Sandboxes)
		}
	}
	return refs, nil
}

func (m *Manager) ResolveMounts(ctx context.Context, specs []domain.VolumeMountSpec, options ResolveOptions) ([]domain.SandboxVolumeMount, []string, error) {
	normalized, err := domain.NormalizeVolumeMountSpecs(specs)
	if err != nil {
		return nil, nil, err
	}
	if len(normalized) == 0 {
		return nil, nil, nil
	}
	resolver := BindResolver{ProjectRoot: options.ProjectRoot}
	mounts := make([]domain.SandboxVolumeMount, 0, len(normalized))
	var warnings []string
	for _, spec := range normalized {
		var mount domain.SandboxVolumeMount
		switch spec.Type {
		case domain.VolumeMountTypeBind:
			hostPath, err := resolver.Resolve(spec.Source)
			if err != nil {
				return nil, nil, err
			}
			mount = domain.SandboxVolumeMount{
				ID:          stableVolumeMountID(spec.Type, spec.Source, spec.Target),
				Type:        spec.Type,
				Source:      spec.Source,
				Target:      spec.Target,
				ReadOnly:    spec.ReadOnly,
				HostPath:    hostPath,
				ProjectPath: strings.TrimSpace(options.ProjectRoot),
			}
		case domain.VolumeMountTypeVolume:
			record, ok := options.ProjectVolumes[spec.Source]
			if !ok {
				if m == nil || m.Store == nil {
					return nil, nil, fmt.Errorf("volume store is required")
				}
				var err error
				record, err = m.Store.GetVolume(ctx, spec.Source)
				if err != nil {
					return nil, nil, err
				}
			}
			driver, err := m.driver(record.Driver)
			if err != nil {
				return nil, nil, err
			}
			hostPath, err := driver.ResolveMountSource(ctx, record)
			if err != nil {
				return nil, nil, err
			}
			mount = domain.SandboxVolumeMount{
				ID:       stableVolumeMountID(spec.Type, record.ID, spec.Target),
				Type:     spec.Type,
				Source:   spec.Source,
				Target:   spec.Target,
				ReadOnly: spec.ReadOnly,
				VolumeID: record.ID,
				Driver:   record.Driver,
				HostPath: hostPath,
			}
		default:
			return nil, nil, fmt.Errorf("volume mount type %q is not supported", spec.Type)
		}
		if warning := ReservedTargetWarning(mount.Target); warning != "" {
			warnings = append(warnings, warning)
		}
		mounts = append(mounts, mount)
	}
	return mounts, warnings, nil
}

func (m *Manager) driver(name string) (Driver, error) {
	name = domain.NormalizeVolumeDriver(name)
	if m == nil {
		return nil, fmt.Errorf("volume manager is required")
	}
	driver, ok := m.Drivers[name]
	if !ok {
		return nil, fmt.Errorf("volume driver %q is not configured", name)
	}
	return driver, nil
}

func (r BindResolver) Resolve(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("bind mount source is required")
	}
	path := source
	if !filepath.IsAbs(path) {
		root := strings.TrimSpace(r.ProjectRoot)
		if root == "" {
			var err error
			root, err = os.Getwd()
			if err != nil {
				return "", fmt.Errorf("resolve current dir: %w", err)
			}
		}
		path = filepath.Join(root, path)
	}
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve bind mount source %s: %w", source, err)
	}
	if evaluated, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = evaluated
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat bind mount source %s: %w", absPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("bind mount source %s is not a directory", absPath)
	}
	return absPath, nil
}

func ReservedTargetWarning(target string) string {
	clean := filepath.Clean(strings.TrimSpace(target))
	switch clean {
	case "/", "/data", "/workspace", "/state", "/runtime", "/logs", "/root", "/home":
		return fmt.Sprintf("volume target %s overlaps an agent-compose runtime path", clean)
	default:
		if strings.HasPrefix(clean, "/root/.") ||
			strings.HasPrefix(clean, "/home/") ||
			strings.HasPrefix(clean, "/data/") ||
			strings.HasPrefix(clean, "/workspace/") {
			return fmt.Sprintf("volume target %s may overlap an agent-compose runtime path", clean)
		}
	}
	return ""
}

func stableVolumeMountID(parts ...string) string {
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		normalized = append(normalized, strings.TrimSpace(part))
	}
	joined := strings.Join(normalized, "\x00")
	if strings.TrimSpace(joined) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(joined))
	return "mount-" + hex.EncodeToString(sum[:])[:24]
}
