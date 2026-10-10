package adapters

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/telemetry"
)

// TestSandboxDriverTracesImagePull covers the guest-image resolution boundary:
// starting a sandbox opens one image.pull span for the resolved driver, which is
// where the Docker driver performs its pull.
func TestSandboxDriverTracesImagePull(t *testing.T) {
	recorder, spans := newDriverSpanRecorder(t)
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		BoxliteHome:          filepath.Join(root, "boxlite"),
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	sandbox, err := store.CreateSandbox(ctx, "image pull", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	driver := NewSandboxDriver(config, store, nil, fakeRuntimeProvider{runtime: fakeSessionRuntime{info: domain.SandboxVMInfo{BoxID: "container-1"}}}, WithSandboxDriverRecorder(recorder))

	if err := driver.StartSandboxVM(ctx, sandbox); err != nil {
		t.Fatalf("StartSandboxVM returned error: %v", err)
	}

	var imageSpanAttributes attribute.Set
	foundSpan := false
	for _, span := range spans.Ended() {
		if span.Name() != telemetry.SpanImagePull {
			continue
		}
		foundSpan = true
		imageSpanAttributes = attribute.NewSet(span.Attributes()...)
	}
	if !foundSpan {
		t.Fatal("no image.pull span recorded")
	}
	if got := metricAttribute(imageSpanAttributes, telemetry.AttrDriver); got != driverpkg.RuntimeDriverBoxlite {
		t.Fatalf("image.pull driver = %q, want %q", got, driverpkg.RuntimeDriverBoxlite)
	}
	if got := metricAttribute(imageSpanAttributes, telemetry.AttrSandboxID); got != sandbox.Summary.ID {
		t.Fatalf("image.pull sandbox id = %q, want %q", got, sandbox.Summary.ID)
	}
}

func metricAttribute(attributes attribute.Set, key attribute.Key) string {
	value, ok := attributes.Value(key)
	if !ok {
		return ""
	}
	return value.AsString()
}
