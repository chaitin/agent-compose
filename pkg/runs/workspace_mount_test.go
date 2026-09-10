package runs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

func TestProjectRunWorkspaceMountSkipsSnapshotAndPreservesExistingContent(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "read-write", true: "read-only"}[readOnly], func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "project", "source")
			if err := os.MkdirAll(source, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "input.txt"), []byte("shared source"), 0o640); err != nil {
				t.Fatal(err)
			}
			config := &appconfig.Config{DataRoot: filepath.Join(root, "data"), RuntimeDriver: "docker"}
			controller := &Controller{config: config}
			run := domain.ProjectRunRecord{RunID: "run-mount", AgentName: "worker"}
			project := domain.ProjectRecord{SourcePath: filepath.Dir(source)}
			workspaceID := WorkspaceID(run, "local")
			contentRoot, err := workspaces.DefaultFileWorkspaceContentRoot(config, workspaceID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(contentRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(contentRoot, "previous-copy.txt")
			if err := os.WriteFile(sentinel, []byte("preserve previous copy"), 0o600); err != nil {
				t.Fatal(err)
			}
			spec := &compose.WorkspaceSpec{Provider: "file", Path: "source", Target: "inputs", Mode: "mount", ReadOnly: readOnly}
			original := *spec
			workspace, err := controller.prepareProjectRunWorkspace(context.Background(), run, project, WorkspaceRequest{
				Project: &compose.WorkspaceSpec{Provider: "file", Path: "."}, Agent: spec,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*spec, original) {
				t.Fatalf("caller workspace changed: got %#v, want %#v", *spec, original)
			}
			if workspace.ID != workspaceID || workspace.Name != WorkspaceName(run, "local") || workspace.Type != "file" {
				t.Fatalf("workspace identity = %#v", workspace)
			}
			var mount struct {
				Mode        string `json:"mode"`
				SourcePath  string `json:"source_path"`
				ProjectRoot string `json:"project_root"`
				Target      string `json:"target"`
				ReadOnly    bool   `json:"read_only"`
			}
			if err := json.Unmarshal([]byte(workspace.ConfigJSON), &mount); err != nil {
				t.Fatal(err)
			}
			canonicalSource, err := filepath.EvalSymlinks(source)
			if err != nil {
				t.Fatal(err)
			}
			if mount.Mode != "mount" || mount.SourcePath != canonicalSource || mount.ProjectRoot != filepath.Dir(canonicalSource) || mount.Target != "inputs" || mount.ReadOnly != readOnly {
				t.Fatalf("mount configuration = %#v", mount)
			}
			entries, err := os.ReadDir(contentRoot)
			if err != nil || len(entries) != 1 || entries[0].Name() != "previous-copy.txt" {
				t.Fatalf("mount copied or reset source snapshot: entries=%v err=%v", entries, err)
			}
			if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve previous copy" {
				t.Fatalf("previous copy = %q, err=%v", data, err)
			}
			if err := os.RemoveAll(filepath.Join(config.DataRoot, "workspaces")); err != nil {
				t.Fatal(err)
			}
			if _, err := controller.prepareProjectRunWorkspace(context.Background(), run, project, WorkspaceRequest{Project: spec}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(config.DataRoot, "workspaces")); !os.IsNotExist(err) {
				t.Fatalf("mount created workspace snapshot storage: %v", err)
			}
		})
	}
}

func TestProjectRunWorkspaceMountRejectsInvalidDeliveryBeforeSnapshot(t *testing.T) {
	for _, test := range []struct {
		name          string
		spec          compose.WorkspaceSpec
		driver        string
		defaultDriver string
		wantError     string
	}{
		{name: "unknown-mode", spec: compose.WorkspaceSpec{Provider: "file", Path: ".", Mode: "map"}, wantError: "mode"},
		{name: "git-mount", spec: compose.WorkspaceSpec{Provider: "git", URL: "https://example.invalid/source", Mode: "mount"}, wantError: "file"},
		{name: "copy-read-only", spec: compose.WorkspaceSpec{Provider: "file", Path: ".", ReadOnly: true}, wantError: "read_only"},
		{name: "explicit-copy-read-only", spec: compose.WorkspaceSpec{Provider: "file", Path: ".", Mode: "copy", ReadOnly: true}, wantError: "read_only"},
		{name: "boxlite", spec: compose.WorkspaceSpec{Provider: "file", Path: ".", Mode: "mount"}, driver: "boxlite", wantError: "docker"},
		{name: "microsandbox", spec: compose.WorkspaceSpec{Provider: "file", Path: ".", Mode: "mount"}, driver: "microsandbox", wantError: "docker"},
		{name: "k8s", spec: compose.WorkspaceSpec{Provider: "file", Path: ".", Mode: "mount"}, driver: "k8s", wantError: "docker"},
		{name: "default-non-docker", spec: compose.WorkspaceSpec{Provider: "file", Path: ".", Mode: "mount"}, defaultDriver: "boxlite", wantError: "docker"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			config := &appconfig.Config{DataRoot: filepath.Join(root, "data"), RuntimeDriver: test.defaultDriver}
			controller := &Controller{config: config}
			_, err := controller.prepareProjectRunWorkspace(context.Background(), domain.ProjectRunRecord{RunID: "invalid-mount", Driver: test.driver}, domain.ProjectRecord{SourcePath: root}, WorkspaceRequest{Agent: &test.spec})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want containing %q", err, test.wantError)
			}
			if _, err := os.Stat(config.DataRoot); !os.IsNotExist(err) {
				t.Fatalf("invalid workspace wrote data root: %v", err)
			}
		})
	}
}

