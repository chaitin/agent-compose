//go:build linux

package sandboxinit

import (
	"bytes"
	"errors"
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
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	if limit.Cur < dnsSocketCacheLimit+128 {
		t.Skip("socket capacity regression requires enough file descriptors")
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
	extra := boundDNSReviewSocket(t)
	if err := s.rememberDNSSocket(extra, []byte("extra")); !errors.Is(err, unix.ENOSPC) {
		t.Fatalf("full live cache did not refuse the new redirect: %v", err)
	}
	if raw, err := s.dnsSocketSource(fds[0]); err != nil || string(raw) != "nameserver" {
		t.Fatalf("live mapping was evicted: %x %v", raw, err)
	}
	if err := unix.Close(fds[0]); err != nil {
		t.Fatal(err)
	}
	fds[0] = -1
	if err := s.rememberDNSSocket(extra, []byte("extra")); err != nil {
		t.Fatalf("closed socket was not reclaimed: %v", err)
	}
	if len(s.dnsSockets) != dnsSocketCacheLimit {
		t.Fatalf("cache size %d", len(s.dnsSockets))
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
