package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestIntegrationCLIInspectDeclarationAndRuntime(t *testing.T) {
	script := strings.Repeat("// 大脚本\n", 16000) + "console.log('end');"
	project := inspectDetailsProject(script)
	original := proto.Clone(project)
	for _, kind := range []agentcomposev2.ResourceKind{agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT, agentcomposev2.ResourceKind_RESOURCE_KIND_AGENT} {
		t.Run(kind.String(), func(t *testing.T) {
			server := inspectDetailsServer(t, project, kind)
			defer server.Close()
			for _, byID := range []bool{false, true} {
				for _, omit := range []bool{false, true} {
					for _, jsonFlag := range []bool{false, true} {
						t.Run(fmt.Sprintf("id=%t/omit=%t/json=%t", byID, omit, jsonFlag), func(t *testing.T) {
							args := []string{"inspect", "--host", server.URL, "--project-name", "demo"}
							if omit {
								args = append(args, "--omit-scripts")
							}
							if jsonFlag {
								args = append(args, "--json")
							}
							if byID {
								args = append(args, strings.Repeat("a", 12))
							} else if kind == agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT {
								args = append(args, "project", "demo")
							} else {
								args = append(args, "agent", "reviewer")
							}
							stdout, stderr, _, code := executeCLICommand(args...)
							if code != 0 || stderr != "" {
								t.Fatalf("inspect code/stderr = %d/%s", code, stderr)
							}
							assertInspectDetails(t, stdout, project, kind, omit)
						})
					}
				}
			}
		})
	}
	if !proto.Equal(original, project) {
		t.Fatal("inspection mutated the source project")
	}
}

func inspectDetailsProject(script string) *agentcomposev2.Project {
	project := testCLIProject(strings.Repeat("a", 64), "demo", "/deployed/agent-compose.yml")
	project.Spec = &agentcomposev2.ProjectSpec{
		Name:           "demo",
		Variables:      []*agentcomposev2.EnvVarSpec{{Name: "PROJECT_SECRET", Value: "project-private", Secret: true}, {Name: "ANTHROPIC_AUTH_TOKEN", Value: "absorbed-private"}},
		OctobusServers: []*agentcomposev2.OctoBusServerSpec{{Name: "events", Token: "octobus-private"}},
		Workspaces:     []*agentcomposev2.NamedWorkspaceSpec{{Name: "shared", Workspace: &agentcomposev2.WorkspaceSpec{Provider: "git", Token: "workspace-private"}}},
		Agents: []*agentcomposev2.AgentSpec{{
			Name: "reviewer", Provider: "codex", SystemPrompt: "review carefully", Image: "guest:v1",
			Driver:     &agentcomposev2.DriverSpec{Name: "docker", Config: &agentcomposev2.DriverSpec_Docker{Docker: &agentcomposev2.DockerDriverSpec{}}},
			Env:        []*agentcomposev2.EnvVarSpec{{Name: "TOKEN", Value: "env-private", Secret: true}, {Name: "MODE", Value: "review"}, {Name: "OPENAI_API_KEY", Value: "absorbed-agent-private"}},
			Workspace:  &agentcomposev2.WorkspaceSpec{Provider: "git", Token: "agent-workspace-private"},
			Skills:     []*agentcomposev2.SkillSpec{{Name: "review", Provider: "git", Password: "skill-private"}},
			McpServers: []*agentcomposev2.MCPServerSpec{{Name: "tools", Headers: []*agentcomposev2.EnvVarSpec{{Name: "Authorization", Value: "header-private", Secret: true}}}},
			Scheduler:  &agentcomposev2.SchedulerSpec{Enabled: true, Script: script, Triggers: []*agentcomposev2.TriggerSpec{{Name: "tick", Interval: "1h"}}},
		}, {Name: "other", Scheduler: &agentcomposev2.SchedulerSpec{Script: "console.log('other');"}}},
	}
	project.Agents[0].Model = ""
	project.Agents[0].ResolvedModel = "daemon-model"
	project.Agents[0].ModelSource = agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_DAEMON_DEFAULT
	project.Agents[0].Enabled = true
	project.Agents[0].Health = agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_AT_RISK
	project.Agents[0].CurrentRun = &agentcomposev2.ProjectAgentCurrentRun{RunningSchedulerRunCount: 2}
	project.Schedulers[0].Enabled = false
	return project
}

