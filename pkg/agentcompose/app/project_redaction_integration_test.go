package app

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/proto"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	"github.com/chaitin/agent-compose/pkg/volumes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

// TestIntegrationRedactedViewPatchesButDoesNotApply exercises the whole
// read-modify-write path a UI uses against a credential the view hides by name
// rather than by the secret flag.
//
// PatchProject must accept the view back and keep the stored credential: a
// project that declares OPENAI_API_KEY without secret: true is exactly the case
// the name-based redaction rule covers, and refusing its marker would make the
// project uneditable. ApplyProject has no stored revision to restore from, so it
// must refuse the marker instead of persisting it as the credential.
func TestIntegrationRedactedViewPatchesButDoesNotApply(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:  root,
		DbAddr:    filepath.Join(root, "data.db"),
		DbTimeout: 5 * time.Second,
	}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("open migrated config store: %v", err)
	}
	controller := projects.NewController(projects.ControllerDependencies{
		Config: config, Store: store, Volumes: volumes.NewManager(store),
	})
	handler := api.NewProjectHandler(projectControllerDelegate{controller: controller}, store, nil, nil)
	_, connectHandler := agentcomposev2connect.NewProjectServiceHandler(handler)
	server := httptest.NewServer(connectHandler)
	t.Cleanup(server.Close)
	client := agentcomposev2connect.NewProjectServiceClient(server.Client(), server.URL)

	created, err := client.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: &agentcomposev2.ProjectSpec{
			Name: "redaction-round-trip",
			Variables: []*agentcomposev2.EnvVarSpec{
				{Name: "OPENAI_API_KEY", Value: "sk-real-credential"},
				{Name: "MODE", Value: "review"},
			},
		},
		Source: &agentcomposev2.ProjectSource{ComposePath: "/srv/project/compose.yaml"},
	}))
	if err != nil {
		t.Fatalf("ApplyProject create: %v", err)
	}
	if !created.Msg.GetApplied() {
		t.Fatalf("ApplyProject create response = %#v", created.Msg)
	}
	projectID := created.Msg.GetProject().GetSummary().GetProjectId()
	projectRef := &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: projectID}}

	initialRevision, err := store.GetProjectRevision(ctx, projectID, int64(created.Msg.GetRevision().GetRevision()))
	if err != nil {
		t.Fatalf("load initial revision: %v", err)
	}
	if !strings.Contains(initialRevision.SpecJSON, "sk-real-credential") {
		t.Fatalf("initial revision lost the credential: %s", initialRevision.SpecJSON)
	}

	loaded, err := client.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project: projectRef, IncludeSpec: true,
	}))
	if err != nil {
		t.Fatalf("GetProject after create: %v", err)
	}
	view := loaded.Msg.GetProject().GetSpec()
	if got := projectVariableValue(view, "OPENAI_API_KEY"); got != "********" {
		t.Fatalf("view credential = %q, want the redaction marker", got)
	}

	// The client changes one unrelated field and sends the redacted view back.
	candidate := proto.Clone(view).(*agentcomposev2.ProjectSpec)
	setProjectVariableValue(t, candidate, "MODE", "audit")
	patched, err := client.PatchProject(ctx, connect.NewRequest(&agentcomposev2.PatchProjectRequest{
		Project: projectRef, ExpectedCurrentSpecHash: created.Msg.GetRevision().GetSpecHash(), Spec: candidate,
	}))
	if err != nil {
		t.Fatalf("PatchProject with a redacted view: %v", err)
	}
	if !patched.Msg.GetApplied() {
		t.Fatalf("PatchProject rejected the round trip: %#v", patched.Msg.GetIssues())
	}
	for _, issue := range patched.Msg.GetIssues() {
		if issue.GetSeverity() == agentcomposev2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_ERROR {
			t.Fatalf("PatchProject returned a blocking issue: %s: %s", issue.GetPath(), issue.GetMessage())
		}
	}
	patchedRevision, err := store.GetProjectRevision(ctx, projectID, int64(patched.Msg.GetRevision().GetRevision()))
	if err != nil {
		t.Fatalf("load patched revision: %v", err)
	}
	if strings.Contains(patchedRevision.SpecJSON, "********") {
		t.Fatalf("patched revision persisted the marker: %s", patchedRevision.SpecJSON)
	}
	for _, want := range []string{"sk-real-credential", `"audit"`} {
		if !strings.Contains(patchedRevision.SpecJSON, want) {
			t.Fatalf("patched revision missing %s: %s", want, patchedRevision.SpecJSON)
		}
	}

	// ApplyProject cannot restore a marker: there is no revision to take the
	// value from, and the run-scoped facade is created from whatever it stores.
	applied, err := client.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: &agentcomposev2.ProjectSpec{
			Name:      "redaction-round-trip",
			Variables: []*agentcomposev2.EnvVarSpec{{Name: "OPENAI_API_KEY", Value: "********"}},
		},
		Source: &agentcomposev2.ProjectSource{ComposePath: "/srv/project/compose.yaml"},
	}))
	if err != nil {
		t.Fatalf("ApplyProject with a redacted view: %v", err)
	}
	if applied.Msg.GetApplied() || len(applied.Msg.GetIssues()) == 0 {
		t.Fatalf("ApplyProject accepted a redacted view: %#v", applied.Msg)
	}
	blocking := applied.Msg.GetIssues()[0]
	if blocking.GetPath() != "variables.OPENAI_API_KEY.value" {
		t.Fatalf("ApplyProject issue path = %q", blocking.GetPath())
	}
	if blocking.GetSeverity() != agentcomposev2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_ERROR {
		t.Fatalf("ApplyProject issue severity = %v, want error", blocking.GetSeverity())
	}
	finalRevision, err := store.GetProjectRevision(ctx, projectID, int64(patched.Msg.GetRevision().GetRevision()))
	if err != nil {
		t.Fatalf("load final revision: %v", err)
	}
	if strings.Contains(finalRevision.SpecJSON, "********") {
		t.Fatalf("rejected apply changed the stored revision: %s", finalRevision.SpecJSON)
	}
}
