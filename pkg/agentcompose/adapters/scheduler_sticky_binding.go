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

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/schedulers"
)

func (r *SchedulerSandboxRunner) reuseCompatibleSchedulerBinding(ctx context.Context, scheduler domain.Scheduler, triggerID, configHash string) (*domain.Sandbox, string, bool, *domain.SchedulerBinding, error) {
	for range 3 {
		binding, found, err := r.ConfigDB.GetSchedulerBinding(ctx, scheduler.Summary.ID, triggerID)
		if err != nil || !found {
			return nil, "", false, nil, err
		}
		binding, current, err := r.claimLegacySchedulerBindingConfigHash(ctx, binding, configHash)
		if err != nil {
			return nil, "", false, &binding, err
		}
		if !current {
			continue
		}
		retiringHash, retiring := schedulers.RetiringSchedulerBindingConfigHash(binding)
		if !retiring && binding.SandboxConfigHash == configHash {
			session, eventType, current, err := r.loadOrResumeSchedulerBinding(ctx, binding)
			if !current {
				continue
			}
			if err == nil {
				return session, eventType, true, &binding, nil
			}
			slog.Warn("failed to reuse scheduler sticky sandbox, creating a new one", "scheduler_id", scheduler.Summary.ID, "sandbox_id", binding.SandboxID, "error", err)
			replacement := schedulers.RetiringSchedulerBinding(binding, configHash)
			claimed, claimErr := r.ConfigDB.CompareAndSwapSchedulerBinding(ctx, &binding, replacement)
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
			replacement := schedulers.RetiringSchedulerBinding(binding, configHash)
			claimed, err := r.ConfigDB.CompareAndSwapSchedulerBinding(ctx, &binding, replacement)
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
		slog.Info("retired scheduler sticky sandbox with stale configuration", "scheduler_id", scheduler.Summary.ID, "trigger_id", triggerID, "sandbox_id", binding.SandboxID)
		return nil, "", false, &binding, nil
	}
	return nil, "", false, nil, fmt.Errorf("scheduler sticky sandbox binding changed concurrently")
}

func (r *SchedulerSandboxRunner) loadOrResumeSchedulerBinding(ctx context.Context, binding domain.SchedulerBinding) (*domain.Sandbox, string, bool, error) {
	unlock := r.LifecycleLocks.Lock(binding.SandboxID)
	defer unlock()
	current, found, err := r.ConfigDB.GetSchedulerBinding(ctx, binding.SchedulerID, binding.TriggerID)
	if err != nil {
		return nil, "", true, err
	}
	if !found || !schedulers.SchedulerBindingsMatch(current, binding) {
		return nil, "", false, nil
	}
	session, eventType, err := r.loadOrResumeLocked(ctx, binding.SandboxID)
	return session, eventType, true, err
}

func (r *SchedulerSandboxRunner) bindSchedulerSandbox(ctx context.Context, scheduler domain.Scheduler, triggerID, sandboxID, configHash string, expected *domain.SchedulerBinding) (bool, error) {
	return r.ConfigDB.CompareAndSwapSchedulerBinding(ctx, expected, domain.SchedulerBinding{
		SchedulerID:       scheduler.Summary.ID,
		TriggerID:         triggerID,
		SandboxID:         sandboxID,
		SandboxConfigHash: configHash,
	})
}

func (r *SchedulerSandboxRunner) claimLegacySchedulerBindingConfigHash(ctx context.Context, binding domain.SchedulerBinding, configHash string) (domain.SchedulerBinding, bool, error) {
	replacement, legacy := schedulers.AdoptLegacySchedulerBindingConfigHash(binding, configHash)
	if !legacy {
		return binding, true, nil
	}
	claimed, err := r.ConfigDB.CompareAndSwapSchedulerBinding(ctx, &binding, replacement)
	if err != nil {
		return binding, false, err
	}
	return replacement, claimed, nil
}

func (r *SchedulerSandboxRunner) reuseWinningSchedulerBinding(ctx context.Context, schedulerID, triggerID, configHash string) (*domain.Sandbox, string, bool, error) {
	for range 3 {
		binding, found, err := r.ConfigDB.GetSchedulerBinding(ctx, schedulerID, triggerID)
		if err != nil || !found {
			return nil, "", false, err
		}
		binding, current, err := r.claimLegacySchedulerBindingConfigHash(ctx, binding, configHash)
		if err != nil {
			return nil, "", false, err
		}
		if !current {
			continue
		}
		if _, retiring := schedulers.RetiringSchedulerBindingConfigHash(binding); retiring || binding.SandboxConfigHash != configHash {
			return nil, "", false, nil
		}
		session, eventType, current, err := r.loadOrResumeSchedulerBinding(ctx, binding)
		if err != nil || !current {
			return nil, "", false, err
		}
		return session, eventType, true, nil
	}
	return nil, "", false, fmt.Errorf("scheduler sticky sandbox binding changed concurrently")
}

func schedulerSandboxConfigHash(scheduler domain.Scheduler) (string, error) {
	return schedulers.SchedulerSandboxConfigHash(scheduler)
}

type schedulerEffectiveSandboxConfig struct {
	// loader_config_hash is part of the canonical JSON used to calculate the
	// sticky sandbox hash. Renaming it would invalidate every existing binding.
	SchedulerConfigHash string                       `json:"loader_config_hash"`
	Agent               string                       `json:"agent,omitempty"`
	AgentDefinition     *schedulerAgentSandboxConfig `json:"agent_definition,omitempty"`
	SandboxPolicy       string                       `json:"sandbox_policy,omitempty"`
	PullPolicy          string                       `json:"pull_policy,omitempty"`
	JupyterEnabled      bool                         `json:"jupyter_enabled,omitempty"`
	ProviderEnvItems    []domain.SandboxEnvVar       `json:"provider_env_items,omitempty"`
	EnvItems            []domain.SandboxEnvVar       `json:"env_items,omitempty"`
	Workspace           *domain.SandboxWorkspace     `json:"workspace,omitempty"`
	Driver              string                       `json:"driver"`
	GuestImage          string                       `json:"guest_image"`
	VolumeMounts        []domain.SandboxVolumeMount  `json:"volume_mounts,omitempty"`
}

type schedulerAgentSandboxConfig struct {
	ID           string                   `json:"id"`
	Provider     string                   `json:"provider"`
	Model        string                   `json:"model,omitempty"`
	SystemPrompt string                   `json:"system_prompt,omitempty"`
	Driver       string                   `json:"driver,omitempty"`
	GuestImage   string                   `json:"guest_image,omitempty"`
	WorkspaceID  string                   `json:"workspace_id,omitempty"`
	EnvItems     []domain.SandboxEnvVar   `json:"env_items,omitempty"`
	Volumes      []domain.VolumeMountSpec `json:"volumes,omitempty"`
	ConfigJSON   string                   `json:"config_json"`
	CapsetIDs    []string                 `json:"capset_ids,omitempty"`
	Skills       []domain.AgentSkill      `json:"skills,omitempty"`
	ProjectID    string                   `json:"managed_project_id,omitempty"`
	AgentName    string                   `json:"managed_agent_name,omitempty"`
}

func schedulerRequestSandboxConfigHash(baseHash string, request domain.SchedulerAgentRequest, agentDefinition *domain.AgentDefinition, providerEnvItems, envItems []domain.SandboxEnvVar, workspace *domain.SandboxWorkspace, driver, guestImage string, volumeMounts []domain.SandboxVolumeMount) (string, error) {
	var agentConfig *schedulerAgentSandboxConfig
	if agentDefinition != nil {
		current, err := domain.NormalizeAgentDefinition(*agentDefinition, true)
		if err != nil {
			return "", err
		}
		capsetIDs := append([]string(nil), current.CapsetIDs...)
		sort.Strings(capsetIDs)
		volumes := append([]domain.VolumeMountSpec(nil), current.Volumes...)
		sort.Slice(volumes, func(i, j int) bool { return volumes[i].Target < volumes[j].Target })
		agentConfig = &schedulerAgentSandboxConfig{
			ID:           current.ID,
			Provider:     current.Provider,
			Model:        current.Model,
			SystemPrompt: current.SystemPrompt,
			Driver:       current.Driver,
			GuestImage:   current.GuestImage,
			WorkspaceID:  current.WorkspaceID,
			EnvItems:     current.EnvItems,
			Volumes:      volumes,
			ConfigJSON:   current.ConfigJSON,
			CapsetIDs:    capsetIDs,
			Skills:       current.Skills,
			ProjectID:    current.ProjectID,
			AgentName:    current.AgentName,
		}
	}
	payload, err := json.Marshal(schedulerEffectiveSandboxConfig{
		SchedulerConfigHash: baseHash,
		Agent:               domain.NormalizeAgentKind(request.Agent),
		AgentDefinition:     agentConfig,
		SandboxPolicy:       strings.TrimSpace(domain.SchedulerAgentSandboxPolicy(request)),
		PullPolicy:          strings.TrimSpace(request.PullPolicy),
		JupyterEnabled:      request.JupyterEnabled,
		ProviderEnvItems:    domain.NormalizeEnvItems(providerEnvItems),
		EnvItems:            domain.NormalizeEnvItems(envItems),
		Workspace:           workspace,
		Driver:              driver,
		GuestImage:          guestImage,
		VolumeMounts:        schedulers.NormalizeStickySandboxVolumeMounts(volumeMounts),
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
