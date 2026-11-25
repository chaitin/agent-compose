package volumes

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/nfsvolume"
)

var volumeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// NormalizeName validates a volume name and returns its canonical spelling.
func NormalizeName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("volume name is required")
	}
	if !volumeNamePattern.MatchString(trimmed) {
		return "", fmt.Errorf("volume name %q must match %s", trimmed, volumeNamePattern.String())
	}
	return trimmed, nil
}

// NormalizeDriver defaults an empty driver to the local driver.
func NormalizeDriver(driver string) string {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver == "" {
		return domain.VolumeDriverLocal
	}
	return driver
}

// IsSupportedDriver reports whether driver names a volume driver agent-compose
// implements. The driver must already be normalized.
func IsSupportedDriver(driver string) bool {
	switch driver {
	case domain.VolumeDriverLocal, domain.VolumeDriverK8s, domain.VolumeDriverNFS:
		return true
	default:
		return false
	}
}

// NormalizeRecord enforces the invariants a stored volume must satisfy.
func NormalizeRecord(item domain.VolumeRecord) (domain.VolumeRecord, error) {
	item.ID = strings.TrimSpace(item.ID)
	name, err := NormalizeName(item.Name)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	item.Name = name
	item.Driver = NormalizeDriver(item.Driver)
	item.Path = strings.TrimSpace(item.Path)
	item.ProjectID = strings.TrimSpace(item.ProjectID)
	item.Labels = NormalizeStringMap(item.Labels)
	item.Options = NormalizeStringMap(item.Options)
	if item.ID == "" {
		return domain.VolumeRecord{}, fmt.Errorf("volume id is required")
	}
	if !IsSupportedDriver(item.Driver) {
		return domain.VolumeRecord{}, fmt.Errorf("volume driver %q is not supported", item.Driver)
	}
	return item, nil
}

// NormalizeMountSpec validates one declared mount and canonicalizes its target.
func NormalizeMountSpec(item domain.VolumeMountSpec) (domain.VolumeMountSpec, error) {
	item.Type = strings.ToLower(strings.TrimSpace(item.Type))
	item.Source = strings.TrimSpace(item.Source)
	item.Target = filepath.Clean(strings.TrimSpace(item.Target))
	if item.Target == "." {
		item.Target = ""
	}
	if item.Type == "" {
		item.Type = domain.VolumeMountTypeVolume
	}
	switch item.Type {
	case domain.VolumeMountTypeVolume:
		if _, err := NormalizeName(item.Source); err != nil {
			return domain.VolumeMountSpec{}, fmt.Errorf("volume mount source: %w", err)
		}
	case domain.VolumeMountTypeBind:
		if item.Source == "" {
			return domain.VolumeMountSpec{}, fmt.Errorf("bind mount source is required")
		}
	default:
		return domain.VolumeMountSpec{}, fmt.Errorf("volume mount type %q is not supported", item.Type)
	}
	if item.Target == "" {
		return domain.VolumeMountSpec{}, fmt.Errorf("volume mount target is required")
	}
	if !filepath.IsAbs(item.Target) {
		return domain.VolumeMountSpec{}, fmt.Errorf("volume mount target %q must be absolute", item.Target)
	}
	return item, nil
}

// NormalizeMountSpecs normalizes a declared mount set and rejects duplicate
// targets, which would otherwise shadow each other at mount time.
func NormalizeMountSpecs(items []domain.VolumeMountSpec) ([]domain.VolumeMountSpec, error) {
	if len(items) == 0 {
		return nil, nil
	}
	normalized := make([]domain.VolumeMountSpec, 0, len(items))
	seenTargets := make(map[string]struct{}, len(items))
	for _, item := range items {
		current, err := NormalizeMountSpec(item)
		if err != nil {
			return nil, err
		}
		targetKey := filepath.Clean(current.Target)
		if _, ok := seenTargets[targetKey]; ok {
			return nil, fmt.Errorf("duplicate volume mount target %q", current.Target)
		}
		seenTargets[targetKey] = struct{}{}
		normalized = append(normalized, current)
	}
	return normalized, nil
}

// ValidateDriverMountSpecs rejects mount types that cannot be represented by a
// runtime driver. K8s Pods do not share the daemon's filesystem, so a local
// bind source has no meaningful path inside the Pod.
func ValidateDriverMountSpecs(driver string, items []domain.VolumeMountSpec) error {
	if strings.ToLower(strings.TrimSpace(driver)) != domain.VolumeDriverK8s {
		return nil
	}
	for _, item := range items {
		if strings.ToLower(strings.TrimSpace(item.Type)) != domain.VolumeMountTypeBind {
			continue
		}
		return fmt.Errorf("k8s driver does not support local bind mounts (source %q, target %q); use a named volume instead", strings.TrimSpace(item.Source), strings.TrimSpace(item.Target))
	}
	return nil
}

// ValidateResolvedDriverMounts applies the same restriction after volume
// records have been looked up. This catches a named volume that still uses the
// local driver before a sandbox is created.
func ValidateResolvedDriverMounts(driver string, items []domain.SandboxVolumeMount) error {
	if strings.ToLower(strings.TrimSpace(driver)) != domain.VolumeDriverK8s {
		return nil
	}
	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case domain.VolumeMountTypeBind:
			return fmt.Errorf("k8s driver does not support local bind mounts (source %q, target %q); use a named volume instead", strings.TrimSpace(item.Source), strings.TrimSpace(item.Target))
		case domain.VolumeMountTypeVolume:
			switch NormalizeDriver(item.Driver) {
			case domain.VolumeDriverK8s:
			case domain.VolumeDriverNFS:
				// A Pod mounts the export through an inline NFS volume, which
				// has no field for a protocol version or mount options.
				source, err := nfsvolume.ParseRef(item.HostPath)
				if err != nil {
					return fmt.Errorf("volume %q: %w", strings.TrimSpace(item.Source), err)
				}
				if source.HasCustomMountSettings() {
					return fmt.Errorf("k8s driver cannot apply the version or mount_options of nfs volume %q; remove them so the node's NFS defaults apply, or use a k8s volume backed by an NFS StorageClass", strings.TrimSpace(item.Source))
				}
			default:
				return fmt.Errorf("k8s driver does not support volume driver %q for source %q; use a volume with driver k8s or nfs", NormalizeDriver(item.Driver), strings.TrimSpace(item.Source))
			}
		}
	}
	return nil
}

// NormalizeSandboxMounts drops resolved mounts that lost a field they cannot
// work without, so a sandbox never carries a half-resolved mount.
func NormalizeSandboxMounts(items []domain.SandboxVolumeMount) []domain.SandboxVolumeMount {
	if len(items) == 0 {
		return nil
	}
	out := make([]domain.SandboxVolumeMount, 0, len(items))
	for _, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		item.Source = strings.TrimSpace(item.Source)
		item.Target = filepath.Clean(strings.TrimSpace(item.Target))
		item.VolumeID = strings.TrimSpace(item.VolumeID)
		item.Driver = NormalizeDriver(item.Driver)
		item.HostPath = strings.TrimSpace(item.HostPath)
		item.ProjectPath = strings.TrimSpace(item.ProjectPath)
		if item.Type == "" || item.Target == "." || item.Target == "" || item.HostPath == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

// NormalizeStringMap trims keys and values and drops empty keys, returning nil
// for an empty result so stored labels and options compare equal when unset.
func NormalizeStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make(map[string]string, len(values))
	for _, key := range keys {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		out[name] = strings.TrimSpace(values[key])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
