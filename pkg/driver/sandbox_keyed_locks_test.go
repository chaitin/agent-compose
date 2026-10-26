package driver

import (
	"sync"
	"testing"
	"time"
)

func TestSandboxKeyedLocksSerializePerKey(t *testing.T) {
	var locks sandboxKeyedLocks

	// Holding key "a" must not block an unrelated key "b".
	unlockA := locks.lock("a")
	releaseB := make(chan struct{})
	go func() {
		unlockB := locks.lock("b")
		close(releaseB)
		unlockB()
	}()
	select {
	case <-releaseB:
	case <-time.After(time.Second):
		t.Fatal("a different key must not be blocked by a held key")
	}

	// A second acquire of "a" must block until the first is released.
	acquired := make(chan struct{})
	go func() {
		unlockA2 := locks.lock("a")
		close(acquired)
		unlockA2()
	}()
	select {
	case <-acquired:
		t.Fatal("same key must serialize: second lock acquired while first held")
	case <-time.After(50 * time.Millisecond):
	}
	unlockA()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second same-key lock was never released")
	}
}

func TestSandboxKeyedLocksDropsEmptyEntries(t *testing.T) {
	var locks sandboxKeyedLocks
	unlock := locks.lock("s")
	unlock()

	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.locks) != 0 {
		t.Fatalf("expected entry removed after last unlock, got %d entries", len(locks.locks))
	}
}

func TestSandboxKeyedLocksReentrantRefCount(t *testing.T) {
	var locks sandboxKeyedLocks
	var mu sync.Mutex
	order := []string{}
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, s)
	}

	done := make(chan struct{})
	unlock1 := locks.lock("s")
	go func() {
		unlock2 := locks.lock("s")
		record("second")
		unlock2()
		close(done)
	}()
	time.Sleep(50 * time.Millisecond) // let the goroutine queue behind the held lock
	record("first-release-pending")
	unlock1()
	<-done

	if len(order) == 0 || order[0] == "second" {
		t.Fatalf("same-key access was not serialized: %v", order)
	}
}
