package app

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/chaitin/agent-compose/pkg/compose"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestIntegrationProjectRPCLiteralValues(t *testing.T) {
	t.Setenv("RPC_LITERAL", "daemon-before")
	for _, protocol := range []string{"connect", "grpc"} {
		t.Run(protocol, func(t *testing.T) {
			client, store := newScriptSourceProjectClient(t, protocol)
			spec := literalProjectSpec()
			validated, err := client.ValidateProject(t.Context(), connect.NewRequest(&agentcomposev2.ValidateProjectRequest{Spec: spec}))
			if err != nil || !validated.Msg.GetValid() {
				t.Fatalf("validate: %v %v", validated, err)
			}
			t.Setenv("RPC_LITERAL", "daemon-after")
			dry, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: spec, DryRun: true, SubmittedSpecHash: validated.Msg.GetSpecHash()}))
			if err != nil || len(dry.Msg.GetIssues()) != 0 {
				t.Fatalf("dry apply: %v %v", dry, err)
			}
			applied, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: spec, SubmittedSpecHash: validated.Msg.GetSpecHash()}))
			if err != nil || !applied.Msg.GetApplied() {
				t.Fatalf("apply: %v %v", applied, err)
			}
			id := applied.Msg.GetProject().GetSummary().GetProjectId()
			ref := &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: id}}
			// Change a public field so Patch must persist a new revision. Credentials
			// returned redacted by Get must survive restoration without interpolation.
			loaded, err := client.GetProject(t.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: ref, IncludeSpec: true}))
			if err != nil {
				t.Fatal(err)
			}
			patch := loaded.Msg.GetProject().GetSpec()
			patch.Agents[0].Description = "updated"
			for _, dryRun := range []bool{true, false} {
				patched, err := client.PatchProject(t.Context(), connect.NewRequest(&agentcomposev2.PatchProjectRequest{Project: ref, Spec: patch, DryRun: dryRun, ExpectedCurrentSpecHash: applied.Msg.GetRevision().GetSpecHash()}))
				if err != nil || len(patched.Msg.GetIssues()) != 0 {
					t.Fatalf("patch: %v %v", patched, err)
				}
			}
			for _, revision := range []int64{1, 2} {
				record, err := store.GetProjectRevision(t.Context(), id, revision)
				if err != nil {
					t.Fatal(err)
				}
				normalized, err := compose.ParseCanonicalJSON([]byte(record.SpecJSON))
				if err != nil {
					t.Fatal(err)
				}
				assertLiteralProject(t, normalized)
			}
		})
	}
}

func literalProjectSpec() *agentcomposev2.ProjectSpec {
	spec := scriptSourceProjectSpec(nil)
	spec.Agents[0].Scheduler.Script = `function main() { return "${RPC_LITERAL}"; }`
	spec.Variables = []*agentcomposev2.EnvVarSpec{{Name: "VALUE", Value: "${RPC_LITERAL}", Secret: true}, {Name: "MISSING", Value: "${RPC_LITERAL_ABSENT}"}}
	agent := spec.Agents[0]
	agent.Model = "${RPC_LITERAL}"
	agent.Env = []*agentcomposev2.EnvVarSpec{{Name: "RPC_LITERAL", Value: "agent-value"}, {Name: "VALUE", Value: "prefix-${RPC_LITERAL}", Secret: true}}
	agent.Workspace = &agentcomposev2.WorkspaceSpec{Provider: "git", Url: "https://source.test/${RPC_LITERAL}.git", Ref: "${RPC_LITERAL}", Username: "${RPC_LITERAL}", Password: "${RPC_LITERAL}", Token: "${RPC_LITERAL}"}
	spec.Workspaces = []*agentcomposev2.NamedWorkspaceSpec{{Name: "archive", Workspace: &agentcomposev2.WorkspaceSpec{Provider: "http", Url: "https://source.test/${RPC_LITERAL}.zip", Path: "${RPC_LITERAL}", Format: "zip", Token: "${RPC_LITERAL}"}}}
	agent.Skills = []*agentcomposev2.SkillSpec{{Name: "review", Provider: "git", Url: "https://source.test/${RPC_LITERAL}.git", Path: "${RPC_LITERAL}", Ref: "${RPC_LITERAL}", Username: "${RPC_LITERAL}", Password: "${RPC_LITERAL}", Token: "${RPC_LITERAL}"}}
	agent.McpServers = []*agentcomposev2.MCPServerSpec{{Name: "docs", Type: "remote", Transport: "http", Url: "https://source.test/${RPC_LITERAL}", Headers: []*agentcomposev2.EnvVarSpec{{Name: "X-Value", Value: "${RPC_LITERAL}"}}}}
	return spec
}

func assertLiteralProject(t *testing.T, spec *compose.NormalizedProjectSpec) {
	t.Helper()
	a := spec.Agents[0]
	for field, value := range map[string]string{
		"variable": spec.Variables["VALUE"].Value, "model": a.Model, "workspace ref": a.Workspace.Ref,
		"workspace user": a.Workspace.Username, "workspace password": a.Workspace.Password, "workspace token": a.Workspace.Token,
		"archive token": spec.Workspaces["archive"].Token, "archive path": spec.Workspaces["archive"].Path,
		"skill path": a.Skills[0].Path, "skill ref": a.Skills[0].Ref, "skill user": a.Skills[0].Username,
		"skill password": a.Skills[0].Password, "skill token": a.Skills[0].Token, "MCP header": a.MCPServers["docs"].Headers["X-Value"].Value,
	} {
		if value != "${RPC_LITERAL}" {
			t.Errorf("%s changed to %q", field, value)
		}
	}
	if spec.Variables["MISSING"].Value != "${RPC_LITERAL_ABSENT}" || a.Env["VALUE"].Value != "prefix-${RPC_LITERAL}" {
		t.Error("environment values changed")
	}
	for _, value := range []string{a.Workspace.URL, a.Skills[0].URL, a.MCPServers["docs"].URL, a.Scheduler.Script} {
		if !strings.Contains(value, "${RPC_LITERAL}") {
			t.Errorf("embedded reference changed: %q", value)
		}
	}
}
