package sandboxes_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
)

type removalAccessProbe struct {
	t       *testing.T
	revoked string
}

type journalFailureRuntime struct {
	removalTestRuntime
	journal string
}

func (r *journalFailureRuntime) StopSandboxVM(context.Context, *domain.Sandbox) error {
	if err := os.Remove(r.journal); err != nil {
		return err
	}
	// Make the next journal rename fail after the runtime has stopped.
	return os.Mkdir(r.journal, 0o700)
}

func TestRemovalRevokesAccessBeforePersistingTheStoppedStage(t *testing.T) {
	root := t.TempDir()
	sandbox := removalTestSandbox(t, removalTestSandboxSpec{Root: root, ID: "journal-failure", Status: domain.VMStatusRunning, Updated: time.Now()})
	journal, err := sandboxes.OwnershipRecordPath(root, sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &journalFailureRuntime{journal: journal}
	probe := &removalAccessProbe{t: t}
	coordinator := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: &removalTestStore{sandboxes: map[string]*domain.Sandbox{sandbox.Summary.ID: sandbox}}, Runtime: runtime, AccessRevoker: probe}
	result, err := coordinator.Remove(context.Background(), sandbox.Summary.ID, true)
	if err == nil || !result.Stopped || runtime.removeCalls != 0 {
		t.Fatalf("journal failure not exercised: stopped=%t removeCalls=%d err=%v", result.Stopped, runtime.removeCalls, err)
	}
	if probe.revoked != sandbox.Summary.ID {
		t.Fatal("stopped runtime retained capability access after journal failure")
	}
}

func (p *removalAccessProbe) RevokeSandbox(id string) { p.revoked = id }

func (p *removalAccessProbe) ReleaseSandboxResources(_ context.Context, id string) error {
	if p.revoked != id {
		p.t.Error("access remained active after runtime removal")
	}
	return errors.New("accessory release failed")
}

func TestRemovalWithdrawsCapabilityAccessWhenTheRuntimeIsStopped(t *testing.T) {
	for _, stage := range []string{"success", "stop-fails", "remove-fails", "accessories-fail"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			sandbox := removalTestSandbox(t, removalTestSandboxSpec{Root: root, ID: "access-test", Status: domain.VMStatusRunning, Updated: time.Now()})
			store := &removalTestStore{sandboxes: map[string]*domain.Sandbox{sandbox.Summary.ID: sandbox}}
			runtime := &removalTestRuntime{}
			probe := &removalAccessProbe{t: t}
			coordinator := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: runtime, AccessRevoker: probe}
			switch stage {
			case "stop-fails":
				runtime.stopErrors = []error{errors.New("stop failed")}
			case "remove-fails":
				runtime.removeErrors = []error{errors.New("remove failed")}
			case "accessories-fail":
				coordinator.Accessories = probe
			}
			result, err := coordinator.Remove(context.Background(), sandbox.Summary.ID, true)
			if stage == "success" {
				if err != nil || !result.Removed {
					t.Fatalf("removal failed: %v", err)
				}
			} else if err == nil {
				t.Fatal("injected removal failure not returned")
			}
			if stage == "stop-fails" {
				if probe.revoked != "" {
					t.Fatal("failed stop revoked a still-running sandbox")
				}
			} else if probe.revoked != sandbox.Summary.ID {
				t.Fatal("stopped/removed runtime retained capability access")
			}
			if stage == "accessories-fail" {
				probe.revoked = ""
				coordinator.Accessories = nil
				if result, err := coordinator.Remove(context.Background(), sandbox.Summary.ID, true); err != nil || !result.Removed || probe.revoked != sandbox.Summary.ID {
					t.Fatal("resumed deletion did not revoke access")
				}
			}
		})
	}
}
