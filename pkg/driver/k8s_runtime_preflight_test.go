//go:build k8scompose

package driver

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// TestK8sPreflightReportsAnUnusableKubeconfigAsAFailedPrecondition pins the
// contract the sandbox start path relies on: an unreadable kubeconfig is
// refused by the preflight, classified as a precondition failure, and never
// reported as a generic start error that looks like a Pod that did not become
// Ready. Before the preflight existed there was no call to make at all, so a
// start against an unconfigured cluster created runtime state first.
func TestK8sPreflightReportsAnUnusableKubeconfigAsAFailedPrecondition(t *testing.T) {
	config := &appconfig.Config{
		K8sNamespace:      "default",
		K8sKubeconfigPath: filepath.Join(t.TempDir(), "missing-kubeconfig.yaml"),
	}
	runtime, err := newK8sRuntime(config, nil)
	if err != nil {
		t.Fatalf("newK8sRuntime returned error: %v", err)
	}
	preflight, ok := runtime.(interface {
		PreflightSandboxRuntime(context.Context, *Sandbox, VMState) error
	})
	if !ok {
		t.Fatalf("k8s runtime %T does not implement PreflightSandboxRuntime", runtime)
	}

	err = preflight.PreflightSandboxRuntime(context.Background(), &Sandbox{Summary: SandboxSummary{ID: "sandbox-preflight"}}, VMState{})
	if err == nil {
		t.Fatal("PreflightSandboxRuntime accepted an unreadable kubeconfig")
	}
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("PreflightSandboxRuntime error = %v, want a failed precondition", err)
	}
}
