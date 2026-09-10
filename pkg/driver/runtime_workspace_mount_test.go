package driver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testWorkspaceMountSandbox(t *testing.T, target string, readOnly bool) (*Sandbox, string) {
	t.Helper()
	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(projectRoot, "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "marker.txt"), []byte("source-only"), 0o640); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"mode": "mount", "source_path": source, "project_root": projectRoot, "target": target, "read_only": readOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	sandbox := testRuntimeMountSandbox(filepath.Join(t.TempDir(), "sandbox"))
	sandbox.Workspace = &SandboxWorkspace{Type: "file", ConfigJSON: string(payload)}
	return sandbox, source
}

func TestWorkspaceMountDriverModeMatrix(t *testing.T) {
	for _, driver := range []string{RuntimeDriverDocker, RuntimeDriverBoxlite, RuntimeDriverMicrosandbox, RuntimeDriverK8s} {
		for _, mode := range []string{"", "copy", "mount"} {
			for _, readOnly := range []bool{false, true} {
				name := driver + "/" + mode + "/rw"
				if readOnly {
					name = driver + "/" + mode + "/ro"
				}
				t.Run(name, func(t *testing.T) {
					sandbox, _ := testWorkspaceMountSandbox(t, ".", readOnly)
					if mode != "mount" {
						payload, err := json.Marshal(map[string]any{"mode": mode, "read_only": readOnly})
						if err != nil {
							t.Fatal(err)
						}
						sandbox.Workspace.ConfigJSON = string(payload)
					}
					manifest, err := prepareRuntimeMountManifest(testRuntimeMountConfig(), sandbox, driver)
					wantError := (mode == "mount" && driver != RuntimeDriverDocker) || (mode != "mount" && readOnly)
					if wantError {
						if err == nil {
							t.Fatalf("unsupported workspace combination produced manifest: %+v", manifest)
						}
						if _, statErr := os.Stat(hostSandboxDir(sandbox)); !os.IsNotExist(statErr) {
							t.Fatalf("rejected workspace created sandbox directory: %v", statErr)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if mode != "mount" {
						sandbox.Workspace = nil
						baseline, err := buildRuntimeMountManifest(testRuntimeMountConfig(), sandbox, driver)
						if err != nil || !reflect.DeepEqual(manifest, baseline) {
							t.Fatalf("copy manifest changed: got=%+v baseline=%+v error=%v", manifest, baseline, err)
						}
					}
				})
			}
		}
	}
}

func TestWorkspaceMountManifestRootAndSubdirectory(t *testing.T) {
	for _, target := range []string{".", "reference", "nested/reference"} {
		for _, readOnly := range []bool{false, true} {
			t.Run(target+"/"+map[bool]string{true: "ro", false: "rw"}[readOnly], func(t *testing.T) {
				sandbox, source := testWorkspaceMountSandbox(t, target, readOnly)
				config := testRuntimeMountConfig()
				manifest, err := prepareRuntimeMountManifest(config, sandbox, RuntimeDriverDocker)
				if err != nil {
					t.Fatal(err)
				}
				want := make(map[string]RuntimeMount)
				for _, entry := range runtimeMountEntries(config) {
					want[entry.guestPath] = RuntimeMount{HostPath: logicalRuntimeMountHostPath(sandbox, entry), GuestPath: entry.guestPath, Type: "bind"}
				}
				guestTarget := filepath.Join(config.GuestWorkspacePath, target)
				want[guestTarget] = RuntimeMount{HostPath: source, GuestPath: guestTarget, Type: "bind", ReadOnly: readOnly}
				actual := make(map[string]RuntimeMount)
				for _, mount := range manifest.Mounts {
					if _, duplicate := actual[mount.GuestPath]; duplicate {
						t.Fatalf("duplicate mount target: %s", mount.GuestPath)
					}
					actual[mount.GuestPath] = mount
				}
				if !reflect.DeepEqual(actual, want) {
					t.Fatalf("mounts = %#v, want %#v", actual, want)
				}
				loaded, err := loadRuntimeMountManifest(sandbox, RuntimeDriverDocker)
				if err != nil || !reflect.DeepEqual(manifest, loaded) {
					t.Fatalf("manifest did not round-trip: %+v, %v", loaded, err)
				}
				entries, err := os.ReadDir(sandbox.Summary.WorkspacePath)
				if err != nil || len(entries) != 0 {
					t.Fatalf("workspace mount copied source files: entries=%v, error=%v", entries, err)
				}
				data, err := os.ReadFile(filepath.Join(source, "marker.txt"))
				if err != nil || string(data) != "source-only" {
					t.Fatalf("source was changed: %q, %v", data, err)
				}
			})
		}
	}
}

func TestWorkspaceMountReplacementNormalizesGuestRoot(t *testing.T) {
	for _, configRoot := range []string{"/workspace", "/workspace/", "/unused/../workspace"} {
		for _, specRoot := range []string{"/workspace", "/workspace/", "/./workspace"} {
			for _, target := range []string{".", "reference"} {
				t.Run(configRoot+"/"+specRoot+"/"+target, func(t *testing.T) {
					config := testRuntimeMountConfig()
					config.GuestWorkspacePath = configRoot
					specs := []runtimeMountSpec{
						{hostPath: "/sandbox/workspace", guestPath: specRoot},
						{hostPath: "/sandbox/state", guestPath: "/data/state"},
					}
					before := append([]runtimeMountSpec(nil), specs...)
					workspace := runtimeMountSpec{
						hostPath: "/project/source", guestPath: filepath.Join("/workspace", target), readOnly: true, mustExist: true,
					}
					want := []runtimeMountSpec{workspace, specs[1]}
					if target != "." {
						want = append(append([]runtimeMountSpec(nil), specs...), workspace)
					}
					got := applyWorkspaceRuntimeMount(config, specs, &workspace)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("workspace mounts = %#v, want %#v", got, want)
					}
					if !reflect.DeepEqual(specs, before) {
						t.Fatal("workspace replacement mutated the original mount specs")
					}
				})
			}
		}
	}
}

func TestWorkspaceMountRejectsOverlappingGuestPaths(t *testing.T) {
	for _, target := range []string{"/workspace/reference", "/workspace", "/workspace/reference/child", "/"} {
		t.Run(target, func(t *testing.T) {
			sandbox, _ := testWorkspaceMountSandbox(t, "reference", false)
			sandbox.VolumeMounts = []SandboxVolumeMount{{Type: "bind", HostPath: t.TempDir(), Target: target}}
			if _, err := prepareRuntimeMountManifest(testRuntimeMountConfig(), sandbox, RuntimeDriverDocker); err == nil || !strings.Contains(err.Error(), "overlaps volume target") {
				t.Fatalf("overlap error = %v", err)
			}
		})
	}
	for _, guestRoot := range []string{"/", "/root", "/root/project", "/data", "/data/state", "/data/runtime/repo", "/data/logs"} {
		t.Run("reserved"+guestRoot, func(t *testing.T) {
			sandbox, _ := testWorkspaceMountSandbox(t, ".", false)
			config := testRuntimeMountConfig()
			config.GuestWorkspacePath = guestRoot
			if _, err := prepareRuntimeMountManifest(config, sandbox, RuntimeDriverDocker); err == nil || !strings.Contains(err.Error(), "overlaps") {
				t.Fatalf("reserved overlap error = %v", err)
			}
		})
	}
	for _, target := range []string{"/workspace/other", "/workspace/reference2", "/cache"} {
		t.Run("disjoint"+target, func(t *testing.T) {
			sandbox, _ := testWorkspaceMountSandbox(t, "reference", false)
			sandbox.VolumeMounts = []SandboxVolumeMount{{Type: "bind", HostPath: t.TempDir(), Target: target}}
			if _, err := prepareRuntimeMountManifest(testRuntimeMountConfig(), sandbox, RuntimeDriverDocker); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkspaceMountMissingSourceNeverCreatesDirectory(t *testing.T) {
	sandbox, source := testWorkspaceMountSandbox(t, ".", false)
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRuntimeMountManifest(testRuntimeMountConfig(), sandbox, RuntimeDriverDocker); err == nil {
		t.Fatal("missing source was accepted")
	}
	if err := ensureRuntimeMountSource(runtimeMountSpec{hostPath: source, mustExist: true}); err == nil {
		t.Fatal("missing external mount source was recreated")
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source path changed: %v", err)
	}
}

func TestWorkspaceMountRejectsNonFileProviderAndUnknownModes(t *testing.T) {
	sandbox, _ := testWorkspaceMountSandbox(t, ".", false)
	sandbox.Workspace.Type = "git"
	if _, err := prepareRuntimeMountManifest(testRuntimeMountConfig(), sandbox, RuntimeDriverDocker); err == nil || !strings.Contains(err.Error(), "requires provider file") {
		t.Fatalf("git mount error = %v", err)
	}
	sandbox.Workspace.Type = "file"
	sandbox.Workspace.ConfigJSON = `{"mode":"map"}`
	if _, err := prepareRuntimeMountManifest(testRuntimeMountConfig(), sandbox, RuntimeDriverDocker); err == nil {
		t.Fatal("unknown mode silently fell back to copy")
	}
	if _, err := os.Stat(hostSandboxDir(sandbox)); !os.IsNotExist(err) {
		t.Fatalf("invalid workspace created sandbox directory: %v", err)
	}
}

func TestWorkspaceMountManifestRejectsEveryStaleFieldAndExtraMount(t *testing.T) {
	sandbox, _ := testWorkspaceMountSandbox(t, ".", true)
	config := testRuntimeMountConfig()
	manifest, err := prepareRuntimeMountManifest(config, sandbox, RuntimeDriverDocker)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaceRuntimeMountSpec(config, sandbox, RuntimeDriverDocker)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkspaceRuntimeManifest(config, sandbox, manifest, workspace); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*RuntimeMountManifest){
		"source":   func(m *RuntimeMountManifest) { m.Mounts[0].HostPath += "/stale" },
		"target":   func(m *RuntimeMountManifest) { m.Mounts[0].GuestPath = "/old-workspace" },
		"readonly": func(m *RuntimeMountManifest) { m.Mounts[0].ReadOnly = false },
		"type":     func(m *RuntimeMountManifest) { m.Mounts[0].Type = "volume" },
		"missing":  func(m *RuntimeMountManifest) { m.Mounts = m.Mounts[1:] },
		"extra": func(m *RuntimeMountManifest) {
			m.Mounts = append(m.Mounts, RuntimeMount{Type: "bind", HostPath: "/stale", GuestPath: "/workspace/stale"})
		},
		"duplicate":      func(m *RuntimeMountManifest) { m.Mounts[1] = m.Mounts[0] },
		"runtime source": func(m *RuntimeMountManifest) { m.Mounts[1].HostPath += "/stale" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := manifest
			changed.Mounts = append([]RuntimeMount(nil), manifest.Mounts...)
			mutate(&changed)
			if err := validateWorkspaceRuntimeManifest(config, sandbox, changed, workspace); err == nil {
				t.Fatal("stale manifest accepted")
			}
		})
	}
}
