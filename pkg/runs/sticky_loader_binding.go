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

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/sessionstore"
)

type stickyProjectRunSandboxConfig struct {
	LoaderConfigHash string                            `json:"loader_config_hash"`
	ProjectID        string                            `json:"project_id"`
	ProjectRevision  int64                             `json:"project_revision"`
	AgentName        string                            `json:"agent_name"`
	ManagedAgentID   string                            `json:"managed_agent_id"`
	Driver           string                            `json:"driver"`
	ImageRef         string                            `json:"image_ref"`
	EnvItems         []domain.SandboxEnvVar            `json:"env_items,omitempty"`
	CapsetIDs        []string                          `json:"capset_ids,omitempty"`
	Workspace        *domain.SandboxWorkspace          `json:"workspace,omitempty"`
	VolumeMounts     []domain.SandboxVolumeMount       `json:"volume_mounts,omitempty"`
	Jupyter          sessionstore.CreateSandboxOptions `json:"jupyter"`
}

func stickyProjectRunConfigHash(baseHash string, run domain.ProjectRunRecord, prepared Preparation, driver, guestImage string, volumeMounts []domain.SandboxVolumeMount, jupyter sessionstore.CreateSandboxOptions) (string, error) {
	baseHash = strings.TrimSpace(baseHash)
	if baseHash == "" {
		return "", nil
	}
	capsetIDs := capabilities.NormalizeCapsetIDs(prepared.CapsetIDs)
	sort.Strings(capsetIDs)
	volumeMounts = loaders.NormalizeStickySandboxVolumeMounts(volumeMounts)
	jupyter.VolumeMounts = loaders.NormalizeStickySandboxVolumeMounts(jupyter.VolumeMounts)
	payload, err := json.Marshal(stickyProjectRunSandboxConfig{
		LoaderConfigHash: baseHash,
		ProjectID:        run.ProjectID,
		ProjectRevision:  run.ProjectRevision,
		AgentName:        run.AgentName,
		ManagedAgentID:   run.ManagedAgentID,
		Driver:           strings.TrimSpace(driver),
		ImageRef:         strings.TrimSpace(guestImage),
		EnvItems:         domain.NormalizeEnvItems(prepared.EnvItems),
		CapsetIDs:        capsetIDs,
		Workspace:        prepared.Workspace,
		VolumeMounts:     volumeMounts,
		Jupyter:          jupyter,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (c *Controller) resolveStickyLoaderBinding(ctx context.Context, store stickyBindingStore, loaderID, triggerID, configHash string) (string, *domain.LoaderBinding, []string, error) {
	for range 3 {
		binding, found, err := store.GetLoaderBinding(ctx, loaderID, triggerID)
		if err != nil {
			return "", nil, nil, fmt.Errorf("load sticky sandbox binding: %w", err)
		}
		if !found {
			return "", nil, nil, nil
		}
		retiringHash, retiring := loaders.RetiringLoaderBindingConfigHash(binding)
		if retiring && retiringHash == configHash {
			return "", &binding, nil, nil
		}
		if !retiring {
			binding, current, err := claimLegacyStickyLoaderBindingConfigHash(ctx, store, binding, configHash)
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

		retiringBinding := loaders.RetiringLoaderBinding(binding, configHash)
		claimed, err := store.CompareAndSwapLoaderBinding(ctx, &binding, retiringBinding)
		if err != nil {
			return "", &binding, nil, fmt.Errorf("claim stale sticky sandbox %s retirement: %w", binding.SandboxID, err)
		}
		if !claimed {
			continue
		}

		unlock := c.lifecycleLocks.Lock(binding.SandboxID)
		sandbox, err := c.store.GetSandbox(ctx, binding.SandboxID)
		if err == nil {
			err = c.stopProjectRunSandbox(ctx, sandbox)
		}
		unlock()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", &retiringBinding, []string{fmt.Sprintf("stale sticky sandbox %s is unavailable; creating a replacement", binding.SandboxID)}, nil
			}
			return "", &retiringBinding, nil, fmt.Errorf("retire stale sticky sandbox %s: %w", binding.SandboxID, err)
		}
		return "", &retiringBinding, []string{fmt.Sprintf("sticky sandbox %s used stale loader configuration; created a replacement", binding.SandboxID)}, nil
	}
	return "", nil, nil, fmt.Errorf("sticky sandbox binding changed concurrently")
}

func claimLegacyStickyLoaderBindingConfigHash(ctx context.Context, store stickyBindingStore, binding domain.LoaderBinding, configHash string) (domain.LoaderBinding, bool, error) {
	replacement, legacy := loaders.AdoptLegacyLoaderBindingConfigHash(binding, configHash)
	if !legacy {
		return binding, true, nil
	}
	claimed, err := store.CompareAndSwapLoaderBinding(ctx, &binding, replacement)
	if err != nil {
		return binding, false, err
	}
	return replacement, claimed, nil
}

func loadCompatibleStickyLoaderBinding(ctx context.Context, store stickyBindingStore, loaderID, triggerID, configHash string) (domain.LoaderBinding, bool, error) {
	for range 3 {
		binding, found, err := store.GetLoaderBinding(ctx, loaderID, triggerID)
		if err != nil || !found {
			return domain.LoaderBinding{}, false, err
		}
		if _, retiring := loaders.RetiringLoaderBindingConfigHash(binding); retiring {
			return domain.LoaderBinding{}, false, nil
		}
		binding, current, err := claimLegacyStickyLoaderBindingConfigHash(ctx, store, binding, configHash)
		if err != nil {
			return domain.LoaderBinding{}, false, err
		}
		if !current {
			continue
		}
		return binding, binding.SandboxConfigHash == configHash, nil
	}
	return domain.LoaderBinding{}, false, fmt.Errorf("sticky sandbox binding changed concurrently")
}
