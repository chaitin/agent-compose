package projects

import (
	"encoding/json"
	"testing"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

func TestNewAgentDefinitionFromSpecPreservesJupyterConfig(t *testing.T) {
	project := domain.ProjectRecord{ID: "project-1", Name: "project"}
	agent := compose.NormalizedAgentSpec{
		Name:     "reviewer",
		Provider: "codex",
		Jupyter:  &compose.JupyterSpec{Enabled: true, GuestPort: 8888},
	}

	definition, err := NewAgentDefinitionFromSpec(project, 1, agent, nil)
	if err != nil {
		t.Fatalf("NewAgentDefinitionFromSpec returned error: %v", err)
	}
	var config struct {
		Jupyter *compose.JupyterSpec `json:"jupyter"`
	}
	if err := json.Unmarshal([]byte(definition.ConfigJSON), &config); err != nil {
		t.Fatalf("unmarshal config json: %v", err)
	}
	if config.Jupyter == nil || !config.Jupyter.Enabled || config.Jupyter.GuestPort != 8888 {
		t.Fatalf("config json = %s, want jupyter enabled guest port 8888", definition.ConfigJSON)
	}
}

func TestNewAgentDefinitionFromSpecKeepsEmptyConfigWithoutJupyter(t *testing.T) {
	project := domain.ProjectRecord{ID: "project-1", Name: "project"}
	agent := compose.NormalizedAgentSpec{Name: "reviewer", Provider: "codex"}

	definition, err := NewAgentDefinitionFromSpec(project, 1, agent, nil)
	if err != nil {
		t.Fatalf("NewAgentDefinitionFromSpec returned error: %v", err)
	}
	if definition.ConfigJSON != "{}" {
		t.Fatalf("config json = %s, want empty object", definition.ConfigJSON)
	}
}

func TestProjectRecordsCarryVolumeMountSpecs(t *testing.T) {
	project := domain.ProjectRecord{ID: "project-1", Name: "project"}
	agent := compose.NormalizedAgentSpec{
		Name:     "reviewer",
		Provider: "codex",
		Image:    "guest:latest",
		Volumes: []compose.NormalizedVolumeMountSpec{
			{Type: "volume", Source: "cache", Target: "/cache"},
			{Type: "bind", Source: "./fixtures", Target: "/fixtures", ReadOnly: true},
		},
		Scheduler: &compose.NormalizedSchedulerSpec{Enabled: true, Script: "scheduler.agent('hi')"},
	}
	definition, err := NewAgentDefinitionFromSpec(project, 1, agent, nil)
	if err != nil {
		t.Fatalf("NewAgentDefinitionFromSpec returned error: %v", err)
	}
	if len(definition.Volumes) != 2 || !definition.Volumes[1].ReadOnly {
		t.Fatalf("agent definition volumes = %#v", definition.Volumes)
	}
	scheduler, ok, err := NewSchedulerRecordFromSpec(project.ID, 1, agent)
	if err != nil || !ok {
		t.Fatalf("NewSchedulerRecordFromSpec = %#v/%v/%v", scheduler, ok, err)
	}
	loader, err := NewManagedLoaderFromScheduler(project, scheduler, agent)
	if err != nil {
		t.Fatalf("NewManagedLoaderFromScheduler returned error: %v", err)
	}
	if len(loader.Volumes) != 2 || loader.Volumes[0].Source != "cache" {
		t.Fatalf("loader volumes = %#v", loader.Volumes)
	}
}

func TestNewAgentDefinitionFromSpecPreservesMCPConfig(t *testing.T) {
	project := domain.ProjectRecord{ID: "project-1", Name: "project"}
	agent := compose.NormalizedAgentSpec{
		Name:     "reviewer",
		Provider: "codex",
		MCP:      []string{"filesystem", "docs"},
	}
	projectMCPs := map[string]compose.NormalizedMCPServerSpec{
		"filesystem": {
			Type:    "local",
			Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/workspace"},
		},
		"docs": {
			Type:      "remote",
			Transport: "http",
			URL:       "https://docs.example.com/mcp",
			Headers: map[string]compose.EnvVarSpec{
				"Authorization": {Value: "Bearer secret", Secret: true},
			},
		},
	}

	definition, err := NewAgentDefinitionFromSpec(project, 1, agent, projectMCPs)
	if err != nil {
		t.Fatalf("NewAgentDefinitionFromSpec returned error: %v", err)
	}
	var config struct {
		MCPs map[string]compose.NormalizedMCPServerSpec `json:"mcps"`
	}
	if err := json.Unmarshal([]byte(definition.ConfigJSON), &config); err != nil {
		t.Fatalf("unmarshal config json: %v", err)
	}
	if len(config.MCPs) != 2 || config.MCPs["filesystem"].Command != "npx" || config.MCPs["docs"].Transport != "http" {
		t.Fatalf("config json = %s, want mcps preserved", definition.ConfigJSON)
	}
}
