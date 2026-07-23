package loaders

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"agent-compose/pkg/capabilities"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

type loaderSandboxConfig struct {
	WorkspaceID        string                   `json:"workspace_id,omitempty"`
	AgentID            string                   `json:"agent_id,omitempty"`
	Driver             string                   `json:"driver,omitempty"`
	GuestImage         string                   `json:"guest_image,omitempty"`
	DefaultAgent       string                   `json:"default_agent,omitempty"`
	SandboxPolicy      string                   `json:"sandbox_policy,omitempty"`
	CapsetIDs          []string                 `json:"capset_ids,omitempty"`
	EnvItems           []domain.SandboxEnvVar   `json:"env_items,omitempty"`
	Volumes            []domain.VolumeMountSpec `json:"volumes,omitempty"`
	ManagedProjectID   string                   `json:"managed_project_id,omitempty"`
	ManagedRevision    int64                    `json:"managed_project_revision,omitempty"`
	ManagedAgentName   string                   `json:"managed_agent_name,omitempty"`
	ManagedSchedulerID string                   `json:"managed_scheduler_id,omitempty"`
}

// NormalizeStickySandboxVolumeMounts returns normalized mounts in a canonical
// order suitable for sticky sandbox configuration hashes. The complete mount
// state is used as a tie-breaker so equivalent inputs produce the same order
// even when a boundary supplies conflicting mounts for the same target.
func NormalizeStickySandboxVolumeMounts(items []domain.SandboxVolumeMount) []domain.SandboxVolumeMount {
	mounts := domain.NormalizeSandboxVolumeMounts(items)
	sort.Slice(mounts, func(i, j int) bool {
		left, right := mounts[i], mounts[j]
		if left.Target != right.Target {
			return left.Target < right.Target
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.HostPath != right.HostPath {
			return left.HostPath < right.HostPath
		}
		if left.ID != right.ID {
			return left.ID < right.ID
		}
		if left.ReadOnly != right.ReadOnly {
			return !left.ReadOnly
		}
		if left.VolumeID != right.VolumeID {
			return left.VolumeID < right.VolumeID
		}
		if left.Driver != right.Driver {
			return left.Driver < right.Driver
		}
		return left.ProjectPath < right.ProjectPath
	})
	return mounts
}

// LoaderSandboxConfigHash identifies the Loader configuration that is baked
// into a sticky sandbox. Scheduling and presentation fields are deliberately
// excluded because changing them does not require replacing the sandbox.
func LoaderSandboxConfigHash(loader domain.Loader) (string, error) {
	driver := strings.TrimSpace(loader.Summary.Driver)
	if driver != "" {
		var err error
		driver, err = driverpkg.ResolveSandboxRuntimeDriver(driver, driver)
		if err != nil {
			return "", err
		}
	}
	volumes, err := domain.NormalizeVolumeMountSpecs(loader.Volumes)
	if err != nil {
		return "", err
	}
	capsetIDs := capabilities.NormalizeCapsetIDs(loader.Summary.CapsetIDs)
	sort.Strings(capsetIDs)
	sort.Slice(volumes, func(i, j int) bool {
		if volumes[i].Target != volumes[j].Target {
			return volumes[i].Target < volumes[j].Target
		}
		if volumes[i].Type != volumes[j].Type {
			return volumes[i].Type < volumes[j].Type
		}
		return volumes[i].Source < volumes[j].Source
	})
	defaultAgent := domain.NormalizeAgentKind(loader.Summary.DefaultAgent)
	if defaultAgent == "" {
		defaultAgent = domain.DefaultAgentProvider
	}
	payload, err := json.Marshal(loaderSandboxConfig{
		WorkspaceID:        strings.TrimSpace(loader.Summary.WorkspaceID),
		AgentID:            strings.TrimSpace(loader.Summary.AgentID),
		Driver:             driver,
		GuestImage:         strings.TrimSpace(loader.Summary.GuestImage),
		DefaultAgent:       defaultAgent,
		SandboxPolicy:      domain.NormalizeLoaderSandboxPolicy(loader.Summary.SandboxPolicy),
		CapsetIDs:          capsetIDs,
		EnvItems:           domain.NormalizeEnvItems(loader.EnvItems),
		Volumes:            volumes,
		ManagedProjectID:   strings.TrimSpace(loader.Summary.ManagedProjectID),
		ManagedRevision:    loader.Summary.ManagedRevision,
		ManagedAgentName:   strings.TrimSpace(loader.Summary.ManagedAgentName),
		ManagedSchedulerID: strings.TrimSpace(loader.Summary.ManagedSchedulerID),
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
