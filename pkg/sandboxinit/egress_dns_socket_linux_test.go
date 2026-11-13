//go:build linux

package sandboxinit

import (
	"bytes"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/sys/unix"
)

// Advance the clock without reconnecting or sending a destination-bearing
// syscall: connected UDP resolvers can keep the same socket across idle time.
func TestDNSSocketLifetimeIntegration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &egressSupervisor{dnsSockets: make(map[uint64]dnsSocketDestination)}
		server := boundDNSReviewSocket(t)
		peer, err := unix.Getsockname(server)
		if err != nil {
			t.Fatal(err)
		}
		fd := boundDNSReviewSocket(t)
		if err := unix.Connect(fd, peer); err != nil {
			t.Fatal(err)
		}
		original := (&egressDNS{}).rawAddrForDomain(unix.AF_INET)
		original[4] = 8
		if err := s.rememberDNSSocket(fd, original); err != nil {
			t.Fatal(err)
		}
		for round := 0; round < 3; round++ {
			if round > 0 {
				time.Sleep(6 * time.Minute)
			}
			if err := unix.Sendto(fd, []byte("query"), 0, nil); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 16)
			_, from, err := unix.Recvfrom(server, buffer, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := unix.Sendto(server, []byte("answer"), 0, from); err != nil {
				t.Fatal(err)
			}
			if _, _, err := unix.Recvfrom(fd, buffer, 0); err != nil {
				t.Fatal(err)
			}
			source, _ := s.dnsSocketSource(fd)
			if !bytes.Equal(source, original) {
				t.Fatalf("round %d lost the configured source after idle time", round)
			}
		}
	})
}

func TestDNSSocketIdentityIntegration(t *testing.T) {
	s := &egressSupervisor{dnsSockets: make(map[uint64]dnsSocketDestination)}
	fd := boundDNSReviewSocket(t)
	original := []byte("configured-nameserver")
	if err := s.rememberDNSSocket(fd, original); err != nil {
		t.Fatal(err)
	}
	alias, err := unix.Dup(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(alias) }()
	if raw, err := s.dnsSocketSource(alias); err != nil || !bytes.Equal(raw, original) {
		t.Fatalf("dup lost socket identity: %x %v", raw, err)
	}
	inode, _, err := dnsSocketIdentity(fd)
	if err != nil {
		t.Fatal(err)
	}
	entry := s.dnsSockets[inode]
	entry.cookie++ // Deterministically simulate inode reuse by a new socket.
	s.dnsSockets[inode] = entry
	if raw, err := s.dnsSocketSource(fd); err != nil || raw != nil {
		t.Fatalf("stale cookie accepted: %x %v", raw, err)
	}
	other, err := unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(other) }()
	var stat unix.Stat_t
	if err := unix.Fstat(other, &stat); err != nil {
		t.Fatal(err)
	}
	s.dnsSockets[stat.Ino] = entry
	if raw, err := s.dnsSocketSource(other); err != nil || raw != nil {
		t.Fatalf("Unix datagram accepted as DNS: %x %v", raw, err)
	}
}

