package schedulers

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

type schedulerSandboxConfig struct {
	WorkspaceID        string                   `json:"workspace_id,omitempty"`
	AgentID            string                   `json:"agent_id,omitempty"`
	Driver             string                   `json:"driver,omitempty"`
	GuestImage         string                   `json:"guest_image,omitempty"`
	DefaultAgent       string                   `json:"default_agent,omitempty"`
	SandboxPolicy      string                   `json:"sandbox_policy,omitempty"`
	CapsetIDs          []string                 `json:"capset_ids,omitempty"`
	EnvItems           []domain.SandboxEnvVar   `json:"env_items,omitempty"`
	Volumes            []domain.VolumeMountSpec `json:"volumes,omitempty"`
	ProjectID          string                   `json:"managed_project_id,omitempty"`
	AgentName          string                   `json:"managed_agent_name,omitempty"`
	ProjectSchedulerID string                   `json:"managed_scheduler_id,omitempty"`
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

// SchedulerSandboxConfigHash identifies the Scheduler configuration that is baked
// into a sticky sandbox. Scheduling and presentation fields are deliberately
// excluded because changing them does not require replacing the sandbox.
func SchedulerSandboxConfigHash(scheduler domain.Scheduler) (string, error) {
	driver := strings.TrimSpace(scheduler.Summary.Driver)
	if driver != "" {
		var err error
		driver, err = driverpkg.ResolveSandboxRuntimeDriver(driver, driver)
		if err != nil {
			return "", err
		}
	}
	volumes, err := domain.NormalizeVolumeMountSpecs(scheduler.Volumes)
	if err != nil {
		return "", err
	}
	capsetIDs := capabilities.NormalizeCapsetIDs(scheduler.Summary.CapsetIDs)
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
	defaultAgent := domain.NormalizeAgentKind(scheduler.Summary.DefaultAgent)
	if defaultAgent == "" {
		defaultAgent = domain.DefaultAgentProvider
	}
	payload, err := json.Marshal(schedulerSandboxConfig{
		WorkspaceID:        strings.TrimSpace(scheduler.Summary.WorkspaceID),
		AgentID:            strings.TrimSpace(scheduler.Summary.AgentID),
		Driver:             driver,
		GuestImage:         strings.TrimSpace(scheduler.Summary.GuestImage),
		DefaultAgent:       defaultAgent,
		SandboxPolicy:      domain.NormalizeSchedulerSandboxPolicy(scheduler.Summary.SandboxPolicy),
		CapsetIDs:          capsetIDs,
		EnvItems:           domain.NormalizeEnvItems(scheduler.EnvItems),
		Volumes:            volumes,
		ProjectID:          strings.TrimSpace(scheduler.Summary.ProjectID),
		AgentName:          strings.TrimSpace(scheduler.Summary.AgentName),
		ProjectSchedulerID: strings.TrimSpace(scheduler.Summary.ProjectSchedulerID),
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
