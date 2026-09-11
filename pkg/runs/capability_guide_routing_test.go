package runs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	"github.com/chaitin/agent-compose/pkg/capability"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

type scopedRunGuideProvider struct {
	scope capabilities.GuideScope
}

func (scopedRunGuideProvider) Status(context.Context) capability.Status { return capability.Status{} }
func (scopedRunGuideProvider) ListCapsets(context.Context) ([]capability.Capset, error) {
	return nil, nil
}
func (scopedRunGuideProvider) Catalog(context.Context, string) (capability.Catalog, error) {
	return capability.Catalog{}, nil
}
func (scopedRunGuideProvider) ProxyTarget() string { return "proxy:1" }
func (scopedRunGuideProvider) CapabilityGuide(_ context.Context, id string) ([]byte, error) {
	return []byte("global " + id), nil
}
func (scopedRunGuideProvider) InvokeConnect(context.Context, capability.InvokeRequest) (json.RawMessage, error) {
	return nil, capability.ErrNotConfigured
}
func (p *scopedRunGuideProvider) CapabilityGuideForScope(_ context.Context, scope capabilities.GuideScope, declaration string) ([]byte, error) {
	p.scope = scope
	if declaration == "public/fail" {
		return nil, errors.New("unavailable")
	}
	return []byte("scoped " + declaration), nil
}

func TestProjectRunCapabilityGuideRoutesManagedScopeAndMergesBestEffort(t *testing.T) {
	provider := &scopedRunGuideProvider{}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "run-sandbox",
		WorkspacePath: filepath.Join(t.TempDir(), "workspace"),
		Tags: []domain.SandboxTag{
			{Name: "project", Value: "project-1"},
			{Name: domain.AgentSandboxTagID, Value: "agent-1"},
		},
	}}

	WriteCapabilityGuide(context.Background(), CapabilityGuideDeps{Provider: provider}, sandbox, []string{"legacy", "internal/dev", "public/fail"})
	guide, err := os.ReadFile(capabilities.SandboxGuidePath(sandbox))
	if err != nil {
		t.Fatalf("read merged guide: %v", err)
	}
	if provider.scope != (capabilities.GuideScope{ProjectID: "project-1", AgentID: "agent-1"}) {
		t.Fatalf("scope = %#v", provider.scope)
	}
	if content := string(guide); !strings.Contains(content, "global legacy") || !strings.Contains(content, "scoped internal/dev") {
		t.Fatalf("merged guide = %s", content)
	}
}

// TestWriteCapabilityGuidePushesToGuestWhenDriverHasNoSharedMount is the
// regression test for the k8s-runtime PR's #4 finding: for a driver with no
// shared filesystem (k8s), the rendered capability catalog was written to
// the host only and never reached the guest at all, since this whole
// call chain had no WriteGuestFile plumbing.
func TestWriteCapabilityGuidePushesToGuestWhenDriverHasNoSharedMount(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:         root,
		SandboxRoot:      filepath.Join(root, "sandboxes"),
		RuntimeDriver:    driverpkg.RuntimeDriverK8s,
		DefaultImage:     "guest:latest",
		GuestRuntimeRoot: "/data/runtime",
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	sandbox, err := store.CreateSandbox(context.Background(), "guest push", "", driverpkg.RuntimeDriverK8s, "guest:latest", "", domain.SandboxTypeScript, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	if err := store.SaveVMState(sandbox.Summary.ID, domain.VMState{Driver: driverpkg.RuntimeDriverK8s, BoxID: sandbox.Summary.ID}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	provider := &scopedRunGuideProvider{}
	runtime := &guestFileControllerRuntime{}
	controller := &Controller{config: config, store: store, runtime: func(*domain.Sandbox) (Runtime, error) { return runtime, nil }}

	WriteCapabilityGuide(context.Background(), CapabilityGuideDeps{
		Provider:       provider,
		Config:         config,
		WriteGuestFile: controller.sandboxGuestFileWriter(sandbox),
	}, sandbox, []string{"legacy"})

	hostGuide, err := os.ReadFile(capabilities.SandboxGuidePath(sandbox))
	if err != nil {
		t.Fatalf("read host guide: %v", err)
	}
	if runtime.writtenPath != "/data/runtime/mpi/catalog.md" {
		t.Fatalf("guest guide path = %q, want /data/runtime/mpi/catalog.md", runtime.writtenPath)
	}
	if string(runtime.writtenData) != string(hostGuide) {
		t.Fatalf("guest guide content = %q, want host content %q", runtime.writtenData, hostGuide)
	}
}
