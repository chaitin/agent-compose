package projects

import (
	"context"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
)

func TestLLMCredentialWarningsDescribeWhatHappensToTheValue(t *testing.T) {
	tests := []struct {
		name         string
		variables    map[string]compose.EnvVarSpec
		env          map[string]compose.EnvVarSpec
		wantPaths    []string
		wantContains map[string]string
	}{
		{
			name:      "first-party credential is imported and proxied",
			variables: map[string]compose.EnvVarSpec{"ANTHROPIC_API_KEY": {Value: "sk-ant-secret"}},
			wantPaths: []string{"variables.ANTHROPIC_API_KEY"},
			wantContains: map[string]string{
				"variables.ANTHROPIC_API_KEY": "does not reach the sandbox",
			},
		},
		{
			name:      "vendor-neutral declaration against a custom endpoint",
			variables: map[string]compose.EnvVarSpec{"LLM_API_KEY": {Value: "generic-key"}, "LLM_API_ENDPOINT": {Value: "https://gateway.corp.test/v1"}},
			wantPaths: []string{"variables.LLM_API_KEY"},
			wantContains: map[string]string{
				"variables.LLM_API_KEY": "https://gateway.corp.test/v1",
			},
		},
		{
			name:      "credential the daemon cannot proxy is removed, not forwarded",
			env:       map[string]compose.EnvVarSpec{"GOOGLE_API_KEY": {Value: "google-secret"}},
			wantPaths: []string{"agents.worker.env.GOOGLE_API_KEY"},
			wantContains: map[string]string{
				"agents.worker.env.GOOGLE_API_KEY": "the agent never sees it",
			},
		},
		{
			name:      "unrecognized key is passed through",
			env:       map[string]compose.EnvVarSpec{"ACME_API_KEY": {Value: "acme-secret"}},
			wantPaths: []string{"agents.worker.env.ACME_API_KEY"},
			wantContains: map[string]string{
				"agents.worker.env.ACME_API_KEY": "not a provider the daemon recognizes",
			},
		},
		{
			name: "every declared credential is reported",
			variables: map[string]compose.EnvVarSpec{
				"ANTHROPIC_API_KEY": {Value: "sk-ant-secret"},
				"OPENAI_API_KEY":    {Value: "sk-openai-secret"},
			},
			wantPaths: []string{"variables.ANTHROPIC_API_KEY", "variables.OPENAI_API_KEY"},
		},
		{
			name:      "empty value is a reference, not a declared secret",
			variables: map[string]compose.EnvVarSpec{"ANTHROPIC_API_KEY": {Value: ""}},
		},
		{
			name:      "unrelated environment is not reported",
			variables: map[string]compose.EnvVarSpec{"LOG_LEVEL": {Value: "debug"}},
			env:       map[string]compose.EnvVarSpec{"EDITOR": {Value: "vim"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized := normalizedProjectWithCredentials(t, tt.variables, tt.env)
			controller := NewController(ControllerDependencies{
				Config:     &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
				Store:      &controllerCoverageStore{},
				Schedulers: controllerCoverageSchedulerValidator{},
			})

			validation, err := controller.ValidateProject(context.Background(), normalized, nil)
			if err != nil {
				t.Fatalf("ValidateProject returned error: %v", err)
			}
			if !validation.Valid {
				t.Fatalf("ValidateProject rejected the project: %#v", validation.Issues)
			}
			assertCredentialWarnings(t, validation.Issues, tt.wantPaths, tt.wantContains)

			result, err := controller.ApplyProject(context.Background(), ApplyRequest{Normalized: normalized, DryRun: true})
			if err != nil {
				t.Fatalf("ApplyProject returned error: %v", err)
			}
			assertCredentialWarnings(t, result.Issues, tt.wantPaths, tt.wantContains)
		})
	}
}

func normalizedProjectWithCredentials(t *testing.T, variables, env map[string]compose.EnvVarSpec) NormalizedProject {
	t.Helper()
	project := &compose.ProjectSpec{
		Name:      "credential-warning",
		Variables: variables,
		Agents: map[string]compose.AgentSpec{
			"worker": {
				Provider: "codex",
				Image:    "guest:latest",
				Driver:   &compose.DriverSpec{Docker: &compose.DockerDriverSpec{}},
				Env:      env,
			},
		},
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

func assertCredentialWarnings(t *testing.T, issues []ValidationIssue, wantPaths []string, wantContains map[string]string) {
	t.Helper()
	byPath := make(map[string]ValidationIssue, len(issues))
	for _, issue := range issues {
		if issue.Severity != ValidationSeverityWarning {
			t.Fatalf("issue %q has severity %q, want a warning: %s", issue.Path, issue.Severity, issue.Message)
		}
		byPath[issue.Path] = issue
	}
	if len(byPath) != len(wantPaths) {
		t.Fatalf("warnings = %#v, want paths %v", issues, wantPaths)
	}
	for _, path := range wantPaths {
		issue, ok := byPath[path]
		if !ok {
			t.Fatalf("no warning for %s; issues = %#v", path, issues)
		}
		if want := wantContains[path]; want != "" && !strings.Contains(issue.Message, want) {
			t.Fatalf("warning for %s = %q, want it to contain %q", path, issue.Message, want)
		}
	}
}
