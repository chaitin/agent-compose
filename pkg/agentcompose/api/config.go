package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/workspaces"
)

var errWorkspaceContent = errors.New("workspace content operation failed")

func workspaceContentError(err error) error {
	return fmt.Errorf("%w: %v", errWorkspaceContent, err)
}

type ConfigStore interface {
	ListGlobalEnv(ctx context.Context) ([]domain.SandboxEnvVar, error)
	ReplaceGlobalEnv(ctx context.Context, items []domain.SandboxEnvVar) ([]domain.SandboxEnvVar, error)
	ListWorkspaceConfigs(ctx context.Context) ([]domain.WorkspaceConfig, error)
	GetWorkspaceConfig(ctx context.Context, id string) (domain.WorkspaceConfig, error)
	CreateWorkspaceConfig(ctx context.Context, item domain.WorkspaceConfig) (domain.WorkspaceConfig, error)
	UpdateWorkspaceConfig(ctx context.Context, item domain.WorkspaceConfig) (domain.WorkspaceConfig, error)
	DeleteWorkspaceConfig(ctx context.Context, id string) error
	GetCapabilityGateway(ctx context.Context) (domain.CapabilityGatewaySettings, error)
	SaveCapabilityGateway(ctx context.Context, settings domain.CapabilityGatewaySettings) (domain.CapabilityGatewaySettings, error)
}

type workspaceSettings struct {
	config *appconfig.Config
	store  ConfigStore
}

func newWorkspaceSettings(config *appconfig.Config, store ConfigStore) *workspaceSettings {
	return &workspaceSettings{config: config, store: store}
}

func (h *workspaceSettings) createWorkspaceConfig(ctx context.Context, item domain.WorkspaceConfig) (domain.WorkspaceConfig, error) {
	configJSON := strings.TrimSpace(item.ConfigJSON)
	workspaceType := strings.ToLower(strings.TrimSpace(item.Type))
	workspaceID := ""
	if workspaceType == "file" {
		workspaceID = uuid.NewString()
		configJSON = workspaces.DefaultFileConfigJSON(h.config, workspaceID)
		if _, err := workspaces.ValidateFileWorkspaceConfig(h.config, workspaceID, configJSON); err != nil {
			return domain.WorkspaceConfig{}, err
		}
		if err := h.checkFileWorkspaceContentCreatable(workspaceID); err != nil {
			return domain.WorkspaceConfig{}, workspaceContentError(err)
		}
	}
	item, err := h.store.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       item.Name,
		Type:       workspaceType,
		ConfigJSON: configJSON,
		Comment:    item.Comment,
	})
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	if workspaceType == "file" {
		if err := h.createFileWorkspaceContent(item.ID, item.ConfigJSON); err != nil {
			deleteErr := h.store.DeleteWorkspaceConfig(ctx, item.ID)
			if deleteErr != nil {
				return domain.WorkspaceConfig{}, workspaceContentError(fmt.Errorf("create file workspace content: %w; rollback workspace config: %v", err, deleteErr))
			}
			return domain.WorkspaceConfig{}, workspaceContentError(err)
		}
	}
	return item, nil
}

func (h *workspaceSettings) updateWorkspaceConfig(ctx context.Context, requested domain.WorkspaceConfig) (domain.WorkspaceConfig, error) {
	configJSON := strings.TrimSpace(requested.ConfigJSON)
	workspaceType := strings.ToLower(strings.TrimSpace(requested.Type))
	previous, err := h.store.GetWorkspaceConfig(ctx, requested.ID)
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	if workspaceType == "file" {
		configJSON = workspaces.DefaultFileConfigJSON(h.config, requested.ID)
		if _, err := workspaces.ValidateFileWorkspaceConfig(h.config, requested.ID, configJSON); err != nil {
			return domain.WorkspaceConfig{}, err
		}
	}
	wasFile := strings.EqualFold(strings.TrimSpace(previous.Type), "file")
	if workspaceType == "file" {
		if err := h.checkFileWorkspaceContentCreatable(requested.ID); err != nil {
			return domain.WorkspaceConfig{}, workspaceContentError(err)
		}
	} else if wasFile {
		if err := h.checkFileWorkspaceContentRemovable(previous); err != nil {
			return domain.WorkspaceConfig{}, workspaceContentError(err)
		}
	}
	item, err := h.store.UpdateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         requested.ID,
		Name:       requested.Name,
		Type:       workspaceType,
		ConfigJSON: configJSON,
		Comment:    requested.Comment,
	})
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	if workspaceType == "file" {
		if err := h.createFileWorkspaceContent(item.ID, item.ConfigJSON); err != nil {
			_, rollbackErr := h.store.UpdateWorkspaceConfig(ctx, previous)
			if rollbackErr != nil {
				return domain.WorkspaceConfig{}, workspaceContentError(fmt.Errorf("create file workspace content: %w; rollback workspace config: %v", err, rollbackErr))
			}
			return domain.WorkspaceConfig{}, workspaceContentError(err)
		}
	} else if wasFile {
		if err := h.removeFileWorkspaceContent(previous); err != nil {
			_, rollbackErr := h.store.UpdateWorkspaceConfig(ctx, previous)
			if rollbackErr != nil {
				return domain.WorkspaceConfig{}, workspaceContentError(fmt.Errorf("remove file workspace content: %w; rollback workspace config: %v", err, rollbackErr))
			}
			return domain.WorkspaceConfig{}, workspaceContentError(err)
		}
	}
	return item, nil
}

