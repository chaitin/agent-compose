package workspaces

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestFileWorkspaceMountSnapshotContract(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeProvisionerFileStateFile(t, filepath.Join(source, "nested", "marker"), "source data", 0o640)
	writeProvisionerFileStateFile(t, filepath.Join(root, "compose.yml"), "agents: {}\n", 0o600)
	before := snapshotProvisionerFileStateManifest(t, root)
	for _, target := range []string{".", "reference"} {
		for _, readOnly := range []bool{false, true} {
			raw, err := NewFileWorkspaceMountConfig(domain.ProjectRecord{SourcePath: filepath.Join(root, "compose.yml")}, "source", target, readOnly)
			if err != nil {
				t.Fatal(err)
			}
			mount, err := DecodeFileWorkspaceMount(raw)
			if err != nil || mount == nil {
				t.Fatalf("decode = %#v, %v", mount, err)
			}
			canonicalRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			want := FileWorkspaceMountConfig{
				Mode: domain.WorkspaceModeMount, SourcePath: filepath.Join(canonicalRoot, "source"),
				ProjectRoot: canonicalRoot, Target: target, ReadOnly: readOnly,
			}
			if !reflect.DeepEqual(*mount, want) {
				t.Fatalf("snapshot = %#v, want %#v", *mount, want)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &fields); err != nil {
				t.Fatal(err)
			}
			wantFields := 4
			if readOnly {
				wantFields++
			}
			if len(fields) != wantFields {
				t.Fatalf("snapshot fields changed: %s", raw)
			}
			if _, err := FileWorkspaceContentRoot(&appconfig.Config{DataRoot: root}, domain.WorkspaceConfig{ID: "mount", Type: "file", ConfigJSON: raw}); err == nil {
				t.Fatal("mount snapshot was accepted as managed preset content")
			}
		}
	}
	if after := snapshotProvisionerFileStateManifest(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("snapshot creation modified the source tree:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestFileWorkspaceMountRejectsInvalidPathsWithoutSideEffects(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeProvisionerFileStateFile(t, filepath.Join(root, "source", "marker"), "private", 0o600)
	writeProvisionerFileStateFile(t, filepath.Join(outside, "nested", "marker"), "external", 0o600)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "source"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	before := snapshotProvisionerFileStateManifest(t, root)
	for _, pair := range [][2]string{
		{"", "."}, {"../source", "."}, {outside, "."}, {"missing", "."},
		{"source/marker", "."}, {"escape/nested", "."}, {"alias", "."},
		{"source", "../outside"}, {"source", "/workspace"},
	} {
		if _, err := NewFileWorkspaceMountConfig(domain.ProjectRecord{SourcePath: root}, pair[0], pair[1], false); err == nil {
			t.Fatalf("source=%q target=%q unexpectedly accepted", pair[0], pair[1])
		}
	}
	if after := snapshotProvisionerFileStateManifest(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("invalid mount preparation changed project data")
	}
	assertProvisionerFileStateFile(t, filepath.Join(outside, "nested", "marker"), "external", 0o600)
}

func TestFileWorkspaceMountRequiresExistingProjectSource(t *testing.T) {
	root := t.TempDir()
	writeProvisionerFileStateFile(t, filepath.Join(root, "parent-marker"), "must not mount parent", 0o600)
	for _, name := range []string{"removed-project", "missing-compose.yml"} {
		if _, err := NewFileWorkspaceMountConfig(domain.ProjectRecord{SourcePath: filepath.Join(root, name)}, ".", ".", false); err == nil {
			t.Fatalf("missing project source %s unexpectedly mounted its parent", name)
		}
	}
	assertProvisionerFileStateFile(t, filepath.Join(root, "parent-marker"), "must not mount parent", 0o600)
}

func TestDecodeFileWorkspaceMountRejectsBrokenDelivery(t *testing.T) {
	for _, raw := range []string{
		"{", `{"mode":42}`, `{"mode":"future"}`, `{"mode":"copy","read_only":true}`,
		`{"source_path":"/project/source"}`, `{"mode":"mount"}`,
		`{"mode":"mount","source_path":"relative","project_root":"/project"}`,
		`{"mode":"mount","source_path":"/outside","project_root":"/project"}`,
		`{"mode":"mount","source_path":"/project/source","project_root":"/project","root":"/managed"}`,
		`{"mode":"mount","source_path":"/project/source","project_root":"/project","target":"../escape"}`,
	} {
		if _, err := DecodeFileWorkspaceMount(raw); err == nil {
			t.Fatalf("invalid snapshot accepted: %s", raw)
		}
	}
	for _, raw := range []string{"", `{}`, `{"root":"/legacy/root"}`, `{"mode":"copy","read_only":false}`} {
		got, err := DecodeFileWorkspaceMount(raw)
		if err != nil || got != nil {
			t.Fatalf("copy snapshot %q = %#v, %v", raw, got, err)
		}
	}
}

func TestWorkspaceMountRuntimeMatrix(t *testing.T) {
	root := t.TempDir()
	raw, err := NewFileWorkspaceMountConfig(domain.ProjectRecord{SourcePath: root}, ".", ".", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, driver := range []string{"docker", "boxlite", "microsandbox", "k8s", "future"} {
		for _, provider := range []string{"file", "git"} {
			for _, config := range []string{"{}", raw} {
				err := ValidateWorkspaceRuntimeDriver(&domain.SandboxWorkspace{Type: provider, ConfigJSON: config}, driver)
				valid := config == "{}" || provider == "file" && driver == "docker"
				if (err == nil) != valid {
					t.Fatalf("provider=%s driver=%s config=%s: %v", provider, driver, config, err)
				}
			}
		}
	}
}