func TestProjectRunWorkspaceMountHonorsDockerOverride(t *testing.T) {
	root := t.TempDir()
	controller := &Controller{config: &appconfig.Config{DataRoot: filepath.Join(t.TempDir(), "data"), RuntimeDriver: "boxlite"}}
	workspace, err := controller.prepareProjectRunWorkspace(context.Background(), domain.ProjectRunRecord{RunID: "docker-override", Driver: "docker"}, domain.ProjectRecord{SourcePath: root}, WorkspaceRequest{
		Agent: &compose.WorkspaceSpec{Provider: "file", Path: ".", Mode: "mount"},
	})
	if err != nil || workspace == nil {
		t.Fatalf("docker override did not resolve mount: workspace=%#v err=%v", workspace, err)
	}
	if _, err := os.Stat(controller.config.DataRoot); !os.IsNotExist(err) {
		t.Fatalf("mount created snapshot storage: %v", err)
	}
}

func TestProjectRunExplicitCopyPreservesHistoricalWorkspaceConfiguration(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: filepath.Join(t.TempDir(), "data")}
	controller := &Controller{config: config}
	run := domain.ProjectRunRecord{RunID: "copy-default"}
	project := domain.ProjectRecord{SourcePath: root}
	implicit, err := controller.prepareProjectRunWorkspace(context.Background(), run, project, WorkspaceRequest{
		Project: &compose.WorkspaceSpec{Provider: "file", Path: ".", Mode: "mount"},
		Agent:   &compose.WorkspaceSpec{Provider: "file", Path: "."},
	})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := controller.prepareProjectRunWorkspace(context.Background(), run, project, WorkspaceRequest{Agent: &compose.WorkspaceSpec{Provider: "file", Path: ".", Mode: "copy"}})
	if err != nil {
		t.Fatal(err)
	}
	if implicit.SnapshotID == "" || explicit.SnapshotID == "" || implicit.SnapshotID == explicit.SnapshotID {
		t.Fatalf("copy preparations must have distinct owned generations")
	}
	if err := workspaces.CloseTransientSnapshotLease(toSandboxWorkspaceSnapshot(*implicit)); err != nil {
		t.Fatal(err)
	}
	if err := workspaces.CloseTransientSnapshotLease(toSandboxWorkspaceSnapshot(*explicit)); err != nil {
		t.Fatal(err)
	}
	implicit.SnapshotID, explicit.SnapshotID = "", ""
	implicit.SnapshotLease, explicit.SnapshotLease = nil, nil
	if !reflect.DeepEqual(implicit, explicit) {
		t.Fatalf("explicit copy changed historical configuration: implicit=%#v explicit=%#v", implicit, explicit)
	}
}

func TestProjectRunFailedCopyAndPreCreateFailureReleaseGeneration(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: t.TempDir()}
	controller := &Controller{config: config}
	if err := os.WriteFile(filepath.Join(root, "a-file"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a-file", filepath.Join(root, "z-link")); err != nil {
		t.Fatal(err)
	}
	request := WorkspaceRequest{Agent: &compose.WorkspaceSpec{Provider: "file", Path: "."}}
	if _, err := controller.prepareProjectRunWorkspace(context.Background(), domain.ProjectRunRecord{RunID: "copy-fails"}, domain.ProjectRecord{SourcePath: root}, request); err == nil {
		t.Fatal("symlink copy unexpectedly succeeded")
	}
	assertEmpty := func() {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(config.DataRoot, "workspace-snapshots"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("unused generation leaked: %v %v", entries, err)
		}
	}
	assertEmpty()
	if err := os.Remove(filepath.Join(root, "z-link")); err != nil {
		t.Fatal(err)
	}
	cfg, err := controller.prepareProjectRunWorkspace(context.Background(), domain.ProjectRunRecord{RunID: "create-fails"}, domain.ProjectRecord{SourcePath: root}, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.ensureProjectRunSandbox(context.Background(), domain.ProjectRunRecord{}, Preparation{Workspace: toSandboxWorkspaceSnapshot(*cfg)}, RunAgentRequest{}); err == nil {
		t.Fatal("missing runtime dependencies accepted")
	}
	assertEmpty()
}
