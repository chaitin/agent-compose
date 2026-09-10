package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/sources"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

// agentDefinitionInlineWorkspace decodes the yaml `workspace:` declaration
// that internal/projects.NewAgentDefinitionFromSpec embeds in ConfigJSON. It is
// nil for agents without a yaml-declared workspace and for workspaces
// managed as Settings presets (referenced only by AgentDefinition.WorkspaceID
// / SchedulerSummary.WorkspaceID). Compose normalization always resolves a
// yaml `workspace:` block to a spec with a non-empty Provider (either
// authored inline or copied from a project-level `workspaces:` entry), so a
// decoded spec without one is not a real inline declaration.
func agentDefinitionInlineWorkspace(definition *domain.AgentDefinition) *compose.WorkspaceSpec {
	if definition == nil {
		return nil
	}
	var config struct {
		Workspace *compose.WorkspaceSpec `json:"workspace"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(definition.ConfigJSON)), &config); err != nil {
		return nil
	}
	if config.Workspace == nil || strings.TrimSpace(config.Workspace.Provider) == "" {
		return nil
	}
	return config.Workspace
}

// inlineWorkspaceID derives a stable identifier for an agent's yaml-declared
// workspace. It intentionally does not depend on WorkspaceSpec.Name: that
// field is blank whenever the agent workspace references a project-level
// `workspaces:` entry (pkg/compose.resolveAgentWorkspace clears it), so the
// agent definition id is the only value guaranteed to be present and stable
// across scheduler runs sharing the same sandbox.
func inlineWorkspaceID(agentDefinition *domain.AgentDefinition, provider string) string {
	seed := strings.TrimSpace(agentDefinition.ID) + "|workspace|" + provider
	return projects.StableReadableID("workspace", strings.TrimSpace(agentDefinition.Name)+"-"+provider, seed)
}

// inlineWorkspaceSnapshot resolves a scheduler sandbox workspace snapshot
// directly from the agent's yaml `workspace:` spec, mirroring how project
// runs resolve AgentSpec.Workspace in pkg/runs.prepareProjectRunWorkspace.
//
// Before this existed, scheduler.shell/scheduler.exec (and scheduler.agent
// calls that pass a session-override argument) resolved workspaces by
// treating the yaml workspace name as a workspace_config preset id and
// querying the database for it. Project apply never creates such a preset,
// so that lookup always failed at run time (see issue #599). Building the
// snapshot from the inline spec instead keeps this path in sync with the
// project-run path without requiring project apply to write workspace_config
// rows.
func (r *SchedulerSandboxRunner) inlineWorkspaceSnapshot(ctx context.Context, agentDefinition *domain.AgentDefinition, spec *compose.WorkspaceSpec, driver string) (*domain.SandboxWorkspace, string, error) {
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	mode, err := domain.NormalizeWorkspaceDelivery(provider, spec.Mode, spec.ReadOnly)
	if err != nil {
		return nil, "", err
	}
	if mode == domain.WorkspaceModeMount && driver != "docker" {
		return nil, "", fmt.Errorf("%w: workspace mount mode requires the docker runtime driver, got %q", domain.ErrInvalidArgument, driver)
	}
	normalizedSpec := *spec
	normalizedSpec.Mode = mode
	spec = &normalizedSpec
	workspaceID := inlineWorkspaceID(agentDefinition, provider)
	switch provider {
	case sources.ProviderGit:
		config, err := inlineGitWorkspaceConfig(workspaceID, spec)
		if err != nil {
			return nil, "", err
		}
		return toSandboxWorkspaceSnapshot(config), workspaceID, nil
	case sources.ProviderFile:
		config, err := r.materializeInlineFileWorkspace(ctx, agentDefinition, workspaceID, spec)
		if err != nil {
			return nil, "", err
		}
		return toSandboxWorkspaceSnapshot(config), workspaceID, nil
	case "":
		return nil, "", fmt.Errorf("workspace provider is required")
	default:
		return nil, "", fmt.Errorf("unsupported workspace provider %q", spec.Provider)
	}
}

func inlineGitWorkspaceConfig(workspaceID string, spec *compose.WorkspaceSpec) (domain.WorkspaceConfig, error) {
	if strings.TrimSpace(spec.URL) == "" {
		return domain.WorkspaceConfig{}, fmt.Errorf("git workspace url is required")
	}
	if _, err := workspaces.NormalizeWorkspaceTarget(workspaceID, spec.Target); err != nil {
		return domain.WorkspaceConfig{}, err
	}
	payload, err := json.Marshal(workspaces.GitWorkspaceConfig{
		Source: spec.ContentSource(),
		Target: strings.TrimSpace(spec.Target),
	})
	if err != nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("encode git workspace config: %w", err)
	}
	return domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       firstNonEmpty(strings.TrimSpace(spec.Name), workspaceID),
		Type:       "git",
		ConfigJSON: string(payload),
		Comment:    "agent yaml workspace snapshot",
	}, nil
}

// materializeInlineFileWorkspace resets and repopulates the shared content
// directory for an agent's inline file workspace (keyed by the stable
// inlineWorkspaceID, not a per-run id, since scheduler sandboxes reuse and
// reference the same agent workspace across runs). Concurrent scheduler
// calls for the same agent (parallel triggers, or scheduler.shell/exec/agent
// racing each other) can reach Ensure at the same time, so the reset+copy is
// serialized per workspace id to prevent one call's CopyRootDirectoryContents
// from reading a directory another call is concurrently RemoveAll-ing,
// which would otherwise surface as intermittent ENOENT failures or leave the
// shared directory with interleaved content from two different callers.
func (r *SchedulerSandboxRunner) materializeInlineFileWorkspace(ctx context.Context, agentDefinition *domain.AgentDefinition, workspaceID string, spec *compose.WorkspaceSpec) (domain.WorkspaceConfig, error) {
	unlock := r.inlineWorkspaceLocks.Lock(workspaceID)
	defer unlock()
	projectID := strings.TrimSpace(agentDefinition.ProjectID)
	if projectID == "" {
		return domain.WorkspaceConfig{}, fmt.Errorf("file workspace requires a project-managed agent")
	}
	project, err := r.ConfigDB.GetProject(ctx, projectID)
	if err != nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("get agent project %s: %w", projectID, err)
	}
	if spec.Mode == domain.WorkspaceModeMount {
		configJSON, err := workspaces.NewFileWorkspaceMountConfig(project, spec.Path, spec.Target, spec.ReadOnly)
		if err != nil {
			return domain.WorkspaceConfig{}, err
		}
		return domain.WorkspaceConfig{
			ID: workspaceID, Name: firstNonEmpty(strings.TrimSpace(spec.Name), workspaceID), Type: "file", ConfigJSON: configJSON,
			Comment: "agent yaml workspace mount",
		}, nil
	}
	sourceDir, err := runs.ResolveLocalProjectWorkspacePath(project, spec.Path)
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	configJSON := workspaces.DefaultFileConfigJSON(r.Config, workspaceID)
	if _, err := workspaces.ValidateFileWorkspaceConfig(r.Config, workspaceID, configJSON); err != nil {
		return domain.WorkspaceConfig{}, err
	}
	if err := resetInlineFileWorkspaceContent(r.Config, workspaceID); err != nil {
		return domain.WorkspaceConfig{}, err
	}
	config := domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       firstNonEmpty(strings.TrimSpace(spec.Name), workspaceID),
		Type:       "file",
		ConfigJSON: configJSON,
		Comment:    "agent yaml workspace snapshot",
	}
	content, err := workspaces.OpenFileWorkspaceContent(r.Config, config)
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	defer func() { _ = content.Root.Close() }()
	sourceRoot, err := os.OpenRoot(sourceDir)
	if err != nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("open agent workspace source %s: %w", sourceDir, err)
	}
	defer func() { _ = sourceRoot.Close() }()
	target, err := workspaces.NormalizeWorkspaceTarget(workspaceID, spec.Target)
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	destination := content.AbsRoot
	if target != "." {
		destination = filepath.Join(content.AbsRoot, target)
		if err := os.MkdirAll(destination, 0o755); err != nil {
			return domain.WorkspaceConfig{}, fmt.Errorf("create agent workspace target %s: %w", target, err)
		}
	}
	if err := workspaces.CopyRootDirectoryContents(sourceRoot, destination); err != nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("materialize agent workspace snapshot: %w", err)
	}
	return config, nil
}

func resetInlineFileWorkspaceContent(config *appconfig.Config, workspaceID string) error {
	dataRoot, err := workspaces.OpenFileWorkspaceDataRoot(config)
	if err != nil {
		return err
	}
	defer func() { _ = dataRoot.Close() }()
	relRoot, err := workspaces.FileWorkspaceContentRelRoot(workspaceID)
	if err != nil {
		return err
	}
	if err := dataRoot.RemoveAll(relRoot); err != nil {
		return fmt.Errorf("reset agent workspace snapshot %s: %w", workspaceID, err)
	}
	return nil
}
