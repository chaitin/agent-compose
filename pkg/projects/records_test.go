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

func TestDisabledAgentDisablesManagedAgentAndSchedulerRecords(t *testing.T) {
	project := domain.ProjectRecord{ID: "project-1", Name: "project"}
	agent := compose.NormalizedAgentSpec{
		Name:      "reviewer",
		Provider:  "codex",
		Status:    "disabled",
		Scheduler: &compose.NormalizedSchedulerSpec{Enabled: true, Script: "scheduler.agent('hi')"},
	}
	definition, err := NewAgentDefinitionFromSpec(project, 1, agent, nil)
	if err != nil {
		t.Fatalf("NewAgentDefinitionFromSpec returned error: %v", err)
	}
	if definition.Enabled {
		t.Fatalf("definition enabled = true, want false")
	}
	record, err := NewAgentRecordFromSpec(project.ID, 1, agent)
	if err != nil {
		t.Fatalf("NewAgentRecordFromSpec returned error: %v", err)
	}
	if record.SchedulerEnabled {
		t.Fatalf("agent scheduler enabled = true, want false")
	}
	scheduler, ok, err := NewSchedulerRecordFromSpec(project.ID, 1, agent)
	if err != nil || !ok {
		t.Fatalf("NewSchedulerRecordFromSpec = %#v/%v/%v", scheduler, ok, err)
	}
	if scheduler.Enabled {
		t.Fatalf("scheduler enabled = true, want false")
	}
	loader, err := NewManagedLoaderFromScheduler(project, scheduler, agent)
	if err != nil {
		t.Fatalf("NewManagedLoaderFromScheduler returned error: %v", err)
	}
	if loader.Summary.Enabled {
		t.Fatalf("loader enabled = true, want false")
	}
}

func TestNewAgentDefinitionFromSpecPreservesMCPConfig(t *testing.T) {
	project := domain.ProjectRecord{ID: "project-1", Name: "project"}
	agent := compose.NormalizedAgentSpec{
		Name:     "reviewer",
		Provider: "codex",
		MCPServers: map[string]compose.NormalizedMCPServerSpec{
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
		},
	}
	projectMCPServers := map[string]compose.NormalizedMCPServerSpec{}

	definition, err := NewAgentDefinitionFromSpec(project, 1, agent, projectMCPServers)
	if err != nil {
		t.Fatalf("NewAgentDefinitionFromSpec returned error: %v", err)
	}
	var config struct {
		MCPServers map[string]compose.NormalizedMCPServerSpec `json:"mcp_servers"`
	}
	if err := json.Unmarshal([]byte(definition.ConfigJSON), &config); err != nil {
		t.Fatalf("unmarshal config json: %v", err)
	}
	if len(config.MCPServers) != 2 || config.MCPServers["filesystem"].Command != "npx" || config.MCPServers["docs"].Transport != "http" {
		t.Fatalf("config json = %s, want mcp_servers preserved", definition.ConfigJSON)
	}
}

func TestNewAgentDefinitionFromSpecCarriesSkills(t *testing.T) {
	project := domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: "/repo/agent-compose.yml"}
	agent := compose.NormalizedAgentSpec{
		Name:     "reviewer",
		Provider: "codex",
		Skills: []compose.NormalizedSkillSpec{
			{Name: "pdf", Source: "git", URL: "https://github.com/anthropics/skills.git", Path: "skills/pdf", Ref: "main", Token: "${GIT_TOKEN}"},
			{Name: "local-review", Source: "file", Path: "/tmp/skills/local-review"},
		},
	}

	definition, err := NewAgentDefinitionFromSpec(project, 1, agent, nil)
	if err != nil {
		t.Fatalf("NewAgentDefinitionFromSpec returned error: %v", err)
	}
	if len(definition.Skills) != 2 {
		t.Fatalf("skills = %#v, want 2", definition.Skills)
	}
	if definition.Skills[0].Name != "pdf" || definition.Skills[0].Token != "${GIT_TOKEN}" {
		t.Fatalf("first skill = %#v", definition.Skills[0])
	}
	if definition.Skills[1].SourceRoot != "/repo" {
		t.Fatalf("second skill source root = %q, want /repo", definition.Skills[1].SourceRoot)
	}
}

func TestManagedAgentDefinitionUnchangedComparesSkills(t *testing.T) {
	existing := domain.AgentDefinition{
		ID: "agent-1", Name: "Agent", Enabled: true, Provider: "codex", ConfigJSON: "{}",
		Skills: []domain.AgentSkill{{Name: "pdf", Source: "git", URL: "https://github.com/anthropics/skills.git", Path: "skills/pdf"}},
	}
	current := existing
	if !ManagedAgentDefinitionUnchanged(existing, current) {
		t.Fatalf("matching skills should be unchanged")
	}
	current.Skills = []domain.AgentSkill{{Name: "docx", Source: "git", URL: "https://github.com/anthropics/skills.git", Path: "skills/docx"}}
	if ManagedAgentDefinitionUnchanged(existing, current) {
		t.Fatalf("different skills should mark managed agent changed")
	}
}
