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
