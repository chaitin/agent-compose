//go:build linux

package sandboxinit

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDNSRecvmsgActualSourceIntegration(t *testing.T) {
	if err := egressInterceptionSupported(); err != nil {
		t.Skip(err)
	}
	for _, mode := range []string{"source", "source-peek"} {
		t.Run(mode, func(t *testing.T) {
			fd, sender := boundDNSReviewSocket(t), boundDNSReviewSocket(t)
			to, err := unix.Getsockname(fd)
			if err != nil {
				t.Fatal(err)
			}
			from, err := unix.Getsockname(sender)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("DNS_RECVMSG_REVIEW_SOURCE", fmt.Sprintf("127.0.0.1:%d", from.(*unix.SockaddrInet4).Port))
			if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMP, 1); err != nil {
				t.Fatal(err)
			}
			helper := startDNSRecvmsgReviewHelper(t, fd, mode)
			request := receiveDNSRecvmsgReview(t, helper.listener)
			duplicate, err := unix.Dup(fd)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Close(duplicate) }()
			original, err := sockaddrFromTCPAddr(&net.TCPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 53})
			if err != nil {
				t.Fatal(err)
			}
			r := &dnsRecvmsg{request: request, listenerFD: helper.listener, fd: duplicate, original: original}
			if errno := r.readLayout(); errno != 0 {
				t.Fatal(errno)
			}
			if err := unix.Sendto(sender, bytes.Repeat([]byte("dns-response"), 8), 0, to); err != nil {
				t.Fatal(err)
			}
			done, packet := r.try(nil)
			// try owns the duplicate after consuming; a peek keeps caller ownership.
			if r.fd < 0 {
				duplicate = -1
			}
			if packet != nil {
				_ = unix.Close(packet.fd)
			}
			if !done || packet != nil {
				t.Fatal("receive did not commit")
			}
			output, err := helper.outputAndWait()
			if err != nil || !strings.Contains(output, "replay-ok") {
				t.Fatalf("actual source receive: %v %s", err, output)
			}
		})
	}
}

func TestDNSReplySource(t *testing.T) {
	original := []byte("original-nameserver")
	for _, tc := range []struct {
		source unix.Sockaddr
		stub   bool
	}{
		{&unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}, Port: 53}, true},
		{&unix.SockaddrInet6{Addr: [16]byte{15: 1}, Port: 53}, true},
		{&unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}, Port: 12345}, false},
		{&unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 2}, Port: 53}, false},
		{&unix.SockaddrInet6{Addr: [16]byte{0: 0xfe, 1: 0x80, 15: 1}, ZoneId: 7, Port: 53}, false},
	} {
		raw := rawDNSReplySource(tc.source)
		got := restoreDNSReplySource(original, raw)
		want := raw
		if tc.stub {
			want = original
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("source %v changed to %x", tc.source, got)
		}
	}
}
