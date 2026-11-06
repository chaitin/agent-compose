package runs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestRuntimeCatalogClearsOldToolsWhenNoCapsetsRemain(t *testing.T) {
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox", WorkspacePath: filepath.Join(t.TempDir(), "workspace")}}
	filePath := capabilities.SandboxCatalogPath(sandbox)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(`{"tools":[{"id":"stale"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var pushedPath string
	var pushed []byte
	WriteCapabilityGuide(context.Background(), CapabilityGuideDeps{
		Config: &appconfig.Config{GuestRuntimeRoot: "/custom/runtime"},
		WriteGuestFile: func(_ context.Context, path string, content []byte) error {
			pushedPath, pushed = path, content
			return nil
		},
	}, sandbox, nil)
	var catalog capabilities.RuntimeCatalog
	if err := json.Unmarshal(pushed, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tools) != 0 || catalog.SandboxID != "sandbox" || pushedPath != "/custom/runtime/mpi/capabilities.json" {
		t.Fatalf("catalog = %#v, path = %s", catalog, pushedPath)
	}
	local, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(local) != string(pushed) {
		t.Fatal("host and guest catalogs differ")
	}
}

func TestRuntimeCatalogDoesNotFollowGuestSymlinks(t *testing.T) {
	for _, entry := range []string{"capabilities.json", "catalog.md", "mpi"} {
		t.Run(entry, func(t *testing.T) {
			sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox", WorkspacePath: filepath.Join(t.TempDir(), "workspace")}}
			filePath := capabilities.SandboxCatalogPath(sandbox)
			if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			name := entry
			if entry == "mpi" {
				name = "catalog.md"
			}
			target := filepath.Join(outside, name)
			if err := os.WriteFile(target, []byte("outside-must-not-change"), 0o644); err != nil {
				t.Fatal(err)
			}
			link, destination := filepath.Join(filepath.Dir(filePath), entry), target
			if entry == "mpi" {
				link, destination = filepath.Dir(filePath), outside
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(destination, link); err != nil {
				t.Fatal(err)
			}
			WriteCapabilityGuide(context.Background(), CapabilityGuideDeps{Provider: &scopedRunGuideProvider{}}, sandbox, []string{"dev"})
			actual, err := os.ReadFile(target)
			if err != nil || string(actual) != "outside-must-not-change" {
				t.Fatal("catalog publication overwrote a guest symlink target outside the runtime")
			}
			if entry != "mpi" {
				info, err := os.Lstat(link)
				if err != nil || !info.Mode().IsRegular() {
					t.Fatal("catalog was not replaced with a regular snapshot file")
				}
			}
		})
	}
}
