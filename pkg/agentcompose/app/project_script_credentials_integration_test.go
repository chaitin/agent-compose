package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestIntegrationProjectScriptSourceRejectsDaemonCredentialReferences(t *testing.T) {
	t.Setenv("SCRIPT_SOURCE_DAEMON_SECRET", "daemon-only-secret")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected source request", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	client, _ := newScriptSourceProjectClient(t, "grpc")
	inline := scriptSourceProjectSpec(nil)
	inline.Agents[0].Scheduler.Script = sourceTestScript
	created, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: inline}))
	if err != nil || !created.Msg.GetApplied() {
		t.Fatalf("create: %v %v", created, err)
	}
	ref := &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: created.Msg.GetProject().GetSummary().GetProjectId()}}
	for _, provider := range []string{"http", "git"} {
		for _, field := range []string{"username", "password", "token"} {
			t.Run(provider+"/"+field, func(t *testing.T) {
				source := &agentcomposev2.SchedulerScriptSource{Provider: provider, Url: server.URL}
				if provider == "git" {
					source.Path = "scheduler.js"
				}
				credential := " ${SCRIPT_SOURCE_DAEMON_SECRET} "
				switch field {
				case "username":
					source.Username = credential
				case "password":
					source.Password = credential
				case "token":
					source.Token = credential
				}
				spec := scriptSourceProjectSpec(source)
				validated, err := client.ValidateProject(t.Context(), connect.NewRequest(&agentcomposev2.ValidateProjectRequest{Spec: spec}))
				if err != nil || validated.Msg.GetValid() {
					t.Fatalf("validate: %v %v", validated, err)
				}
				assertCredentialReferenceIssue(t, validated.Msg.GetIssues(), field)
				for _, dryRun := range []bool{false, true} {
					applied, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: spec, DryRun: dryRun}))
					if err != nil || applied.Msg.GetApplied() {
						t.Fatalf("apply: %v %v", applied, err)
					}
					assertCredentialReferenceIssue(t, applied.Msg.GetIssues(), field)
					patched, err := client.PatchProject(t.Context(), connect.NewRequest(&agentcomposev2.PatchProjectRequest{Project: ref, Spec: spec, ExpectedCurrentSpecHash: created.Msg.GetRevision().GetSpecHash(), DryRun: dryRun}))
					if err != nil || patched.Msg.GetApplied() {
						t.Fatalf("patch: %v %v", patched, err)
					}
					assertCredentialReferenceIssue(t, patched.Msg.GetIssues(), field)
				}
			})
		}
	}
	if requests.Load() != 0 {
		t.Fatal("unresolved credentials triggered outbound requests")
	}
	loaded, err := client.GetProject(t.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: ref, IncludeSpec: true}))
	if err != nil {
		t.Fatal(err)
	}
	assertScriptSnapshot(t, loaded.Msg.GetProject().GetSpec(), sourceTestScript)
}

func assertCredentialReferenceIssue(t *testing.T, issues []*agentcomposev2.ProjectValidationIssue, field string) {
	t.Helper()
	if len(issues) != 1 || !strings.Contains(issues[0].GetPath(), "scheduler.script") || !strings.Contains(issues[0].GetMessage(), field+" must be resolved by the caller") {
		t.Fatalf("expected credential validation issue, got %v", issues)
	}
	if strings.Contains(issues[0].GetMessage(), "daemon-only-secret") {
		t.Fatal("validation leaked a daemon secret")
	}
}