func (h *workspaceSettings) deleteWorkspaceConfig(ctx context.Context, workspaceID string) error {
	workspace, err := h.store.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(workspace.Type), "file") {
		if err := h.checkFileWorkspaceContentRemovable(workspace); err != nil {
			return workspaceContentError(err)
		}
	}
	if err := h.store.DeleteWorkspaceConfig(ctx, workspaceID); err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(workspace.Type), "file") {
		if err := h.removeFileWorkspaceContent(workspace); err != nil {
			_, rollbackErr := h.store.CreateWorkspaceConfig(ctx, workspace)
			if rollbackErr != nil {
				return workspaceContentError(fmt.Errorf("remove file workspace content: %w; rollback workspace config: %v", err, rollbackErr))
			}
			return workspaceContentError(err)
		}
	}
	return nil
}

func (h *workspaceSettings) createFileWorkspaceContent(workspaceID, configJSON string) error {
	content, err := workspaces.OpenFileWorkspaceContent(h.config, domain.WorkspaceConfig{
		ID:         workspaceID,
		Type:       "file",
		ConfigJSON: configJSON,
	})
	if err != nil {
		return err
	}
	return content.Root.Close()
}

func (h *workspaceSettings) checkFileWorkspaceContentCreatable(workspaceID string) error {
	relRoot, err := workspaces.FileWorkspaceContentRelRoot(workspaceID)
	if err != nil {
		return err
	}
	dataRoot, err := workspaces.OpenFileWorkspaceDataRoot(h.config)
	if err != nil {
		return err
	}
	defer func() { _ = dataRoot.Close() }()
	for _, dir := range []string{"workspaces", filepath.ToSlash(filepath.Join("workspaces", strings.TrimSpace(workspaceID))), relRoot} {
		info, err := dataRoot.Lstat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("file workspace path %s is a symlink", dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("file workspace path %s is not a directory", dir)
		}
	}
	return nil
}

func (h *workspaceSettings) checkFileWorkspaceContentRemovable(workspace domain.WorkspaceConfig) error {
	dataRoot, _, err := h.fileWorkspaceContentRemovalTarget(workspace)
	if err != nil {
		return err
	}
	return dataRoot.Close()
}

func (h *workspaceSettings) removeFileWorkspaceContent(workspace domain.WorkspaceConfig) error {
	dataRoot, relRoot, err := h.fileWorkspaceContentRemovalTarget(workspace)
	if err != nil {
		return err
	}
	defer func() { _ = dataRoot.Close() }()
	return dataRoot.RemoveAll(relRoot)
}

func (h *workspaceSettings) fileWorkspaceContentRemovalTarget(workspace domain.WorkspaceConfig) (*os.Root, string, error) {
	dataRoot, err := workspaces.OpenFileWorkspaceDataRoot(h.config)
	if err != nil {
		return nil, "", err
	}
	relRoot, err := workspaces.FileWorkspaceContentRelRoot(workspace.ID)
	if err != nil {
		_ = dataRoot.Close()
		return nil, "", err
	}
	info, err := dataRoot.Lstat(relRoot)
	if err != nil && !os.IsNotExist(err) {
		_ = dataRoot.Close()
		return nil, "", err
	}
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		_ = dataRoot.Close()
		return nil, "", fmt.Errorf("file workspace content root %s is a symlink", relRoot)
	}
	return dataRoot, relRoot, nil
}
