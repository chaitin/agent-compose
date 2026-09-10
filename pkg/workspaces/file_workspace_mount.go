package workspaces

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// FileWorkspaceMountConfig records an external directory without transferring
// ownership to the sandbox. These fields are internal snapshots, not preset roots.
type FileWorkspaceMountConfig struct {
	Mode        string `json:"mode"`
	SourcePath  string `json:"source_path"`
	ProjectRoot string `json:"project_root"`
	Target      string `json:"target"`
	ReadOnly    bool   `json:"read_only,omitempty"`
}

// NewFileWorkspaceMountConfig resolves a project-relative source once, before a
// sandbox is created, and never creates or copies source content.
func NewFileWorkspaceMountConfig(project domain.ProjectRecord, rawPath, target string, readOnly bool) (string, error) {
	sourcePath := strings.TrimSpace(project.SourcePath)
	if sourcePath == "" {
		return "", fmt.Errorf("%w: local workspace requires project source path", domain.ErrRequired)
	}
	projectRoot, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve project source path: %w", err)
	}
	info, err := os.Stat(projectRoot)
	if err != nil {
		return "", fmt.Errorf("inspect project source path: %w", err)
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: project source must be a directory or regular configuration file", domain.ErrInvalidArgument)
		}
		projectRoot = filepath.Dir(projectRoot)
	}
	projectRoot, err = filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace project root: %w", err)
	}
	relative, err := CleanRelativePath(rawPath, true)
	if err != nil || strings.TrimSpace(rawPath) == "" {
		return "", fmt.Errorf("%w: workspace source must be a non-empty project-relative directory", domain.ErrInvalidArgument)
	}
	source := filepath.Join(projectRoot, relative)
	info, err = os.Lstat(source)
	if err != nil {
		return "", fmt.Errorf("inspect workspace mount source: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: workspace mount source must be a real directory", domain.ErrInvalidArgument)
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", fmt.Errorf("resolve workspace mount source: %w", err)
	}
	target, err = NormalizeWorkspaceTarget("", target)
	if err != nil {
		return "", fmt.Errorf("%w: %w", domain.ErrInvalidArgument, err)
	}
	config := FileWorkspaceMountConfig{
		Mode: domain.WorkspaceModeMount, SourcePath: source,
		ProjectRoot: projectRoot, Target: target, ReadOnly: readOnly,
	}
	if err := ValidateFileWorkspaceMount(config); err != nil {
		return "", err
	}
	payload, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode workspace mount: %w", err)
	}
	return string(payload), nil
}

// DecodeFileWorkspaceMount returns nil for historical copy configurations. An
// invalid or incomplete mount must never silently fall back to a managed copy.
func DecodeFileWorkspaceMount(raw string) (*FileWorkspaceMountConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var stored struct {
		FileWorkspaceMountConfig
		Root string `json:"root"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil, fmt.Errorf("decode workspace delivery: %w", err)
	}
	mode, err := domain.NormalizeWorkspaceDelivery("file", stored.Mode, stored.ReadOnly)
	if err != nil {
		return nil, err
	}
	if mode == "" {
		if stored.SourcePath != "" || stored.ProjectRoot != "" {
			return nil, fmt.Errorf("%w: external workspace source requires mount mode", domain.ErrInvalidArgument)
		}
		return nil, nil
	}
	if stored.Root != "" {
		return nil, fmt.Errorf("%w: mounted workspace cannot have a managed root", domain.ErrInvalidArgument)
	}
	config := stored.FileWorkspaceMountConfig
	config.Mode = mode
	if err := validateFileWorkspaceMountPaths(config); err != nil {
		return nil, err
	}
	config.Target, err = NormalizeWorkspaceTarget("", config.Target)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrInvalidArgument, err)
	}
	return &config, nil
}

func validateFileWorkspaceMountPaths(config FileWorkspaceMountConfig) error {
	for _, path := range []string{config.ProjectRoot, config.SourcePath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("%w: workspace mount paths must be clean absolute paths", domain.ErrInvalidArgument)
		}
	}
	relative, err := filepath.Rel(config.ProjectRoot, config.SourcePath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("%w: workspace mount source is outside its project root", domain.ErrInvalidArgument)
	}
	return nil
}

// ValidateFileWorkspaceMount checks current source availability, including when
// resuming a previously ready sandbox. It must not repair missing directories.
func ValidateFileWorkspaceMount(config FileWorkspaceMountConfig) error {
	if err := validateFileWorkspaceMountPaths(config); err != nil {
		return err
	}
	if _, err := NormalizeWorkspaceTarget("", config.Target); err != nil {
		return fmt.Errorf("%w: %w", domain.ErrInvalidArgument, err)
	}
	for _, path := range []string{config.ProjectRoot, config.SourcePath} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve workspace mount directory %s: %w", path, err)
		}
		if resolved != path {
			return fmt.Errorf("%w: workspace mount directory %s changed to a symlink", domain.ErrInvalidArgument, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("inspect workspace mount directory %s: %w", path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: workspace mount source %s is not a directory", domain.ErrInvalidArgument, path)
		}
	}
	return nil
}

// ValidateWorkspaceRuntimeDriver checks resolved capabilities, including runtime
// overrides and snapshots restored without going through compose normalization.
func ValidateWorkspaceRuntimeDriver(workspace *domain.SandboxWorkspace, driver string) error {
	if workspace == nil {
		return nil
	}
	config, err := DecodeFileWorkspaceMount(workspace.ConfigJSON)
	if err != nil {
		return err
	}
	if config == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(workspace.Type), "file") {
		return fmt.Errorf("%w: workspace mount mode requires provider file", domain.ErrInvalidArgument)
	}
	if driver = strings.ToLower(strings.TrimSpace(driver)); driver != "" && driver != "docker" {
		return fmt.Errorf("%w: workspace mount mode only supports the docker driver", domain.ErrInvalidArgument)
	}
	return nil
}
