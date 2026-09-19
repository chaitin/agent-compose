package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestIntegrationProjectRPCRejectsFileScriptSources(t *testing.T) {
	root := t.TempDir()
	scriptPath := filepath.Join(root, "scheduler.js")
	if err := os.WriteFile(scriptPath, []byte(sourceTestScript), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{"connect", "grpc"} {
		t.Run(protocol, func(t *testing.T) {
			client, _ := newScriptSourceProjectClient(t, protocol)
			inline := scriptSourceProjectSpec(nil)
			inline.Agents[0].Scheduler.Script = sourceTestScript
			created, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: inline}))
			if err != nil || !created.Msg.GetApplied() {
				t.Fatalf("create inline script: %v %v", created, err)
			}
			ref := &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: created.Msg.GetProject().GetSummary().GetProjectId()}}
			spec := scriptSourceProjectSpec(&agentcomposev2.SchedulerScriptSource{Provider: "file", Path: scriptPath})
			validated, err := client.ValidateProject(t.Context(), connect.NewRequest(&agentcomposev2.ValidateProjectRequest{Spec: spec}))
			if err != nil || validated.Msg.GetValid() {
				t.Fatalf("validate file source: %v %v", validated, err)
			}
			assertFileScriptIssue(t, validated.Msg.GetIssues())
			for _, dryRun := range []bool{true, false} {
				applied, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: spec, DryRun: dryRun}))
				if err != nil || applied.Msg.GetApplied() {
					t.Fatalf("apply file source: %v %v", applied, err)
				}
				assertFileScriptIssue(t, applied.Msg.GetIssues())
				patched, err := client.PatchProject(t.Context(), connect.NewRequest(&agentcomposev2.PatchProjectRequest{
					Project: ref, Spec: spec, DryRun: dryRun, ExpectedCurrentSpecHash: created.Msg.GetRevision().GetSpecHash(),
				}))
				if err != nil || patched.Msg.GetApplied() {
					t.Fatalf("patch file source: %v %v", patched, err)
				}
				assertFileScriptIssue(t, patched.Msg.GetIssues())
			}
			loaded, err := client.GetProject(t.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: ref, IncludeSpec: true}))
			if err != nil {
				t.Fatal(err)
			}
			assertScriptSnapshot(t, loaded.Msg.GetProject().GetSpec(), sourceTestScript)
			if loaded.Msg.GetProject().GetSummary().GetCurrentRevision() != created.Msg.GetProject().GetSummary().GetCurrentRevision() {
				t.Fatal("rejected file source changed the revision")
			}
		})
	}
}

func assertFileScriptIssue(t *testing.T, issues []*agentcomposev2.ProjectValidationIssue) {
	t.Helper()
	if len(issues) != 1 || !strings.HasSuffix(issues[0].GetPath(), "scheduler.script.provider") || !strings.Contains(issues[0].GetMessage(), "http or git") {
		t.Fatalf("file provider issues = %v", issues)
	}
}
