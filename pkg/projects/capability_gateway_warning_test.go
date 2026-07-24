package projects

import (
	"context"
	"testing"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestGlobalCapabilityGatewayWarning(t *testing.T) {
	tests := []struct {
		name       string
		capsetIDs  []string
		gateway    domain.CapabilityGatewaySettings
		wantWarned bool
	}{
		{name: "unqualified without global gateway", capsetIDs: []string{"legacy"}, wantWarned: true},
		{name: "qualified only without global gateway", capsetIDs: []string{"internal/dev"}},
		{name: "unqualified with global gateway", capsetIDs: []string{"legacy"}, gateway: domain.CapabilityGatewaySettings{Addr: "https://octobus.example"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized := normalizedProjectWithCapsets(t, tt.capsetIDs)
			controller := NewController(ControllerDependencies{
				Config:  &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
				Store:   &controllerCoverageStore{},
				Loaders: controllerCoverageLoaderValidator{},
				Gateway: staticCapabilityGatewaySource{settings: tt.gateway},
			})

			validation, err := controller.ValidateProject(context.Background(), normalized, nil)
			if err != nil {
				t.Fatalf("ValidateProject returned error: %v", err)
			}
			if !validation.Valid {
				t.Fatalf("ValidateProject valid = false, issues = %#v", validation.Issues)
			}
			assertGatewayWarning(t, validation.Issues, tt.wantWarned)

			result, err := controller.ApplyProject(context.Background(), ApplyRequest{Normalized: normalized, DryRun: true})
			if err != nil {
				t.Fatalf("ApplyProject returned error: %v", err)
			}
			if result.Applied {
				t.Fatal("dry-run was applied")
			}
			assertGatewayWarning(t, result.Issues, tt.wantWarned)
		})
	}
}

func normalizedProjectWithCapsets(t *testing.T, capsetIDs []string) NormalizedProject {
	t.Helper()
	project := &compose.ProjectSpec{
		Name: "gateway-warning",
		Agents: map[string]compose.AgentSpec{
			"worker": {
				Provider:  "codex",
				Image:     "guest:latest",
				Driver:    &compose.DriverSpec{Docker: &compose.DockerDriverSpec{}},
				CapsetIDs: capsetIDs,
			},
		},
	}
	for _, capsetID := range capsetIDs {
		if capsetID == "internal/dev" {
			project.OctoBusServers = map[string]compose.OctoBusServerSpec{
				"internal": {URL: "https://octobus.internal.example"},
			}
		}
	}
	spec, err := compose.Normalize(project, compose.NormalizeOptions{ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	hash, err := spec.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	return NormalizedProject{Spec: spec, SpecHash: hash, SourcePath: "/repo/agent-compose.yml"}
}

func assertGatewayWarning(t *testing.T, issues []ValidationIssue, want bool) {
	t.Helper()
	if got := len(issues) == 1 && issues[0].Severity == ValidationSeverityWarning; got != want {
		t.Fatalf("gateway warning present = %v, want %v; issues = %#v", got, want, issues)
	}
}

type staticCapabilityGatewaySource struct {
	settings domain.CapabilityGatewaySettings
}

func (s staticCapabilityGatewaySource) GetCapabilityGateway(context.Context) (domain.CapabilityGatewaySettings, error) {
	return s.settings, nil
}
