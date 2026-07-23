package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

func (r *LoaderSandboxRunner) reuseCompatibleLoaderBinding(ctx context.Context, loader domain.Loader, triggerID, configHash string) (*domain.Sandbox, string, bool, *domain.LoaderBinding, error) {
	for range 3 {
		binding, found, err := r.ConfigDB.GetLoaderBinding(ctx, loader.Summary.ID, triggerID)
		if err != nil || !found {
			return nil, "", false, nil, err
		}
		binding, current, err := r.claimLegacyLoaderBindingConfigHash(ctx, binding, configHash)
		if err != nil {
			return nil, "", false, &binding, err
		}
		if !current {
			continue
		}
		retiringHash, retiring := loaders.RetiringLoaderBindingConfigHash(binding)
		if !retiring && binding.SandboxConfigHash == configHash {
			session, eventType, current, err := r.loadOrResumeLoaderBinding(ctx, binding)
			if !current {
				continue
			}
			if err == nil {
				return session, eventType, true, &binding, nil
			}
			slog.Warn("failed to reuse loader sticky sandbox, creating a new one", "loader_id", loader.Summary.ID, "sandbox_id", binding.SandboxID, "error", err)
			replacement := loaders.RetiringLoaderBinding(binding, configHash)
			claimed, claimErr := r.ConfigDB.CompareAndSwapLoaderBinding(ctx, &binding, replacement)
			if claimErr != nil {
				return nil, "", false, &binding, claimErr
			}
			if !claimed {
				continue
			}
			if shutdownErr := r.Shutdown(ctx, binding.SandboxID); shutdownErr != nil && !errors.Is(shutdownErr, os.ErrNotExist) {
				return nil, "", false, &replacement, shutdownErr
			}
			return nil, "", false, &replacement, nil
		}

		if !retiring || retiringHash != configHash {
			replacement := loaders.RetiringLoaderBinding(binding, configHash)
			claimed, err := r.ConfigDB.CompareAndSwapLoaderBinding(ctx, &binding, replacement)
			if err != nil {
				return nil, "", false, &binding, err
			}
			if !claimed {
				continue
			}
			binding = replacement
		}
		if err := r.Shutdown(ctx, binding.SandboxID); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, "", false, &binding, err
		}
		slog.Info("retired loader sticky sandbox with stale configuration", "loader_id", loader.Summary.ID, "trigger_id", triggerID, "sandbox_id", binding.SandboxID)
		return nil, "", false, &binding, nil
	}
	return nil, "", false, nil, fmt.Errorf("loader sticky sandbox binding changed concurrently")
}

func (r *LoaderSandboxRunner) loadOrResumeLoaderBinding(ctx context.Context, binding domain.LoaderBinding) (*domain.Sandbox, string, bool, error) {
	unlock := r.LifecycleLocks.Lock(binding.SandboxID)
	defer unlock()
	current, found, err := r.ConfigDB.GetLoaderBinding(ctx, binding.LoaderID, binding.TriggerID)
	if err != nil {
		return nil, "", true, err
	}
	if !found || !loaders.LoaderBindingsMatch(current, binding) {
		return nil, "", false, nil
	}
	session, eventType, err := r.loadOrResumeLocked(ctx, binding.SandboxID)
	return session, eventType, true, err
}

func (r *LoaderSandboxRunner) bindLoaderSandbox(ctx context.Context, loader domain.Loader, triggerID, sandboxID, configHash string, expected *domain.LoaderBinding) (bool, error) {
	return r.ConfigDB.CompareAndSwapLoaderBinding(ctx, expected, domain.LoaderBinding{
		LoaderID:          loader.Summary.ID,
		TriggerID:         triggerID,
		SandboxID:         sandboxID,
		SandboxConfigHash: configHash,
	})
}

func (r *LoaderSandboxRunner) claimLegacyLoaderBindingConfigHash(ctx context.Context, binding domain.LoaderBinding, configHash string) (domain.LoaderBinding, bool, error) {
	replacement, legacy := loaders.AdoptLegacyLoaderBindingConfigHash(binding, configHash)
	if !legacy {
		return binding, true, nil
	}
	claimed, err := r.ConfigDB.CompareAndSwapLoaderBinding(ctx, &binding, replacement)
	if err != nil {
		return binding, false, err
	}
	return replacement, claimed, nil
}

func (r *LoaderSandboxRunner) reuseWinningLoaderBinding(ctx context.Context, loaderID, triggerID, configHash string) (*domain.Sandbox, string, bool, error) {
	for range 3 {
		binding, found, err := r.ConfigDB.GetLoaderBinding(ctx, loaderID, triggerID)
		if err != nil || !found {
			return nil, "", false, err
		}
		binding, current, err := r.claimLegacyLoaderBindingConfigHash(ctx, binding, configHash)
		if err != nil {
			return nil, "", false, err
		}
		if !current {
			continue
		}
		if _, retiring := loaders.RetiringLoaderBindingConfigHash(binding); retiring || binding.SandboxConfigHash != configHash {
			return nil, "", false, nil
		}
		session, eventType, current, err := r.loadOrResumeLoaderBinding(ctx, binding)
		if err != nil || !current {
			return nil, "", false, err
		}
		return session, eventType, true, nil
	}
	return nil, "", false, fmt.Errorf("loader sticky sandbox binding changed concurrently")
}