func assertInspectDetails(t *testing.T, stdout string, project *agentcomposev2.Project, kind agentcomposev2.ResourceKind, omit bool) {
	t.Helper()
	var output struct {
		DeclaredConfig json.RawMessage        `json:"declared_config"`
		Runtime        json.RawMessage        `json:"runtime"`
		OmittedScripts []composeOmittedScript `json:"omitted_scripts"`
	}
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatal(err)
	}
	redacted := api.RedactProjectSpecSecrets(project.Spec)
	var declaration *agentcomposev2.AgentSpec
	var runtime *agentcomposev2.ProjectAgent
	count := 1
	if kind == agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT {
		config := &agentcomposev2.ProjectSpec{}
		state := &agentcomposev2.Project{}
		if err := protojson.Unmarshal(output.DeclaredConfig, config); err != nil {
			t.Fatal(err)
		}
		if err := protojson.Unmarshal(output.Runtime, state); err != nil {
			t.Fatal(err)
		}
		count = 2
		if omit {
			for _, agent := range redacted.Agents {
				agent.Scheduler.Script = ""
			}
		}
		if !proto.Equal(config, redacted) {
			t.Fatal("project declaration lost or changed configuration")
		}
		if state.GetSpec() != nil || state.GetSummary().GetCurrentRevision() != 1 || state.GetSchedulers()[0].GetEnabled() {
			t.Fatal("runtime contains declaration or incorrect revision/scheduler state")
		}
		declaration, runtime = config.Agents[0], state.Agents[0]
	} else {
		declaration, runtime = &agentcomposev2.AgentSpec{}, &agentcomposev2.ProjectAgent{}
		if err := protojson.Unmarshal(output.DeclaredConfig, declaration); err != nil {
			t.Fatal(err)
		}
		if err := protojson.Unmarshal(output.Runtime, runtime); err != nil {
			t.Fatal(err)
		}
		if omit {
			redacted.Agents[0].Scheduler.Script = ""
		}
		if !proto.Equal(declaration, redacted.Agents[0]) {
			t.Fatal("agent declaration lost or changed configuration")
		}
	}
	if declaration.Model != "" || !declaration.Scheduler.Enabled || runtime.ResolvedModel != "daemon-model" || runtime.SchedulerEnabled || runtime.CurrentRun.GetRunningSchedulerRunCount() != 2 || runtime.Health != agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_AT_RISK {
		t.Fatal("declared configuration and effective runtime were not kept separate")
	}
	for _, secret := range []string{"project-private", "octobus-private", "workspace-private", "env-private", "agent-workspace-private", "skill-private", "header-private", "absorbed-private", "absorbed-agent-private"} {
		if strings.Contains(stdout, secret) {
			t.Fatalf("inspect leaked %s", secret)
		}
	}
	// An absorbed credential is redacted, but the operator still sees that they
	// declared one and under which name. The agent declaration carries the
	// agent's own variable; the project variable only appears in a project view.
	names := []string{"OPENAI_API_KEY"}
	if kind == agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT {
		names = append(names, "ANTHROPIC_AUTH_TOKEN")
	}
	for _, name := range names {
		if !strings.Contains(stdout, name) {
			t.Fatalf("inspect hid the declared variable name %s", name)
		}
	}
	if !omit {
		if len(output.OmittedScripts) != 0 || declaration.Scheduler.Script != project.Spec.Agents[0].Scheduler.Script {
			t.Fatal("default inspect truncated the script")
		}
		return
	}
	if len(output.OmittedScripts) != count {
		t.Fatalf("omitted scripts = %d, want %d", len(output.OmittedScripts), count)
	}
	for i, item := range output.OmittedScripts {
		script := project.Spec.Agents[i].Scheduler.Script
		path := "declared_config.scheduler.script"
		if kind == agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT {
			path = fmt.Sprintf("declared_config.agents[%d].scheduler.script", i)
		}
		if item.Path != path || item.Bytes != len(script) || item.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(script))) {
			t.Fatalf("wrong omission metadata: %#v", item)
		}
	}
	if strings.Contains(string(output.DeclaredConfig), `"script"`) {
		t.Fatal("omitted script still has a body field")
	}
}

