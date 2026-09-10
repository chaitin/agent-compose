package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

// This is a public-service E2E: production HTTP routes, controllers, SQLite,
// workspace provisioning, skill resolution and agent execution orchestration.
// Only image discovery and external sandbox execution are controlled substitutes;
// it does not claim to verify a Docker daemon, a VM, or model behavior.
func TestE2EPublicProjectPrivateSkillsReuseAndWorkspaceSnapshotLifecycle(t *testing.T) {
	service := newSkillsPublicE2EService(t)
	source := filepath.Join(service.root, "project")
	writeSkillsPublicE2EFile(t, filepath.Join(source, "workspace", "source.txt"), "workspace source")
	writeSkillsPublicE2EFile(t, filepath.Join(source, "skills", "pdf", "SKILL.md"), "pdf original")
	writeSkillsPublicE2EFile(t, filepath.Join(source, "skills", "pdf", "tool.sh"), "echo original")
	writeSkillsPublicE2EFile(t, filepath.Join(source, "skills", "obsolete", "SKILL.md"), "obsolete original")
	spec := &agentcomposev2.ProjectSpec{Name: "private-skills-e2e", Agents: []*agentcomposev2.AgentSpec{{
		Name: "worker", Provider: "codex", Image: "guest:e2e",
		Workspace: &agentcomposev2.WorkspaceSpec{Provider: "file", Path: "./workspace"},
		Skills: []*agentcomposev2.SkillSpec{
			{Name: "pdf", Provider: "file", Path: "./skills/pdf"},
			{Name: "obsolete", Provider: "file", Path: "./skills/obsolete"},
		},
	}}}
	projectID := service.apply(t, spec, source)
	first := service.run(t, projectID, "", "pdf original")
	second := service.run(t, projectID, "", "pdf original")
	if first.Summary.ID == second.Summary.ID {
		t.Fatal("independent runs reused the same sandbox")
	}
	firstSkills, secondSkills := execution.HostAgentSkillsDir(first), execution.HostAgentSkillsDir(second)
	assertSkillsPublicE2EPrivateCopies(t, firstSkills, secondSkills)
	firstBefore := skillsPublicE2EInventory(t, firstSkills)
	secondBefore := skillsPublicE2EInventory(t, secondSkills)
	alias := filepath.Join(execution.HostSandboxDir(first), "home", ".claude", "skills")
	aliasBefore, err := os.Lstat(alias)
	if err != nil {
		t.Fatal(err)
	}
	writeSkillsPublicE2EFile(t, filepath.Join(first.Summary.WorkspacePath, "source.txt"), "sandbox workspace edit")
	service.run(t, projectID, first.Summary.ID, "pdf original")
	assertSkillsPublicE2EUnchanged(t, firstSkills, firstBefore)
	aliasAfter, err := os.Lstat(alias)
	if err != nil || !os.SameFile(aliasBefore, aliasAfter) {
		t.Fatalf("unchanged prompt replaced Claude alias: %v", err)
	}
	assertSkillsPublicE2EFile(t, filepath.Join(first.Summary.WorkspacePath, "source.txt"), "sandbox workspace edit")
	assertSkillsPublicE2EFile(t, filepath.Join(source, "workspace", "source.txt"), "workspace source")
	service.rejectUnmanagedAlias(t, projectID, first)

	// Shared-filesystem runtimes expose this sandbox-private tree to the guest.
	// Guest writes must neither reach the source nor another sandbox, and the
	// next public prompt must restore changed, missing and unexpected entries.
	writeSkillsPublicE2EFile(t, filepath.Join(firstSkills, "pdf", "SKILL.md"), "guest edit")
	if err := os.Remove(filepath.Join(firstSkills, "pdf", "tool.sh")); err != nil {
		t.Fatal(err)
	}
	writeSkillsPublicE2EFile(t, filepath.Join(firstSkills, "pdf", "extra.txt"), "guest extra")
	service.run(t, projectID, first.Summary.ID, "pdf original")
	assertSkillsPublicE2EFile(t, filepath.Join(firstSkills, "pdf", "tool.sh"), "echo original")
	assertSkillsPublicE2EAbsent(t, filepath.Join(firstSkills, "pdf", "extra.txt"))
	assertSkillsPublicE2EUnchanged(t, secondSkills, secondBefore)
	assertSkillsPublicE2EFile(t, filepath.Join(source, "skills", "pdf", "SKILL.md"), "pdf original")

	writeSkillsPublicE2EFile(t, filepath.Join(source, "skills", "pdf", "SKILL.md"), "pdf updated")
	if err := os.Chmod(filepath.Join(source, "skills", "pdf", "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	service.run(t, projectID, first.Summary.ID, "pdf updated")
	tool, err := os.Stat(filepath.Join(firstSkills, "pdf", "tool.sh"))
	if err != nil || tool.Mode().Perm() != 0o755 {
		t.Fatalf("source executable permission update was lost: %v, %v", tool, err)
	}
	assertSkillsPublicE2EFile(t, filepath.Join(secondSkills, "pdf", "SKILL.md"), "pdf original")
	assertSkillsPublicE2EUnchanged(t, secondSkills, secondBefore)
	spec.Agents[0].Skills = spec.Agents[0].Skills[:1]
	if got := service.apply(t, spec, source); got != projectID {
		t.Fatal("updating skills changed project identity")
	}
	service.run(t, projectID, first.Summary.ID, "pdf updated")
	assertSkillsPublicE2EAbsent(t, filepath.Join(firstSkills, "obsolete"))
	assertSkillsPublicE2EFile(t, filepath.Join(secondSkills, "obsolete", "SKILL.md"), "obsolete original")
	writeSkillsPublicE2EFile(t, filepath.Join(secondSkills, "pdf", "SKILL.md"), "second guest edit")
	assertSkillsPublicE2EFile(t, filepath.Join(firstSkills, "pdf", "SKILL.md"), "pdf updated")
	service.run(t, projectID, second.Summary.ID, "pdf updated")
	assertSkillsPublicE2EAbsent(t, filepath.Join(secondSkills, "obsolete"))
	for _, sandbox := range []*domain.Sandbox{first, second} {
		service.remove(t, sandbox)
	}
	assertSkillsPublicE2EFile(t, filepath.Join(source, "skills", "pdf", "SKILL.md"), "pdf updated")
	assertSkillsPublicE2EFile(t, filepath.Join(source, "skills", "obsolete", "SKILL.md"), "obsolete original")
	assertSkillsPublicE2EFile(t, filepath.Join(source, "workspace", "source.txt"), "workspace source")
}

type skillsPublicE2EService struct {
	root      string
	ctx       context.Context
	config    *appconfig.Config
	store     *sandboxstore.Store
	projects  agentcomposev2connect.ProjectServiceClient
	runs      agentcomposev2connect.RunServiceClient
	sandboxes agentcomposev2connect.SandboxServiceClient
}

func newSkillsPublicE2EService(t *testing.T) skillsPublicE2EService {
	t.Helper()
	root := t.TempDir()
	docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.44")
			_, _ = io.WriteString(w, "OK")
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = io.WriteString(w, `{"Id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","RepoTags":["guest:e2e"],"Os":"linux","Architecture":"amd64","Config":{}}`)
			return
		}
		t.Errorf("unexpected external Docker request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected Docker operation", http.StatusNotImplemented)
	}))
	t.Cleanup(docker.Close)
	for key, value := range map[string]string{
		"DATA_ROOT": filepath.Join(root, "data"), "SANDBOX_ROOT": filepath.Join(root, "sandboxes"),
		"IMAGE_CACHE_ROOT": filepath.Join(root, "images"), "IMAGE_STORE_MODE": "docker", "SANDBOX_ARCHIVE_ROOT": filepath.Join(root, "archives"),
		"RUNTIME_DRIVER": "docker", "DEFAULT_IMAGE": "guest:e2e", "DOCKER_HOST": "tcp://" + strings.TrimPrefix(docker.URL, "http://"),
		"DOCKER_API_VERSION": "1.44", "DOCKER_TLS_VERIFY": "", "DOCKER_CERT_PATH": "",
		"AGENT_COMPOSE_AUTH_TOKEN": "", "LLM_API_ENDPOINT": "", "LLM_API_KEY": "",
		"AGENT_COMPOSE_RUNTIME_BASE_URL": "http://runtime.e2e.invalid", "SANDBOX_START_TIMEOUT": "15s", "SANDBOX_STOP_TIMEOUT": "15s",
	} {
		t.Setenv(key, value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	di := do.New()
	appconfig.Setup(di)
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, slog.New(slog.NewTextHandler(io.Discard, nil)))
	do.ProvideValue(di, echo.New())
	RegisterDependencies(di)
	runtime := &skillsPublicE2ERuntime{alive: make(map[string]bool)}
	do.Override(di, func(do.Injector) (adapters.RuntimeProvider, error) { return runtime, nil })
	RegisterRoutes(di)
	database := do.MustInvoke[*storagesqlite.Database](di)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(do.MustInvoke[*echo.Echo](di))
	t.Cleanup(server.Close)
	return skillsPublicE2EService{
		root: root, ctx: ctx, config: do.MustInvoke[*appconfig.Config](di), store: do.MustInvoke[*sandboxstore.Store](di),
		projects:  agentcomposev2connect.NewProjectServiceClient(server.Client(), server.URL),
		runs:      agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL),
		sandboxes: agentcomposev2connect.NewSandboxServiceClient(server.Client(), server.URL),
	}
}

