package app

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestIntegrationProjectRPCLocalGitScriptBoundary(t *testing.T) {
	root := t.TempDir()
	repository := newScriptSourceRepository(t, t.TempDir())
	if err := os.Symlink(repository, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{"connect", "grpc"} {
		t.Run(protocol, func(t *testing.T) {
			client, _ := newScriptSourceProjectClient(t, protocol)
			origin := &agentcomposev2.ProjectSource{ProjectDir: root}
			inline := scriptSourceProjectSpec(nil)
			inline.Agents[0].Scheduler.Script = sourceTestScript
			created, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: inline, Source: origin}))
			if err != nil || !created.Msg.GetApplied() {
				t.Fatalf("create inline script: %v %v", created, err)
			}
			ref := &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: created.Msg.GetProject().GetSummary().GetProjectId()}}
			for _, location := range []string{repository, (&url.URL{Scheme: "file", Path: repository}).String(), "outside-link"} {
				spec := scriptSourceProjectSpec(&agentcomposev2.SchedulerScriptSource{Provider: "git", Url: location, Ref: "main", Path: "scheduler.js"})
				for _, source := range []*agentcomposev2.ProjectSource{origin, nil} {
					validated, err := client.ValidateProject(t.Context(), connect.NewRequest(&agentcomposev2.ValidateProjectRequest{Spec: spec, Source: source}))
					if err != nil || validated.Msg.GetValid() {
						t.Fatalf("validate local Git source: %v %v", validated, err)
					}
					assertLocalGitScriptIssue(t, validated.Msg.GetIssues())
					for _, dryRun := range []bool{true, false} {
						applied, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: spec, Source: source, DryRun: dryRun}))
						if err != nil || applied.Msg.GetApplied() {
							t.Fatalf("apply local Git source: %v %v", applied, err)
						}
						assertLocalGitScriptIssue(t, applied.Msg.GetIssues())
					}
				}
				for _, dryRun := range []bool{true, false} {
					patched, err := client.PatchProject(t.Context(), connect.NewRequest(&agentcomposev2.PatchProjectRequest{
						Project: ref, Spec: spec, DryRun: dryRun, ExpectedCurrentSpecHash: created.Msg.GetRevision().GetSpecHash(),
					}))
					if err != nil || patched.Msg.GetApplied() {
						t.Fatalf("patch local Git source: %v %v", patched, err)
					}
					assertLocalGitScriptIssue(t, patched.Msg.GetIssues())
				}
			}
			loaded, err := client.GetProject(t.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: ref, IncludeSpec: true}))
			if err != nil {
				t.Fatal(err)
			}
			assertScriptSnapshot(t, loaded.Msg.GetProject().GetSpec(), sourceTestScript)
			if loaded.Msg.GetProject().GetSummary().GetCurrentRevision() != created.Msg.GetProject().GetSummary().GetCurrentRevision() {
				t.Fatal("rejected local Git source changed the revision")
			}
		})
	}
}

func assertLocalGitScriptIssue(t *testing.T, issues []*agentcomposev2.ProjectValidationIssue) {
	t.Helper()
	if len(issues) != 1 || !strings.HasSuffix(issues[0].GetPath(), "scheduler.script.url") || !strings.Contains(issues[0].GetMessage(), "project source directory") {
		t.Fatalf("local Git source issues = %v", issues)
	}
}

func newScriptSourceRepository(t *testing.T, root string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "scheduler.js"), []byte(sourceTestScript), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.test"}, {"config", "user.name", "Test"}, {"add", "scheduler.js"}, {"commit", "-m", "script"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
	}
	return root
}