func TestDNSSocketCapacityIntegration(t *testing.T) {
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &original); err != nil {
		t.Fatal(err)
	}
	want := uint64(dnsSocketCacheLimit + 128)
	if original.Cur < want {
		if original.Max < want {
			t.Skip("socket capacity regression requires enough file descriptors")
		}
		raised := original
		raised.Cur = want
		if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &raised); err != nil {
			t.Skipf("raise file descriptor limit: %v", err)
		}
		t.Cleanup(func() { _ = unix.Setrlimit(unix.RLIMIT_NOFILE, &original) })
	}
	s := &egressSupervisor{dnsSockets: make(map[uint64]dnsSocketDestination)}
	fds := make([]int, 0, dnsSocketCacheLimit)
	t.Cleanup(func() {
		for _, fd := range fds {
			if fd >= 0 {
				_ = unix.Close(fd)
			}
		}
	})
	for len(fds) < dnsSocketCacheLimit {
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		fds = append(fds, fd)
		if err := unix.Bind(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
			t.Fatal(err)
		}
		if err := s.rememberDNSSocket(fd, []byte("nameserver")); err != nil {
			t.Fatal(err)
		}
	}
	// Give every mapping a distinct age, then refresh one of them. The fresh
	// mapping must survive an eviction while the oldest live mapping is removed.
	for index, fd := range fds {
		inode, _, err := dnsSocketIdentity(fd)
		if err != nil {
			t.Fatal(err)
		}
		entry := s.dnsSockets[inode]
		entry.lastUsed = uint64(index + 1)
		s.dnsSockets[inode] = entry
	}
	if raw, err := s.dnsSocketSource(fds[0]); err != nil || string(raw) != "nameserver" {
		t.Fatalf("refreshed mapping: %x %v", raw, err)
	}
	extra := boundDNSReviewSocket(t)
	if err := s.rememberDNSSocket(extra, []byte("extra")); err != nil {
		t.Fatalf("full live cache refused a new DNS redirect: %v", err)
	}
	if len(s.dnsSockets) != dnsSocketCacheLimit {
		t.Fatalf("cache size %d, want %d", len(s.dnsSockets), dnsSocketCacheLimit)
	}
	if raw, err := s.dnsSocketSource(extra); err != nil || string(raw) != "extra" {
		t.Fatalf("new redirect was not recorded: %x %v", raw, err)
	}
	if raw, err := s.dnsSocketSource(fds[0]); err != nil || string(raw) != "nameserver" {
		t.Fatalf("recently used mapping was evicted: %x %v", raw, err)
	}
	evicted, _, err := dnsSocketIdentity(fds[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := s.dnsSockets[evicted]; exists {
		t.Fatal("least recently used mapping was not evicted first")
	}

	// A closed socket must be reclaimed before another live mapping is evicted.
	closed, _, err := dnsSocketIdentity(fds[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(fds[2]); err != nil {
		t.Fatal(err)
	}
	fds[2] = -1
	s.dnsMu.Lock()
	s.dnsLastPrune = time.Time{}
	s.dnsMu.Unlock()
	second := boundDNSReviewSocket(t)
	if err := s.rememberDNSSocket(second, []byte("second")); err != nil {
		t.Fatalf("closed socket slot was not reclaimed: %v", err)
	}
	if _, exists := s.dnsSockets[closed]; exists {
		t.Fatal("closed socket mapping was retained")
	}
	if len(s.dnsSockets) != dnsSocketCacheLimit {
		t.Fatalf("cache size %d after reclaim, want %d", len(s.dnsSockets), dnsSocketCacheLimit)
	}
	if raw, err := s.dnsSocketSource(fds[0]); err != nil || string(raw) != "nameserver" {
		t.Fatalf("live mapping was evicted while a closed slot was available: %x %v", raw, err)
	}
	if raw, err := s.dnsSocketSource(second); err != nil || string(raw) != "second" {
		t.Fatalf("reclaimed redirect was not recorded: %x %v", raw, err)
	}
}

func TestDNSSocketTransferredLifecycleIntegration(t *testing.T) {
	if err := egressInterceptionSupported(); err != nil {
		t.Skip(err)
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}()
	if err := unix.Bind(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	s := &egressSupervisor{dnsSockets: make(map[uint64]dnsSocketDestination)}
	if err := s.rememberDNSSocket(fd, []byte("nameserver")); err != nil {
		t.Fatal(err)
	}
	helper := startDNSRecvmsgReviewHelper(t, fd, "normal")
	_ = receiveDNSRecvmsgReview(t, helper.listener)
	if err := unix.Close(fd); err != nil {
		t.Fatal(err)
	}
	fd = -1 // Only the child now holds the socket; init has no retained fd.
	if err := s.pruneDNSSockets(); err != nil || len(s.dnsSockets) != 1 {
		t.Fatalf("child's live socket was pruned: %v", err)
	}
	if err := helper.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.command.Wait()
	if err := s.pruneDNSSockets(); err != nil || len(s.dnsSockets) != 0 {
		t.Fatalf("closed socket was retained: %v", err)
	}
}

func boundDNSReviewSocket(t *testing.T) int {
	t.Helper()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if err := unix.Bind(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	return fd
}