func (s skillsPublicE2EService) apply(t *testing.T, spec *agentcomposev2.ProjectSpec, source string) string {
	t.Helper()
	response, err := s.projects.ApplyProject(s.ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: spec, Source: &agentcomposev2.ProjectSource{ComposePath: filepath.Join(source, "agent-compose.yaml")},
	}))
	if err != nil {
		t.Fatalf("ApplyProject: %v", err)
	}
	if !response.Msg.GetApplied() || response.Msg.GetProject().GetSummary().GetProjectId() == "" {
		t.Fatalf("project was not applied: %v", response.Msg)
	}
	return response.Msg.GetProject().GetSummary().GetProjectId()
}

func (s skillsPublicE2EService) run(t *testing.T, projectID, sandboxID, want string) *domain.Sandbox {
	t.Helper()
	response, err := s.runs.RunAgent(s.ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId: projectID, AgentName: "worker", SandboxId: sandboxID, Prompt: "Read the declared pdf skill.",
		Source:        agentcomposev2.RunSource_RUN_SOURCE_API,
		CleanupPolicy: agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
	}))
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	run := response.Msg.GetRun()
	if run.GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || !strings.Contains(run.GetOutput()+run.GetResultJson(), want) {
		t.Fatalf("public run did not observe complete skill contents %q: %v", want, run)
	}
	id := run.GetSummary().GetSandboxId()
	if sandboxID != "" && id != sandboxID {
		t.Fatalf("prompt changed sandbox identity: %q != %q", id, sandboxID)
	}
	sandbox, err := s.store.GetSandbox(s.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if sandbox.WorkspaceProvisioning == nil || sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady || sandbox.Workspace == nil || sandbox.Workspace.SnapshotID == "" {
		t.Fatalf("run did not persist Ready ownership: %+v", sandbox.WorkspaceProvisioning)
	}
	workspace := sandbox.Workspace
	content, err := workspaces.FileWorkspaceContentRoot(s.config, domain.WorkspaceConfig{ID: workspace.ID, Type: workspace.Type, ConfigJSON: workspace.ConfigJSON, SnapshotID: workspace.SnapshotID})
	if err != nil {
		t.Fatal(err)
	}
	assertSkillsPublicE2EAbsent(t, filepath.Dir(content))
	assertSkillsPublicE2EAbsent(t, filepath.Dir(content)+".json")
	assertSkillsPublicE2EAbsent(t, filepath.Dir(content)+".lease")
	entries, err := os.ReadDir(filepath.Dir(execution.HostAgentSkillsDir(sandbox)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".skills-update-") || strings.HasPrefix(entry.Name(), ".skills-metadata-") {
			t.Fatalf("successful public run retained a skills staging entry: %s", entry.Name())
		}
	}
	return sandbox
}

