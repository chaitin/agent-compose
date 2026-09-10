package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	"github.com/chaitin/agent-compose/pkg/compose"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

type stickyProjectRunSandboxConfig struct {
	// loader_config_hash is a frozen canonical-hash schema key. The one-shot
	// migrator carries stored binding hashes forward but cannot recompute them;
	// renaming this key would retire every existing sticky sandbox.
	SchedulerConfigHash string                      `json:"loader_config_hash"`
	ProjectID           string                      `json:"project_id"`
	ProjectRevision     int64                       `json:"project_revision"`
	AgentName           string                      `json:"agent_name"`
	AgentID             string                      `json:"managed_agent_id"`
	Driver              string                      `json:"driver"`
	ImageRef            string                      `json:"image_ref"`
	EnvItems            []domain.SandboxEnvVar      `json:"env_items,omitempty"`
	CapsetIDs           []string                    `json:"capset_ids,omitempty"`
	Workspace           *domain.SandboxWorkspace    `json:"workspace,omitempty"`
	VolumeMounts        []domain.SandboxVolumeMount `json:"volume_mounts,omitempty"`
	Jupyter             stickyProjectSandboxOptions `json:"jupyter"`
}

// stickyProjectSandboxOptions preserves the serialized shape used before
// stopped-runtime policy existed for explicit retain. The default remove policy
// is added to the hash so existing retained bindings are retired.
type stickyProjectSandboxOptions struct {
	JupyterEnabled       bool
	JupyterGuestPort     int
	JupyterExpose        bool
	VolumeMounts         []domain.SandboxVolumeMount
	StoppedRuntimePolicy string `json:",omitempty"`
}

func stickyProjectSandboxOptionsFrom(options sandboxstore.CreateSandboxOptions) stickyProjectSandboxOptions {
	policy := ""
	if normalized, err := compose.NormalizeStoppedRuntimePolicy(options.StoppedRuntimePolicy); err == nil && normalized == domain.StoppedRuntimePolicyRemove {
		policy = domain.StoppedRuntimePolicyRemove
	}
	return stickyProjectSandboxOptions{
		JupyterEnabled:       options.JupyterEnabled,
		JupyterGuestPort:     options.JupyterGuestPort,
		JupyterExpose:        options.JupyterExpose,
		VolumeMounts:         schedulers.NormalizeStickySandboxVolumeMounts(options.VolumeMounts),
		StoppedRuntimePolicy: policy,
	}
}

// stickySandboxSpec is the effective sandbox spec folded into a sticky
// scheduler binding's config hash.
type stickySandboxSpec struct {
	Driver       string
	GuestImage   string
	VolumeMounts []domain.SandboxVolumeMount
	Jupyter      sandboxstore.CreateSandboxOptions
}

