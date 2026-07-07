package model

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	VolumeDriverLocal = "local"

	VolumeMountTypeVolume = "volume"
	VolumeMountTypeBind   = "bind"
)

var volumeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type VolumeRecord struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Driver    string            `json:"driver"`
	Path      string            `json:"path,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Options   map[string]string `json:"options,omitempty"`
	ProjectID string            `json:"project_id,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type VolumeMountSpec struct {
	Type     string `json:"type,omitempty" yaml:"type,omitempty"`
	Source   string `json:"source,omitempty" yaml:"source,omitempty"`
	Target   string `json:"target,omitempty" yaml:"target,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty" yaml:"read_only,omitempty"`
}

type SessionVolumeMount struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	ReadOnly    bool   `json:"read_only,omitempty"`
	VolumeID    string `json:"volume_id,omitempty"`
	Driver      string `json:"driver,omitempty"`
	HostPath    string `json:"host_path"`
	ProjectPath string `json:"project_path,omitempty"`
}

type VolumeReference struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Name         string `json:"name,omitempty"`
}

func NormalizeVolumeName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("volume name is required")
	}
	if !volumeNamePattern.MatchString(trimmed) {
		return "", fmt.Errorf("volume name %q must match %s", trimmed, volumeNamePattern.String())
	}
	return trimmed, nil
}

func NormalizeVolumeDriver(driver string) string {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver == "" {
		return VolumeDriverLocal
	}
	return driver
}

func NormalizeVolumeRecord(item VolumeRecord) (VolumeRecord, error) {
	item.ID = strings.TrimSpace(item.ID)
	name, err := NormalizeVolumeName(item.Name)
	if err != nil {
		return VolumeRecord{}, err
	}
	item.Name = name
	item.Driver = NormalizeVolumeDriver(item.Driver)
	item.Path = strings.TrimSpace(item.Path)
	item.ProjectID = strings.TrimSpace(item.ProjectID)
	item.Labels = NormalizeStringMap(item.Labels)
	item.Options = NormalizeStringMap(item.Options)
	if item.ID == "" {
		return VolumeRecord{}, fmt.Errorf("volume id is required")
	}
	if item.Driver != VolumeDriverLocal {
		return VolumeRecord{}, fmt.Errorf("volume driver %q is not supported", item.Driver)
	}
	return item, nil
}

func NormalizeVolumeMountSpec(item VolumeMountSpec) (VolumeMountSpec, error) {
	item.Type = strings.ToLower(strings.TrimSpace(item.Type))
	item.Source = strings.TrimSpace(item.Source)
	item.Target = filepath.Clean(strings.TrimSpace(item.Target))
	if item.Target == "." {
		item.Target = ""
	}
	if item.Type == "" {
		item.Type = VolumeMountTypeVolume
	}
	switch item.Type {
	case VolumeMountTypeVolume:
		if _, err := NormalizeVolumeName(item.Source); err != nil {
			return VolumeMountSpec{}, fmt.Errorf("volume mount source: %w", err)
		}
	case VolumeMountTypeBind:
		if item.Source == "" {
			return VolumeMountSpec{}, fmt.Errorf("bind mount source is required")
		}
	default:
		return VolumeMountSpec{}, fmt.Errorf("volume mount type %q is not supported", item.Type)
	}
	if item.Target == "" {
		return VolumeMountSpec{}, fmt.Errorf("volume mount target is required")
	}
	if !filepath.IsAbs(item.Target) {
		return VolumeMountSpec{}, fmt.Errorf("volume mount target %q must be absolute", item.Target)
	}
	return item, nil
}

func NormalizeVolumeMountSpecs(items []VolumeMountSpec) ([]VolumeMountSpec, error) {
	if len(items) == 0 {
		return nil, nil
	}
	normalized := make([]VolumeMountSpec, 0, len(items))
	seenTargets := make(map[string]struct{}, len(items))
	for _, item := range items {
		current, err := NormalizeVolumeMountSpec(item)
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

func NormalizeSessionVolumeMounts(items []SessionVolumeMount) []SessionVolumeMount {
	if len(items) == 0 {
		return nil
	}
	out := make([]SessionVolumeMount, 0, len(items))
	for _, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		item.Source = strings.TrimSpace(item.Source)
		item.Target = filepath.Clean(strings.TrimSpace(item.Target))
		item.VolumeID = strings.TrimSpace(item.VolumeID)
		item.Driver = NormalizeVolumeDriver(item.Driver)
		item.HostPath = strings.TrimSpace(item.HostPath)
		item.ProjectPath = strings.TrimSpace(item.ProjectPath)
		if item.Type == "" || item.Target == "." || item.Target == "" || item.HostPath == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

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