func (s skillsPublicE2EService) remove(t *testing.T, sandbox *domain.Sandbox) {
	t.Helper()
	if _, err := s.sandboxes.RemoveSandbox(s.ctx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandbox.Summary.ID, Force: true})); err != nil {
		t.Fatalf("RemoveSandbox: %v", err)
	}
	assertSkillsPublicE2EAbsent(t, execution.HostSandboxDir(sandbox))
}

func (s skillsPublicE2EService) rejectUnmanagedAlias(t *testing.T, projectID string, sandbox *domain.Sandbox) {
	t.Helper()
	alias := filepath.Join(execution.HostSandboxDir(sandbox), "home", ".claude", "skills")
	before := skillsPublicE2EInventory(t, execution.HostAgentSkillsDir(sandbox))
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(alias, "user-skill", "notes.txt")
	writeSkillsPublicE2EFile(t, userFile, "user-owned content")
	response, err := s.runs.RunAgent(s.ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId: projectID, AgentName: "worker", SandboxId: sandbox.Summary.ID, Prompt: "Read the declared pdf skill.",
		Source:        agentcomposev2.RunSource_RUN_SOURCE_API,
		CleanupPolicy: agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
	}))
	if err != nil {
		t.Fatalf("RunAgent with alias conflict: %v", err)
	}
	run := response.Msg.GetRun()
	if run.GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED || !strings.Contains(run.GetOutput(), "is not managed by agent-compose") {
		t.Fatalf("unmanaged alias was not rejected by public run: %v", run)
	}
	assertSkillsPublicE2EFile(t, userFile, "user-owned content")
	assertSkillsPublicE2EUnchanged(t, execution.HostAgentSkillsDir(sandbox), before)
	// Remove only the fixture's directory and restore the previous owned alias.
	if err := os.RemoveAll(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../.agents/skills", alias); err != nil {
		t.Fatal(err)
	}
}

