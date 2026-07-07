package volumes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domain "agent-compose/pkg/model"
)

type Store interface {
	CreateVolume(context.Context, domain.VolumeRecord) (domain.VolumeRecord, error)
	UpdateVolume(context.Context, domain.VolumeRecord) (domain.VolumeRecord, error)
	GetVolume(context.Context, string) (domain.VolumeRecord, error)
	GetVolumeIfExists(context.Context, string) (domain.VolumeRecord, bool, error)
	RemoveVolume(context.Context, string) error
	FindVolumeConfigReferences(context.Context, string) ([]domain.VolumeReference, error)
}

type ProjectVolumeStore interface {
	UpsertProjectVolume(ctx context.Context, projectID, key, volumeID string, external bool) error
	ListProjectVolumes(ctx context.Context, projectID string) (map[string]domain.VolumeRecord, error)
}

type Manager struct {
	Store   Store
	Project ProjectVolumeStore
	Drivers map[string]Driver
}

type BindResolver struct {
	ProjectRoot string
}

type ResolveOptions struct {
	ProjectRoot    string
	ProjectVolumes map[string]domain.VolumeRecord
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
	if strings.TrimSpace(item.ID) == "" {
		item.ID = stableVolumeID(item.Name)
	}
	prepared, err := driver.Create(ctx, item)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	return m.Store.CreateVolume(ctx, prepared)
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

func (m *Manager) Remove(ctx context.Context, nameOrID string, force bool) error {
	if m == nil || m.Store == nil {
		return fmt.Errorf("volume store is required")
	}
	item, err := m.Store.GetVolume(ctx, nameOrID)
	if err != nil {
		return err
	}
	if !force {
		if refs, err := m.Store.FindVolumeConfigReferences(ctx, item.ID); err != nil {
			return err
		} else if len(refs) > 0 {
			return domain.ResourceError(domain.ErrReferenced, "volume", item.Name, fmt.Sprintf("volume %s is still referenced", item.Name), nil)
		}
	}
	driver, err := m.driver(item.Driver)
	if err != nil {
		return err
	}
	if err := m.Store.RemoveVolume(ctx, item.ID); err != nil {
		return err
	}
	return driver.Remove(ctx, item)
}

func (m *Manager) ResolveMounts(ctx context.Context, specs []domain.VolumeMountSpec, options ResolveOptions) ([]domain.SessionVolumeMount, []string, error) {
	normalized, err := domain.NormalizeVolumeMountSpecs(specs)
	if err != nil {
		return nil, nil, err
	}
	if len(normalized) == 0 {
		return nil, nil, nil
	}
	resolver := BindResolver{ProjectRoot: options.ProjectRoot}
	mounts := make([]domain.SessionVolumeMount, 0, len(normalized))
	var warnings []string
	for _, spec := range normalized {
		var mount domain.SessionVolumeMount
		switch spec.Type {
		case domain.VolumeMountTypeBind:
			hostPath, err := resolver.Resolve(spec.Source)
			if err != nil {
				return nil, nil, err
			}
			mount = domain.SessionVolumeMount{
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
			mount = domain.SessionVolumeMount{
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

func stableVolumeID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "vol-" + strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

func stableVolumeMountID(parts ...string) string {
	joined := strings.Join(parts, "|")
	replacer := strings.NewReplacer("/", "-", ":", "-", "|", "-", " ", "-")
	value := strings.Trim(replacer.Replace(strings.ToLower(joined)), "-")
	if value == "" {
		return ""
	}
	if len(value) > 64 {
		value = value[:64]
	}
	return "mount-" + value
}
