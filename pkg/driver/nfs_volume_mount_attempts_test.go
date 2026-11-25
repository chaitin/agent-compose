package driver

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/nfsvolume"
)

// blockingNFSMounts stubs the kernel mount with one that blocks until the
// test releases it, and records mounts, unmounts and the mount table.
type blockingNFSMounts struct {
	mu       sync.Mutex
	state    map[string]bool
	mounts   int
	unmounts int
	started  chan struct{}
	release  chan error
}

func stubBlockingNFSMounts(t *testing.T) *blockingNFSMounts {
	t.Helper()
	stub := &blockingNFSMounts{state: map[string]bool{}, started: make(chan struct{}, 8), release: make(chan error)}
	originalMounter, originalState, originalUnmounter := nfsVolumeMounter, nfsVolumeMountStateOf, boxliteVolumeBridgeUnmounter
	nfsVolumeMounter = func(_ context.Context, _ nfsvolume.Source, mountPoint string, _ bool) error {
		stub.mu.Lock()
		stub.mounts++
		stub.mu.Unlock()
		stub.started <- struct{}{}
		if err := <-stub.release; err != nil {
			return err
		}
		stub.mu.Lock()
		stub.state[mountPoint] = true
		stub.mu.Unlock()
		return nil
	}
	nfsVolumeMountStateOf = func(path string) (bool, bool, error) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		return stub.state[path], stub.state[path], nil
	}
	boxliteVolumeBridgeUnmounter = func(path string) error {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		stub.unmounts++
		delete(stub.state, path)
		return nil
	}
	t.Cleanup(func() {
		nfsVolumeMounter, nfsVolumeMountStateOf, boxliteVolumeBridgeUnmounter = originalMounter, originalState, originalUnmounter
	})
	return stub
}

func (s *blockingNFSMounts) counts() (mounts, unmounts int, mounted bool, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p, ok := range s.state {
		if ok {
			mounted, path = true, p
		}
	}
	return s.mounts, s.unmounts, mounted, path
}

func waitForNoNFSAttempt(t *testing.T, mountPoint string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		nfsMountAttempts.Lock()
		_, inFlight := nfsMountAttempts.byPoint[mountPoint]
		nfsMountAttempts.Unlock()
		if !inFlight {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("nfs mount attempt on %s did not finish", mountPoint)
}

func TestNFSMountAttemptReleasesMountThatSucceedsAfterCancel(t *testing.T) {
	root := t.TempDir()
	session, _ := testNFSSandbox(t, root, false)
	mountPoint := filepath.Join(root, "volumes", "mount-nfs0001")
	stub := stubBlockingNFSMounts(t)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- ensureSandboxNFSMounts(ctx, session) }()
	<-stub.started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ensure = %v", err)
	}
	stub.release <- nil // the kernel mount completes after the caller left
	waitForNoNFSAttempt(t, mountPoint)
	if mounts, unmounts, mounted, _ := stub.counts(); mounts != 1 || unmounts != 1 || mounted {
		t.Fatalf("mounts=%d unmounts=%d mounted=%v, want the late mount released", mounts, unmounts, mounted)
	}

	// The next start mounts it again instead of trusting a released mount.
	go func() { result <- ensureSandboxNFSMounts(context.Background(), session) }()
	<-stub.started
	stub.release <- nil
	if err := <-result; err != nil {
		t.Fatalf("retry ensure: %v", err)
	}
	if mounts, unmounts, mounted, path := stub.counts(); mounts != 2 || unmounts != 1 || !mounted || path != mountPoint {
		t.Fatalf("mounts=%d unmounts=%d mounted=%v path=%s after retry", mounts, unmounts, mounted, path)
	}
}

func TestNFSMountAttemptIsSharedByConcurrentCallers(t *testing.T) {
	root := t.TempDir()
	session, _ := testNFSSandbox(t, root, false)
	stub := stubBlockingNFSMounts(t)

	// The first caller gives up while the mount is in flight; a retry joins
	// the same attempt instead of mounting the directory a second time, and
	// keeps the mount when it lands.
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- ensureSandboxNFSMounts(firstCtx, session) }()
	<-stub.started

	second := make(chan error, 1)
	go func() { second <- ensureSandboxNFSMounts(context.Background(), session) }()
	mountPoint := filepath.Join(root, "volumes", "mount-nfs0001")
	deadline := time.Now().Add(5 * time.Second)
	for {
		nfsMountAttempts.Lock()
		waiters := 0
		if attempt := nfsMountAttempts.byPoint[mountPoint]; attempt != nil {
			waiters = attempt.waiters
		}
		nfsMountAttempts.Unlock()
		if waiters == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second caller did not join the in-flight attempt")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancelFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller = %v", err)
	}
	stub.release <- nil
	if err := <-second; err != nil {
		t.Fatalf("joined caller: %v", err)
	}
	if mounts, unmounts, mounted, _ := stub.counts(); mounts != 1 || unmounts != 0 || !mounted {
		t.Fatalf("mounts=%d unmounts=%d mounted=%v, want one kept mount", mounts, unmounts, mounted)
	}
}

func TestNFSMountAttemptReportsFailureToEveryWaiter(t *testing.T) {
	root := t.TempDir()
	session, _ := testNFSSandbox(t, root, false)
	stub := stubBlockingNFSMounts(t)

	result := make(chan error, 1)
	go func() { result <- ensureSandboxNFSMounts(context.Background(), session) }()
	<-stub.started
	stub.release <- errors.New("permission denied")
	err := <-result
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("ensure error = %v, want the mount failure", err)
	}
	waitForNoNFSAttempt(t, filepath.Join(root, "volumes", "mount-nfs0001"))
	if _, unmounts, mounted, _ := stub.counts(); unmounts != 0 || mounted {
		t.Fatalf("unmounts=%d mounted=%v after a failed mount", unmounts, mounted)
	}
}
