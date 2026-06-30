package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var manifestFileNames = []string{
	"agent-compose.yml",
	"agent-compose.yaml",
	"agent-compose.json",
}

type BundleInspect struct {
	Dir          string                 `json:"dir"`
	Manifest     string                 `json:"manifest"`
	Project      string                 `json:"project"`
	AgentCount   int                    `json:"agent_count"`
	ServiceCount int                    `json:"service_count"`
	TriggerCount int                    `json:"trigger_count"`
	Spec         *NormalizedProjectSpec `json:"spec,omitempty"`
}

func FindBundleManifest(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve bundle dir %q: %w", dir, err)
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return "", fmt.Errorf("stat bundle dir %s: %w", absDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("bundle path %s is not a directory", absDir)
	}
	for _, name := range manifestFileNames {
		path := filepath.Join(absDir, name)
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() {
			return path, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("stat manifest %s: %w", path, err)
		}
	}
	return "", fmt.Errorf("bundle dir %s does not contain agent-compose.yml, agent-compose.yaml, or agent-compose.json", absDir)
}

func InspectBundle(dir string) (*BundleInspect, error) {
	manifest, err := FindBundleManifest(dir)
	if err != nil {
		return nil, err
	}
	normalized, err := NormalizeFile(manifest)
	if err != nil {
		return nil, err
	}
	return NewBundleInspect(manifest, normalized)
}

func NewBundleInspect(manifest string, normalized *NormalizedProjectSpec) (*BundleInspect, error) {
	if normalized == nil {
		return nil, &ValidationError{Message: "normalized spec is required"}
	}
	absManifest, err := filepath.Abs(manifest)
	if err != nil {
		return nil, fmt.Errorf("resolve manifest %q: %w", manifest, err)
	}
	return &BundleInspect{
		Dir:          filepath.Dir(absManifest),
		Manifest:     absManifest,
		Project:      normalized.Name,
		AgentCount:   len(normalized.Agents),
		ServiceCount: len(normalized.Services),
		TriggerCount: len(normalized.Triggers),
		Spec:         normalized,
	}, nil
}