func writeSkillsPublicE2EFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) == "SKILL.md" {
		content = fmt.Sprintf("---\nname: %s\ndescription: Public service skill fixture\n---\n%s", filepath.Base(filepath.Dir(path)), content)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertSkillsPublicE2EFile(t *testing.T, path, want string) {
	t.Helper()
	if filepath.Base(path) == "SKILL.md" {
		want = fmt.Sprintf("---\nname: %s\ndescription: Public service skill fixture\n---\n%s", filepath.Base(filepath.Dir(path)), want)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != want {
		t.Fatalf("%s = %q, want %q: %v", path, content, want, err)
	}
}

func assertSkillsPublicE2EAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected path absent: %s: %v", path, err)
	}
}

func skillsPublicE2EInventory(t *testing.T, root string) map[string]os.FileInfo {
	t.Helper()
	entries := make(map[string]os.FileInfo)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil {
			entries[strings.TrimPrefix(path, root)] = info
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return entries
}

func assertSkillsPublicE2EUnchanged(t *testing.T, root string, before map[string]os.FileInfo) {
	t.Helper()
	after := skillsPublicE2EInventory(t, root)
	if len(before) != len(after) {
		t.Fatal("unchanged skills tree added or removed entries")
	}
	for name, previous := range before {
		current := after[name]
		if current == nil || !os.SameFile(previous, current) || previous.Mode() != current.Mode() || previous.Size() != current.Size() || !previous.ModTime().Equal(current.ModTime()) {
			t.Fatalf("unchanged skill entry was replaced or modified: %s", name)
		}
	}
}

func assertSkillsPublicE2EPrivateCopies(t *testing.T, first, second string) {
	t.Helper()
	firstInfo, err := os.Stat(filepath.Join(first, "pdf", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(filepath.Join(second, "pdf", "SKILL.md"))
	if err != nil || os.SameFile(firstInfo, secondInfo) {
		t.Fatalf("sandboxes do not have private skill files: %v", err)
	}
}

type skillsPublicE2ERuntime struct {
	mu    sync.Mutex
	alive map[string]bool
}

func (r *skillsPublicE2ERuntime) ForDriver(string) (adapters.SandboxRuntime, error) { return r, nil }
func (r *skillsPublicE2ERuntime) ForSession(*domain.Sandbox) (adapters.SandboxRuntime, error) {
	return r, nil
}

func (r *skillsPublicE2ERuntime) EnsureSandbox(_ context.Context, sandbox *domain.Sandbox, _ domain.VMState, _ domain.ProxyState) (domain.SandboxVMInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive[sandbox.Summary.ID] = true
	return domain.SandboxVMInfo{BoxID: "e2e-" + sandbox.Summary.ID}, nil
}

func (r *skillsPublicE2ERuntime) StopSandbox(_ context.Context, sandbox *domain.Sandbox, _ domain.VMState) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive[sandbox.Summary.ID] = false
	return false, nil
}

func (r *skillsPublicE2ERuntime) RemoveSandbox(_ context.Context, sandbox *domain.Sandbox, _ domain.VMState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.alive, sandbox.Summary.ID)
	return nil
}

func (r *skillsPublicE2ERuntime) Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, fmt.Errorf("unexpected non-agent runtime execution")
}

func (r *skillsPublicE2ERuntime) ExecStream(_ context.Context, sandbox *domain.Sandbox, state domain.VMState, _ domain.ExecSpec, stream domain.ExecStreamWriter) (domain.ExecResult, error) {
	r.mu.Lock()
	alive := r.alive[sandbox.Summary.ID]
	r.mu.Unlock()
	if !alive || state.BoxID != "e2e-"+sandbox.Summary.ID {
		return domain.ExecResult{}, fmt.Errorf("agent executed without its persisted running runtime")
	}
	content, err := os.ReadFile(filepath.Join(execution.HostAgentSkillsDir(sandbox), "pdf", "SKILL.md"))
	if err != nil {
		return domain.ExecResult{}, err
	}
	result, err := json.Marshal(map[string]string{"provider": "codex", "threadId": "thread-" + sandbox.Summary.ID, "finalText": string(content), "stopReason": "completed"})
	if err != nil {
		return domain.ExecResult{}, err
	}
	payload := execution.AgentResultPrefix + string(result)
	if stream != nil {
		stream(domain.ExecChunk{Text: payload, Stream: "stdout"})
	}
	return domain.ExecResult{Stdout: payload, Output: payload, Success: true}, nil
}