func loaderSandboxConfigHash(loader domain.Loader) (string, error) {
	return loaders.LoaderSandboxConfigHash(loader)
}

type loaderEffectiveSandboxConfig struct {
	LoaderConfigHash string                      `json:"loader_config_hash"`
	Agent            string                      `json:"agent,omitempty"`
	AgentDefinition  *loaderAgentSandboxConfig   `json:"agent_definition,omitempty"`
	SandboxPolicy    string                      `json:"sandbox_policy,omitempty"`
	PullPolicy       string                      `json:"pull_policy,omitempty"`
	JupyterEnabled   bool                        `json:"jupyter_enabled,omitempty"`
	ProviderEnvItems []domain.SandboxEnvVar      `json:"provider_env_items,omitempty"`
	EnvItems         []domain.SandboxEnvVar      `json:"env_items,omitempty"`
	Workspace        *domain.SandboxWorkspace    `json:"workspace,omitempty"`
	Driver           string                      `json:"driver"`
	GuestImage       string                      `json:"guest_image"`
	VolumeMounts     []domain.SandboxVolumeMount `json:"volume_mounts,omitempty"`
}

type loaderAgentSandboxConfig struct {
	ID                     string                   `json:"id"`
	Provider               string                   `json:"provider"`
	Model                  string                   `json:"model,omitempty"`
	SystemPrompt           string                   `json:"system_prompt,omitempty"`
	Driver                 string                   `json:"driver,omitempty"`
	GuestImage             string                   `json:"guest_image,omitempty"`
	WorkspaceID            string                   `json:"workspace_id,omitempty"`
	EnvItems               []domain.SandboxEnvVar   `json:"env_items,omitempty"`
	Volumes                []domain.VolumeMountSpec `json:"volumes,omitempty"`
	ConfigJSON             string                   `json:"config_json"`
	CapsetIDs              []string                 `json:"capset_ids,omitempty"`
	Skills                 []domain.AgentSkill      `json:"skills,omitempty"`
	ManagedProjectID       string                   `json:"managed_project_id,omitempty"`
	ManagedProjectRevision int64                    `json:"managed_project_revision,omitempty"`
	ManagedAgentName       string                   `json:"managed_agent_name,omitempty"`
}

func loaderRequestSandboxConfigHash(baseHash string, request domain.LoaderAgentRequest, agentDefinition *domain.AgentDefinition, providerEnvItems, envItems []domain.SandboxEnvVar, workspace *domain.SandboxWorkspace, driver, guestImage string, volumeMounts []domain.SandboxVolumeMount) (string, error) {
	var agentConfig *loaderAgentSandboxConfig
	if agentDefinition != nil {
		current, err := domain.NormalizeAgentDefinition(*agentDefinition, true)
		if err != nil {
			return "", err
		}
		capsetIDs := append([]string(nil), current.CapsetIDs...)
		sort.Strings(capsetIDs)
		volumes := append([]domain.VolumeMountSpec(nil), current.Volumes...)
		sort.Slice(volumes, func(i, j int) bool { return volumes[i].Target < volumes[j].Target })
		agentConfig = &loaderAgentSandboxConfig{
			ID:                     current.ID,
			Provider:               current.Provider,
			Model:                  current.Model,
			SystemPrompt:           current.SystemPrompt,
			Driver:                 current.Driver,
			GuestImage:             current.GuestImage,
			WorkspaceID:            current.WorkspaceID,
			EnvItems:               current.EnvItems,
			Volumes:                volumes,
			ConfigJSON:             current.ConfigJSON,
			CapsetIDs:              capsetIDs,
			Skills:                 current.Skills,
			ManagedProjectID:       current.ManagedProjectID,
			ManagedProjectRevision: current.ManagedProjectRevision,
			ManagedAgentName:       current.ManagedAgentName,
		}
	}
	payload, err := json.Marshal(loaderEffectiveSandboxConfig{
		LoaderConfigHash: baseHash,
		Agent:            domain.NormalizeAgentKind(request.Agent),
		AgentDefinition:  agentConfig,
		SandboxPolicy:    strings.TrimSpace(domain.LoaderAgentSandboxPolicy(request)),
		PullPolicy:       strings.TrimSpace(request.PullPolicy),
		JupyterEnabled:   request.JupyterEnabled,
		ProviderEnvItems: domain.NormalizeEnvItems(providerEnvItems),
		EnvItems:         domain.NormalizeEnvItems(envItems),
		Workspace:        workspace,
		Driver:           driver,
		GuestImage:       guestImage,
		VolumeMounts:     loaders.NormalizeStickySandboxVolumeMounts(volumeMounts),
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
