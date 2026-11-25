package model

import (
	"time"
)

const (
	VolumeDriverLocal = "local"
	VolumeDriverK8s   = "k8s"
	VolumeDriverNFS   = "nfs"

	VolumeMountTypeVolume = "volume"
	VolumeMountTypeBind   = "bind"
)

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

type SandboxVolumeMount struct {
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

type ProjectVolumeLink struct {
	VolumeID string `json:"volume_id"`
	External bool   `json:"external,omitempty"`
}

type VolumeListOptions struct {
	Query     string `json:"query,omitempty"`
	Driver    string `json:"driver,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}
