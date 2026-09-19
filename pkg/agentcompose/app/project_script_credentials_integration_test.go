package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestIntegrationProjectScriptSourceLiteralCredentials(t *testing.T) {
	t.Setenv("SCRIPT_TOKEN", "process-value")
	for _, protocol := range []string{"connect", "grpc"} {
		for _, basic := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/basic=%t", protocol, basic), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if basic {
						user, password, ok := r.BasicAuth()
						if !ok || user != "${SCRIPT_TOKEN}" || password != "prefix-${SCRIPT_TOKEN}" {
							t.Error("basic credentials were changed")
						}
					} else if r.Header.Get("Authorization") != "Bearer ${SCRIPT_TOKEN}" {
						t.Error("token was changed")
					}
					if _, err := fmt.Fprint(w, sourceTestScript); err != nil {
						t.Errorf("write script: %v", err)
					}
				}))
				t.Cleanup(server.Close)
				client, _ := newScriptSourceProjectClient(t, protocol)
				source := &agentcomposev2.SchedulerScriptSource{Provider: "http", Url: server.URL, Token: "${SCRIPT_TOKEN}"}
				if basic {
					source.Token = ""
					source.Username = "${SCRIPT_TOKEN}"
					source.Password = "prefix-${SCRIPT_TOKEN}"
				}
				spec := scriptSourceProjectSpec(source)
				validated, err := client.ValidateProject(t.Context(), connect.NewRequest(&agentcomposev2.ValidateProjectRequest{Spec: spec}))
				if err != nil || !validated.Msg.GetValid() {
					t.Fatalf("validate: %v %v", validated, err)
				}
				applied, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: spec, SubmittedSpecHash: validated.Msg.GetSpecHash()}))
				if err != nil || !applied.Msg.GetApplied() {
					t.Fatalf("apply: %v %v", applied, err)
				}
				ref := &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: applied.Msg.GetProject().GetSummary().GetProjectId()}}
				for _, dry := range []bool{true, false} {
					a, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: spec, DryRun: dry}))
					if err != nil || len(a.Msg.GetIssues()) != 0 {
						t.Fatalf("apply: %v %v", a, err)
					}
					p, err := client.PatchProject(t.Context(), connect.NewRequest(&agentcomposev2.PatchProjectRequest{Project: ref, Spec: spec, DryRun: dry, ExpectedCurrentSpecHash: applied.Msg.GetRevision().GetSpecHash()}))
					if err != nil || len(p.Msg.GetIssues()) != 0 {
						t.Fatalf("patch: %v %v", p, err)
					}
				}
				loaded, err := client.GetProject(t.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: ref, IncludeSpec: true}))
				if err != nil {
					t.Fatal(err)
				}
				assertScriptSnapshot(t, loaded.Msg.GetProject().GetSpec(), sourceTestScript)
				if requests.Load() != 6 {
					t.Fatalf("source requests = %d, want 6", requests.Load())
				}
			})
		}
	}
}