func TestIntegrationCLIInspectUnavailableDeclaration(t *testing.T) {
	project := inspectDetailsProject("")
	project.Spec = nil
	server := inspectDetailsServer(t, project, agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT)
	defer server.Close()
	for _, target := range [][]string{{"project", "demo"}, {"agent", "reviewer"}} {
		args := append([]string{"inspect", "--host", server.URL, "--project-name", "demo", "--omit-scripts"}, target...)
		stdout, stderr, _, code := executeCLICommand(args...)
		if code != 0 || stderr != "" {
			t.Fatalf("inspect code/stderr = %d/%s", code, stderr)
		}
		var output map[string]json.RawMessage
		if err := json.Unmarshal([]byte(stdout), &output); err != nil {
			t.Fatal(err)
		}
		if string(output["declared_config"]) != "null" || string(output["runtime"]) == "null" {
			t.Fatal("missing declaration was fabricated or runtime lost")
		}
	}
}

func inspectDetailsServer(t *testing.T, project *agentcomposev2.Project, kind agentcomposev2.ResourceKind) *httptest.Server {
	t.Helper()
	return newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{getProject: func(_ context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
			if !req.Msg.IncludeSpec {
				t.Error("inspect did not request persisted configuration")
			}
			return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
		}},
		run: runServiceStub{listRuns: func(context.Context, *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{}), nil
		}},
		resource: resourceServiceStub{resolveID: func(context.Context, *connect.Request[agentcomposev2.ResolveResourceIDRequest]) (*connect.Response[agentcomposev2.ResolveResourceIDResponse], error) {
			return connect.NewResponse(&agentcomposev2.ResolveResourceIDResponse{Targets: []*agentcomposev2.ResourceTarget{{Kind: kind, Id: project.Summary.ProjectId, ProjectId: project.Summary.ProjectId, AgentName: "reviewer"}}}), nil
		}},
	})
}

func TestIntegrationCLIInspectRejectsOmitScriptsForOtherResources(t *testing.T) {
	for _, target := range []string{"image", "cache", "volume", "run", "sandbox"} {
		stdout, stderr, _, code := executeCLICommand("inspect", "--omit-scripts", target, "test")
		if code != exitCodeUsage || stdout != "" || !strings.Contains(stderr, "requires a project or agent") {
			t.Fatalf("%s code/stdout/stderr = %d/%s/%s", target, code, stdout, stderr)
		}
	}
	server := inspectDetailsServer(t, inspectDetailsProject(""), agentcomposev2.ResourceKind_RESOURCE_KIND_IMAGE)
	defer server.Close()
	stdout, stderr, _, code := executeCLICommand("inspect", "--host", server.URL, "--omit-scripts", strings.Repeat("a", 12))
	if code != exitCodeUsage || stdout != "" || !strings.Contains(stderr, "requires a project or agent") {
		t.Fatalf("id code/stdout/stderr = %d/%s/%s", code, stdout, stderr)
	}
}

func TestIntegrationCLIInspectInvalidConfigurationDoesNotWritePartialOutput(t *testing.T) {
	project := inspectDetailsProject(string([]byte{0xff}))
	server := inspectDetailsServer(t, project, agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT)
	defer server.Close()
	// Invalid protobuf strings are rejected at the transport boundary too.
	stdout, _, _, code := executeCLICommand("inspect", "--host", server.URL, "project", "demo")
	if code == 0 || stdout != "" {
		t.Fatalf("invalid configuration code/stdout = %d/%s", code, stdout)
	}
}
