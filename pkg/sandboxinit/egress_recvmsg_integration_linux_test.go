//go:build linux

package sandboxinit

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The child owns a real seccomp filter and hands its listener to this process.
// Inheriting the datagram fd avoids pidfd_getfd/CAP_SYS_PTRACE and binding port
// 53, so the queue-to-memory-to-notification boundary runs in unprivileged CI.
func TestDNSRecvmsgQueueRoundTripIntegration(t *testing.T) {
	if err := egressInterceptionSupported(); err != nil {
		t.Skip(err)
	}
	for _, mode := range []string{"normal", "truncate", "peek", "control-trunc", "control-partial", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
			if err := unix.SetsockoptInt(pair[0], unix.SOL_SOCKET, unix.SO_TIMESTAMP, 1); err != nil {
				t.Fatal(err)
			}
			if mode == "timeout" {
				if err := unix.SetsockoptTimeval(pair[0], unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Usec: 20000}); err != nil {
					t.Fatal(err)
				}
			}
			helper := startDNSRecvmsgReviewHelper(t, pair[0], mode)
			request := receiveDNSRecvmsgReview(t, helper.listener)
			fd, err := unix.Dup(pair[0])
			if err != nil {
				t.Fatal(err)
			}
			listener, err := unix.Dup(helper.listener)
			if err != nil {
				_ = unix.Close(fd)
				t.Fatal(err)
			}
			r := &dnsRecvmsg{request: request, listenerFD: listener, fd: fd, inode: 1}
			if errno := r.readLayout(); errno != 0 {
				r.close()
				t.Fatal(errno)
			}
			q, err := newDNSRecvmsgQueue()
			if err != nil {
				r.close()
				t.Fatal(err)
			}
			defer q.close()
			q.submit(r)
			if mode != "timeout" {
				if err := unix.Sendto(pair[1], bytes.Repeat([]byte("dns-response"), 8), 0, nil); err != nil {
					t.Fatal(err)
				}
			}
			output, err := helper.outputAndWait()
			if err != nil || !strings.Contains(output, "replay-ok") {
				t.Fatalf("queued receive: %v %s", err, output)
			}
			waitForDNSCondition(t, func() bool { return len(q.slots) == 0 })
			if mode == "peek" {
				data := make([]byte, 128)
				n, _, err := unix.Recvfrom(pair[0], data, unix.MSG_DONTWAIT)
				if err != nil || n != 96 {
					t.Fatalf("peek consumed queued packet: %d %v", n, err)
				}
			}
		})
	}
}

func TestDNSRecvmsgFullRecoveryCacheIntegration(t *testing.T) {
	if err := egressInterceptionSupported(); err != nil {
		t.Skip(err)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
	if err := unix.SetsockoptInt(pair[0], unix.SOL_SOCKET, unix.SO_TIMESTAMP, 1); err != nil {
		t.Fatal(err)
	}
	saved := make(dnsRecvmsgPackets)
	for inode := uint64(1); inode <= dnsRecvmsgPendingLimit; inode++ {
		saved[inode] = &dnsRecvmsgPacket{fd: -1, expires: time.Now().Add(dnsRecvmsgPacketLifetime)}
	}
	helper := startDNSRecvmsgReviewHelper(t, pair[0], "normal")
	request := receiveDNSRecvmsgReview(t, helper.listener)
	fd, err := unix.Dup(pair[0])
	if err != nil {
		t.Fatal(err)
	}
	r := &dnsRecvmsg{request: request, listenerFD: helper.listener, fd: fd, inode: dnsRecvmsgPendingLimit + 1}
	defer func() {
		if r.fd >= 0 {
			_ = unix.Close(r.fd)
		}
	}()
	if errno := r.readLayout(); errno != 0 {
		t.Fatal(errno)
	}
	if err := unix.Sendto(pair[1], bytes.Repeat([]byte("dns-response"), 8), 0, nil); err != nil {
		t.Fatal(err)
	}
	if !saved.receive(r) {
		t.Fatal("live receive remained blocked by full recovery cache")
	}
	output, err := helper.outputAndWait()
	if err != nil || !strings.Contains(output, "replay-ok") {
		t.Fatalf("full-cache receive: %v %s", err, output)
	}
	if len(saved) != dnsRecvmsgPendingLimit {
		t.Fatal("successful receive unnecessarily evicted a canceled reply")
	}
}