func stickyProjectRunConfigHash(baseHash string, run domain.ProjectRunRecord, prepared Preparation, spec stickySandboxSpec) (string, error) {
	baseHash = strings.TrimSpace(baseHash)
	if baseHash == "" {
		return "", nil
	}
	capsetIDs := capabilities.NormalizeCapsetIDs(prepared.CapsetIDs)
	sort.Strings(capsetIDs)
	volumeMounts := schedulers.NormalizeStickySandboxVolumeMounts(spec.VolumeMounts)
	payload, err := json.Marshal(stickyProjectRunSandboxConfig{
		SchedulerConfigHash: baseHash,
		ProjectID:           run.ProjectID,
		ProjectRevision:     run.ProjectRevision,
		AgentName:           run.AgentName,
		AgentID:             run.AgentID,
		Driver:              strings.TrimSpace(spec.Driver),
		ImageRef:            strings.TrimSpace(spec.GuestImage),
		EnvItems:            domain.NormalizeEnvItems(prepared.EnvItems),
		CapsetIDs:           capsetIDs,
		Workspace:           workspaces.WorkspaceForConfigurationHash(prepared.Workspace),
		VolumeMounts:        volumeMounts,
		Jupyter:             stickyProjectSandboxOptionsFrom(spec.Jupyter),
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// stickyBindingKey identifies the scheduler binding a sticky sandbox is
// being resolved or claimed against.
type stickyBindingKey struct {
	SchedulerID string
	TriggerID   string
	ConfigHash  string
}

func (c *Controller) resolveStickySchedulerBinding(ctx context.Context, store stickyBindingStore, key stickyBindingKey) (string, *domain.SchedulerBinding, []string, error) {
	schedulerID, triggerID, configHash := key.SchedulerID, key.TriggerID, key.ConfigHash
	for range 3 {
		binding, found, err := store.GetSchedulerBinding(ctx, schedulerID, triggerID)
		if err != nil {
			return "", nil, nil, fmt.Errorf("load sticky sandbox binding: %w", err)
		}
		if !found {
			return "", nil, nil, nil
		}
		if pendingRunID, pending, err := c.pendingCompletionForSandbox(ctx, binding.SandboxID); err != nil {
			return "", &binding, nil, err
		} else if pending {
			return "", &binding, nil, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("sandbox %s has pending completion for run %s", binding.SandboxID, pendingRunID), nil)
		}
		retiringHash, retiring := schedulers.RetiringSchedulerBindingConfigHash(binding)
		if retiring && retiringHash == configHash {
			return "", &binding, nil, nil
		}
		if !retiring {
			binding, current, err := schedulers.ClaimLegacySchedulerBindingConfigHash(ctx, store, binding, configHash)
			if err != nil {
				return "", &binding, nil, fmt.Errorf("adopt legacy sticky sandbox configuration: %w", err)
			}
			if !current {
				continue
			}
			if configHash == "" || binding.SandboxConfigHash == configHash {
				return binding.SandboxID, &binding, nil, nil
			}
		}

		retiringBinding := schedulers.RetiringSchedulerBinding(binding, configHash)
		claimed, err := store.CompareAndSwapSchedulerBinding(ctx, &binding, retiringBinding)
		if err != nil {
			return "", &binding, nil, fmt.Errorf("claim stale sticky sandbox %s retirement: %w", binding.SandboxID, err)
		}
		if !claimed {
			continue
		}

		unlock := c.lifecycleLocks.Lock(binding.SandboxID)
		sandbox, err := c.store.GetSandbox(ctx, binding.SandboxID)
		if err == nil {
			err = c.stopProjectRunSandboxLocked(ctx, sandbox)
		}
		unlock()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", &retiringBinding, []string{fmt.Sprintf("stale sticky sandbox %s is unavailable; creating a replacement", binding.SandboxID)}, nil
			}
			return "", &retiringBinding, nil, fmt.Errorf("retire stale sticky sandbox %s: %w", binding.SandboxID, err)
		}
		return "", &retiringBinding, []string{fmt.Sprintf("sticky sandbox %s used stale scheduler configuration; created a replacement", binding.SandboxID)}, nil
	}
	return "", nil, nil, fmt.Errorf("sticky sandbox binding changed concurrently")
}

func loadCompatibleStickySchedulerBinding(ctx context.Context, store stickyBindingStore, key stickyBindingKey) (domain.SchedulerBinding, bool, error) {
	schedulerID, triggerID, configHash := key.SchedulerID, key.TriggerID, key.ConfigHash
	for range 3 {
		binding, found, err := store.GetSchedulerBinding(ctx, schedulerID, triggerID)
		if err != nil || !found {
			return domain.SchedulerBinding{}, false, err
		}
		if _, retiring := schedulers.RetiringSchedulerBindingConfigHash(binding); retiring {
			return domain.SchedulerBinding{}, false, nil
		}
		binding, current, err := schedulers.ClaimLegacySchedulerBindingConfigHash(ctx, store, binding, configHash)
		if err != nil {
			return domain.SchedulerBinding{}, false, err
		}
		if !current {
			continue
		}
		return binding, binding.SandboxConfigHash == configHash, nil
	}
	return domain.SchedulerBinding{}, false, fmt.Errorf("sticky sandbox binding changed concurrently")
}
