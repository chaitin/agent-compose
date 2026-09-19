package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/proto"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/images"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/volumes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

const sourceTestScript = `function main() { return {ok: true}; }`

func TestIntegrationProjectScriptSources(t *testing.T) {
	root := newScriptSourceRepository(t, t.TempDir())
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer source-test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if _, err := fmt.Fprint(w, sourceTestScript); err != nil {
			t.Errorf("write source response: %v", err)
		}
	}))
	t.Cleanup(sourceServer.Close)
	for _, protocol := range []string{"connect", "grpc"} {
		for _, source := range []*agentcomposev2.SchedulerScriptSource{
			{Provider: "http", Url: sourceServer.URL, Token: "source-test-token"},
			{Provider: "git", Url: root, Ref: "main", Path: "scheduler.js"},
			{Provider: "git", Url: (&url.URL{Scheme: "file", Path: root}).String(), Ref: "main", Path: "scheduler.js"},
			{Provider: "git", Url: ".", Ref: "main", Path: "scheduler.js"},
		} {
			t.Run(protocol+"/"+source.Provider, func(t *testing.T) {
				client, store := newScriptSourceProjectClient(t, protocol)
				spec := scriptSourceProjectSpec(source)
				original := proto.Clone(spec)
				origin := &agentcomposev2.ProjectSource{ComposePath: filepath.Join(root, "compose.yml")}
				if protocol == "grpc" {
					origin = &agentcomposev2.ProjectSource{ProjectDir: root}
				}
				validated, err := client.ValidateProject(t.Context(), connect.NewRequest(&agentcomposev2.ValidateProjectRequest{Spec: spec, Source: origin}))
				if err != nil || !validated.Msg.GetValid() {
					t.Fatalf("validate: %v %v", validated, err)
				}
				dry, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: spec, Source: origin, DryRun: true}))
				if err != nil || dry.Msg.GetApplied() || len(dry.Msg.GetIssues()) != 0 {
					t.Fatalf("dry run: %v %v", dry, err)
				}
				applied, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: spec, Source: origin, SubmittedSpecHash: validated.Msg.GetSpecHash()}))
				if err != nil || !applied.Msg.GetApplied() || len(applied.Msg.GetIssues()) != 0 {
					t.Fatalf("apply: %v %v", applied, err)
				}
				assertScriptSnapshot(t, applied.Msg.GetProject().GetSpec(), sourceTestScript)
				if !proto.Equal(original, spec) {
					t.Fatal("source request mutated")
				}
				id := applied.Msg.GetProject().GetSummary().GetProjectId()
				record, err := store.GetProjectRevision(t.Context(), id, 1)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(record.SpecJSON, "source-test-token") || strings.Contains(record.SpecJSON, "script_source") || !strings.Contains(record.SpecJSON, sourceTestScript) {
					t.Fatalf("persisted unexpected source: %s", record.SpecJSON)
				}
				ref := &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: id}}
				loaded, err := client.GetProject(t.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: ref, IncludeSpec: true}))
				if err != nil {
					t.Fatal(err)
				}
				assertScriptSnapshot(t, loaded.Msg.GetProject().GetSpec(), sourceTestScript)
				inline := proto.Clone(spec).(*agentcomposev2.ProjectSpec)
				inline.Agents[0].Scheduler.ScriptSource = nil
				inline.Agents[0].Scheduler.Script = sourceTestScript
				unchanged, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: inline, Source: origin}))
				if err != nil || !unchanged.Msg.GetUnchanged() || unchanged.Msg.GetRevision().GetSpecHash() != validated.Msg.GetSpecHash() {
					t.Fatalf("inline compatibility/hash: %v %v", unchanged, err)
				}
				patched, err := client.PatchProject(t.Context(), connect.NewRequest(&agentcomposev2.PatchProjectRequest{Project: ref, Spec: spec, ExpectedCurrentSpecHash: validated.Msg.GetSpecHash()}))
				if err != nil || !patched.Msg.GetUnchanged() {
					t.Fatalf("source patch: %v %v", patched, err)
				}
				assertScriptSnapshot(t, patched.Msg.GetProject().GetSpec(), sourceTestScript)
			})
		}
	}
}

