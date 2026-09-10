package workspaces

import (
	"context"
	"encoding/json"
	"path/filepath"

	"errors"
	"fmt"
	"github.com/google/uuid"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestIntegrationTransientSnapshotLeaseCrossProcess(t *testing.T) {
	if childRoot := os.Getenv("AGENT_COMPOSE_SNAPSHOT_LEASE_TEST_ROOT"); childRoot != "" {
		cleaner := &TransientSnapshotCleaner{Config: &appconfig.Config{DataRoot: childRoot}, Store: transientSnapshotReader{}}
		result, err := cleaner.Clean(context.Background(), time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("removed=%d skipped=%d\n", result.Removed, result.Skipped)
		return
	}
	config := &appconfig.Config{DataRoot: t.TempDir()}
	_, workspace, root := newTransientSnapshotFixture(t, config)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	probe := func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestIntegrationTransientSnapshotLeaseCrossProcess$")
		cmd.Env = append(os.Environ(), "AGENT_COMPOSE_SNAPSHOT_LEASE_TEST_ROOT="+config.DataRoot)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	output, err := probe()
	if err != nil || !strings.Contains(output, "removed=0 skipped=1") {
		t.Fatalf("second daemon collected live preparation: %s %v", output, err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
	// Releasing the preparation lease models both normal close and process exit.
	if err := CloseTransientSnapshotLease(workspace); err != nil {
		t.Fatal(err)
	}
	const workers = 3
	type result struct {
		output string
		err    error
	}
	results := make(chan result, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	start := make(chan struct{})
	for range workers {
		go func() { ready.Done(); <-start; output, err := probe(); results <- result{output, err} }()
	}
	ready.Wait()
	close(start)
	removed := 0
	for range workers {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent cleaner: %s %v", result.output, result.err)
		}
		if strings.Contains(result.output, "removed=1 ") {
			removed++
		}
	}
	if removed != 1 {
		t.Fatalf("generation removed by %d cleaners", removed)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unowned generation survived cleanup: %v", err)
	}
	output, err = probe()
	if err != nil || !strings.Contains(output, "removed=0 skipped=0") {
		t.Fatalf("lease path recreated after removal: %s %v", output, err)
	}
}

func TestTransientSnapshotLeaseCloseIsIdempotentAndCancellationDoesNotLeakDescriptor(t *testing.T) {
	config := &appconfig.Config{DataRoot: t.TempDir()}
	_, workspace, root := newTransientSnapshotFixture(t, config)
	lease := workspace.SnapshotLease.(*transientSnapshotLease)
	file := lease.file
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ReleaseUnusedTransientSnapshot(ctx, config, nil, workspace); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preparation cleanup = %v", err)
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("preparation descriptor still open: %v", err)
	}
	if err := CloseTransientSnapshotLease(workspace); err != nil {
		t.Fatalf("second close = %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
	cleaner := &TransientSnapshotCleaner{Config: config, Store: transientSnapshotReader{}}
	result, err := cleaner.Clean(context.Background(), time.Time{})
	if err != nil || result.Removed != 1 {
		t.Fatalf("recover canceled source = %#v %v", result, err)
	}
}

func TestTransientSnapshotLeaseNeverSerializes(t *testing.T) {
	config := &appconfig.Config{DataRoot: t.TempDir()}
	cfg, workspace, _ := newTransientSnapshotFixture(t, config)
	if cfg.SnapshotLease == nil || workspace.SnapshotLease == nil {
		t.Fatal("preparation lost lease")
	}
	filtered := WorkspaceForConfigurationHash(workspace)
	if filtered.SnapshotLease != nil || filtered.SnapshotID != "" {
		t.Fatal("physical snapshot ownership reached sticky hash")
	}
	// Persisted references are sufficient after creation commits; the open handle
	// belongs to preparation only and must not be restored from untrusted JSON.
	payload, err := json.Marshal(workspace)
	if err != nil {
		t.Fatal(err)
	}
	var decoded domain.SandboxWorkspace
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SnapshotLease != nil || decoded.SnapshotID != workspace.SnapshotID {
		t.Fatalf("persisted workspace = %#v", decoded)
	}
}

func TestTransientSnapshotCrashBeforeOwnershipPublicationHasNoContent(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			config := &appconfig.Config{DataRoot: t.TempDir()}
			root, err := openTransientSnapshotRoot(config)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			id := uuid.NewString()
			lease, acquired, err := acquireTransientSnapshotLease(root, id, true)
			if err != nil || !acquired {
				t.Fatalf("allocate lease: %v %v", acquired, err)
			}
			if partial {
				if err := root.WriteFile(id+".json", []byte(`{"version":`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			cleaner := &TransientSnapshotCleaner{Config: config, Store: transientSnapshotReader{}}
			result, err := cleaner.Clean(context.Background(), time.Time{})
			if (err != nil) != partial || result.Removed != 0 {
				t.Fatalf("interrupted ownership cleanup = %#v %v", result, err)
			}
			if _, err := root.Stat(id); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("content allocated before ownership marker: %v", err)
			}
			info, err := os.Stat(filepath.Join(config.DataRoot, transientSnapshotDirectory, id+".lease"))
			if err != nil || info.Size() != 0 {
				t.Fatalf("pre-publication residue must be empty lock metadata: %v %v", info, err)
			}
		})
	}
}