func TestIntegrationProjectScriptSourceFailureDoesNotPersist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/empty":
			if _, err := fmt.Fprint(w, " \n"); err != nil {
				t.Errorf("write source response: %v", err)
			}
		case "/encoding":
			if _, err := w.Write([]byte{0xff}); err != nil {
				t.Errorf("write source response: %v", err)
			}
		case "/large":
			if _, err := fmt.Fprint(w, strings.Repeat("x", (1<<20)+1)); err != nil {
				t.Errorf("write source response: %v", err)
			}
		default:
			http.Error(w, "source-test-secret", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, store := newScriptSourceProjectClient(t, "grpc")
	for _, path := range []string{"/empty", "/encoding", "/large", "/missing?token=source-test-secret"} {
		t.Run(path, func(t *testing.T) {
			result, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: scriptSourceProjectSpec(&agentcomposev2.SchedulerScriptSource{Provider: "http", Url: server.URL + path})}))
			if err != nil || result.Msg.GetApplied() || len(result.Msg.GetIssues()) == 0 {
				t.Fatalf("invalid source: %v %v", result, err)
			}
			if strings.Contains(result.Msg.String(), "source-test-secret") {
				t.Fatal("source failure leaked credentials")
			}
		})
	}
	list, err := store.ListProjects(t.Context(), domain.ProjectListOptions{})
	if err != nil || len(list.Projects) != 0 {
		t.Fatalf("failed apply wrote projects: %v %v", list, err)
	}
}

func TestIntegrationProjectScriptSourceFailuresPreserveRevision(t *testing.T) {
	client, _ := newScriptSourceProjectClient(t, "grpc")
	inline := scriptSourceProjectSpec(nil)
	inline.Agents[0].Scheduler.Script = sourceTestScript
	created, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: inline}))
	if err != nil || !created.Msg.GetApplied() {
		t.Fatalf("create: %v %v", created, err)
	}
	ref := &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: created.Msg.GetProject().GetSummary().GetProjectId()}}
	provider := func(body string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.Error(w, "missing", http.StatusNotFound)
				return
			}
			if _, err := fmt.Fprint(w, body); err != nil {
				t.Errorf("write source response: %v", err)
			}
		}))
		t.Cleanup(server.Close)
		return server
	}
	unchanged := provider(sourceTestScript)
	changed := provider(sourceTestScript + " // changed after validation")
	spec := scriptSourceProjectSpec(&agentcomposev2.SchedulerScriptSource{Provider: "http", Url: unchanged.URL})
	validated, err := client.ValidateProject(t.Context(), connect.NewRequest(&agentcomposev2.ValidateProjectRequest{Spec: spec}))
	if err != nil || !validated.Msg.GetValid() {
		t.Fatalf("validate: %v %v", validated, err)
	}
	drifted := scriptSourceProjectSpec(&agentcomposev2.SchedulerScriptSource{Provider: "http", Url: changed.URL})
	applied, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: drifted, SubmittedSpecHash: validated.Msg.GetSpecHash()}))
	if err != nil || applied.Msg.GetApplied() || len(applied.Msg.GetIssues()) != 1 || applied.Msg.GetIssues()[0].GetPath() != "submitted_spec_hash" {
		t.Fatalf("changed source hash: %v %v", applied, err)
	}
	missing := scriptSourceProjectSpec(&agentcomposev2.SchedulerScriptSource{Provider: "http", Url: unchanged.URL + "/missing"})
	patched, err := client.PatchProject(t.Context(), connect.NewRequest(&agentcomposev2.PatchProjectRequest{Project: ref, Spec: missing, ExpectedCurrentSpecHash: created.Msg.GetRevision().GetSpecHash()}))
	if err != nil || patched.Msg.GetApplied() || len(patched.Msg.GetIssues()) == 0 {
		t.Fatalf("missing patch source: %v %v", patched, err)
	}
	loaded, err := client.GetProject(t.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: ref, IncludeSpec: true}))
	if err != nil {
		t.Fatal(err)
	}
	assertScriptSnapshot(t, loaded.Msg.GetProject().GetSpec(), sourceTestScript)
	if !proto.Equal(created.Msg.GetProject().GetSummary(), loaded.Msg.GetProject().GetSummary()) {
		t.Fatal("failed source requests changed the project revision")
	}
}

func TestIntegrationPatchScriptSourceRechecksConcurrentRevision(t *testing.T) {
	client, _ := newScriptSourceProjectClient(t, "grpc")
	base := scriptSourceProjectSpec(nil)
	base.Agents[0].Scheduler.Script = sourceTestScript
	created, err := client.ApplyProject(t.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: base}))
	if err != nil || !created.Msg.GetApplied() {
		t.Fatalf("create: %v %v", created, err)
	}
	ref := &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: created.Msg.GetProject().GetSummary().GetProjectId()}}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			if _, err := fmt.Fprint(w, sourceTestScript+" // slow patch"); err != nil {
				t.Errorf("write source response: %v", err)
			}
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(unblock)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, err := client.PatchProject(ctx, connect.NewRequest(&agentcomposev2.PatchProjectRequest{Project: ref, ExpectedCurrentSpecHash: created.Msg.GetRevision().GetSpecHash(), Spec: scriptSourceProjectSpec(&agentcomposev2.SchedulerScriptSource{Provider: "http", Url: server.URL})}))
		done <- err
	}()
	defer func() {
		unblock()
		select {
		case <-finished:
		case <-time.After(15 * time.Second):
			t.Error("patch did not terminate")
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	replacement := proto.Clone(base).(*agentcomposev2.ProjectSpec)
	replacement.Agents[0].Scheduler.Script += " // newer revision"
	applied, err := client.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: replacement}))
	if err != nil || !applied.Msg.GetApplied() {
		t.Fatalf("apply while source blocked: %v %v", applied, err)
	}
	unblock()
	select {
	case err := <-done:
		if connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("stale patch = %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	loaded, err := client.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: ref, IncludeSpec: true}))
	if err != nil {
		t.Fatal(err)
	}
	assertScriptSnapshot(t, loaded.Msg.GetProject().GetSpec(), replacement.Agents[0].Scheduler.Script)
}

func scriptSourceProjectSpec(source *agentcomposev2.SchedulerScriptSource) *agentcomposev2.ProjectSpec {
	return &agentcomposev2.ProjectSpec{Name: "script-sources", Agents: []*agentcomposev2.AgentSpec{{Name: "reviewer", Provider: "codex", Image: "guest:test", Scheduler: &agentcomposev2.SchedulerSpec{Enabled: true, ScriptSource: source}}}}
}

func assertScriptSnapshot(t *testing.T, spec *agentcomposev2.ProjectSpec, expected string) {
	t.Helper()
	if len(spec.GetAgents()) != 1 || spec.Agents[0].GetScheduler().GetScript() != expected || spec.Agents[0].GetScheduler().GetScriptSource() != nil {
		t.Fatalf("unexpected script snapshot: %v", spec)
	}
}

// The runtime validator is real; no background scheduler work is started.
type scriptSourceScheduler struct{ schedulers.QJSSchedulerEngine }

func (*scriptSourceScheduler) Refresh(context.Context) error { return nil }

type scriptSourceImages struct{ images.Backend }

func (scriptSourceImages) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{}, nil
}

func newScriptSourceProjectClient(t *testing.T, protocol string) (agentcomposev2connect.ProjectServiceClient, *configstore.ConfigStore) {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, DbAddr: filepath.Join(root, "data.db"), DbTimeout: 5 * time.Second, RuntimeDriver: "docker"}
	di := do.New()
	do.ProvideValue(di, t.Context())
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatal(err)
	}
	controller := projects.NewController(projects.ControllerDependencies{Config: config, Store: store, Volumes: volumes.NewManager(store), Images: scriptSourceImages{}, Schedulers: &scriptSourceScheduler{}})
	handler := api.NewProjectHandler(projectControllerDelegate{controller: controller}, store)
	_, connectHandler := agentcomposev2connect.NewProjectServiceHandler(handler)
	server := httptest.NewUnstartedServer(connectHandler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	var options []connect.ClientOption
	if protocol == "grpc" {
		options = append(options, connect.WithGRPC())
	}
	return agentcomposev2connect.NewProjectServiceClient(server.Client(), server.URL, options...), store
}
