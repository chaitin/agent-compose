//go:build linux

package sandboxinit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDNSRecvmsgTargetFilter(t *testing.T) {
	s := &egressSupervisor{dnsSockets: make(map[uint64]dnsSocketDestination)}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if s.isDNSTargetSocket(os.Getpid(), fd) {
		t.Fatal("ordinary socket classified as DNS")
	}
	if err := s.rememberDNSSocket(fd, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if !s.isDNSTargetSocket(os.Getpid(), fd) {
		t.Fatal("redirected socket not recognized")
	}
	alias, err := unix.Dup(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(alias) }()
	if !s.isDNSTargetSocket(os.Getpid(), alias) {
		t.Fatal("duplicated socket lost its DNS identity")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if !s.isDNSTargetSocket(unix.Gettid(), alias) {
		t.Fatal("thread fd table not recognized")
	}
	other, err := unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(other) }()
	if err := unix.Dup3(other, alias, 0); err != nil {
		t.Fatal(err)
	}
	if s.isDNSTargetSocket(os.Getpid(), alias) {
		t.Fatal("fd reuse inherited stale DNS identity")
	}
}

func TestDNSRecvmsgQueueIntakeBound(t *testing.T) {
	wake, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(wake) }()
	// Leave the worker stopped to exercise admission while it cannot drain.
	q := &dnsRecvmsgQueue{wake: wake, add: make(chan *dnsRecvmsg, dnsRecvmsgPendingLimit), slots: make(chan struct{}, dnsRecvmsgPendingLimit)}
	for i := 0; i < dnsRecvmsgPendingLimit+1; i++ {
		q.submit(&dnsRecvmsg{fd: -1, listenerFD: -1})
	}
	if len(q.add) != dnsRecvmsgPendingLimit || len(q.slots) != dnsRecvmsgPendingLimit {
		t.Fatal("intake was not bounded")
	}
	for len(q.add) > 0 {
		q.release(<-q.add)
	}
}

// Cancel precisely after a datagram has been removed but before NOTIF_SEND.
// The next recvmsg uses a different listener and buffer/flags, so replay must
// retain the complete packet rather than the canceled caller's truncated copy.
func TestDNSRecvmsgCanceledPacketReplay(t *testing.T) {
	if err := egressInterceptionSupported(); err != nil {
		t.Skip(err)
	}
	for _, mode := range []string{"normal", "truncate", "peek", "control-trunc", "control-partial"} {
		t.Run(mode, func(t *testing.T) {
			pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
			payload := bytes.Repeat([]byte("dns-response"), 8)
			if err := unix.SetsockoptInt(pair[0], unix.SOL_SOCKET, unix.SO_TIMESTAMP, 1); err != nil {
				t.Fatal(err)
			}
			if err := unix.Sendto(pair[1], payload, 0, nil); err != nil {
				t.Fatal(err)
			}
			// Each helper has its own real seccomp listener while inheriting this socket.
			original := startDNSRecvmsgReviewHelper(t, pair[0], "normal")
			request := receiveDNSRecvmsgReview(t, original.listener)
			fd, err := unix.Dup(pair[0])
			if err != nil {
				t.Fatal(err)
			}
			packet, err := readDNSRecvmsgPacket(fd, 0)
			if err != nil {
				_ = unix.Close(fd)
				t.Fatal(err)
			}
			packet.fd = fd
			if err := original.command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = original.command.Wait()
			waitForDNSCondition(t, func() bool { return !notificationIDValid(original.listener, request.ID) })
			r := &dnsRecvmsg{request: request, listenerFD: original.listener}
			if retained := r.complete(packet, nil, nil, 0, 0); retained != packet {
				t.Fatal("canceled reply dropped the packet")
			}
			defer func() {
				if packet != nil {
					_ = unix.Close(packet.fd)
				}
			}()
			second := []byte("next-response")
			if err := unix.Sendto(pair[1], second, 0, nil); err != nil {
				t.Fatal(err)
			}
			next := startDNSRecvmsgReviewHelper(t, pair[0], mode)
			nextRequest := receiveDNSRecvmsgReview(t, next.listener)
			r = &dnsRecvmsg{request: nextRequest, listenerFD: next.listener, fd: pair[0]}
			if errno := r.readLayout(); errno != 0 {
				t.Fatal(errno)
			}
			done, retained := r.try(packet)
			if !done {
				t.Fatal("replay stayed blocked on the empty kernel queue")
			}
			output, err := next.outputAndWait()
			if err != nil || !strings.Contains(output, "replay-ok") {
				t.Fatalf("replayed receive: %v %s", err, output)
			}
			tail := make([]byte, 128)
			n, _, _, _, err := unix.Recvmsg(pair[0], tail, nil, unix.MSG_DONTWAIT)
			if err != nil || !bytes.Equal(tail[:max(n, 0)], second) {
				t.Fatalf("replay consumed the next datagram: %v %q", err, tail[:max(n, 0)])
			}
			if mode == "peek" {
				if retained != packet {
					t.Fatal("MSG_PEEK consumed the retained packet")
				}
			} else {
				if retained != nil {
					t.Fatal("committed receive did not release the retained packet")
				}
				packet = nil
			}
		})
	}
}

type dnsRecvmsgReviewHelper struct {
	listener int
	command  *exec.Cmd
	output   bytes.Buffer
}

func (h *dnsRecvmsgReviewHelper) outputAndWait() (string, error) {
	err := h.command.Wait()
	return h.output.String(), err
}

func startDNSRecvmsgReviewHelper(t *testing.T, fd int, mode string) *dnsRecvmsgReviewHelper {
	t.Helper()
	handoff, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, child := os.NewFile(uintptr(handoff[0]), "parent"), os.NewFile(uintptr(handoff[1]), "child")
	defer func() { _ = parent.Close(); _ = child.Close() }()
	duplicate, err := unix.Dup(fd)
	if err != nil {
		t.Fatal(err)
	}
	socket := os.NewFile(uintptr(duplicate), "datagram")
	defer func() { _ = socket.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	h := &dnsRecvmsgReviewHelper{}
	h.command = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDNSRecvmsgReviewHelperProcess$")
	h.command.ExtraFiles = []*os.File{child, socket}
	h.command.Env = append(os.Environ(), "DNS_RECVMSG_REVIEW_HELPER="+mode)
	h.command.Stdout, h.command.Stderr = &h.output, &h.output
	if err := h.command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = h.command.Wait(); _ = unix.Close(h.listener) })
	_ = child.Close()
	_ = socket.Close()
	if err := waitFD(int(parent.Fd()), unix.POLLIN, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	h.listener, err = receiveListener(parent)
	if err != nil {
		_ = h.command.Wait()
		t.Fatalf("listener handoff: %v output=%s", err, h.output.String())
	}
	return h
}

func receiveDNSRecvmsgReview(t *testing.T, listener int) seccompNotif {
	t.Helper()
	if err := waitFD(listener, unix.POLLIN, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	var request seccompNotif
	if err := ioctlNotifRecv(listener, &request); err != nil {
		t.Fatal(err)
	}
	if request.Data.NR != int32(unix.SYS_RECVMSG) {
		t.Fatalf("unexpected syscall %d", request.Data.NR)
	}
	return request
}

func TestDNSRecvmsgReviewHelperProcess(t *testing.T) {
	mode := os.Getenv("DNS_RECVMSG_REVIEW_HELPER")
	if mode == "" {
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	listener, err := installEgressFilter()
	if err != nil {
		t.Fatal(err)
	}
	handoff := os.NewFile(3, "handoff")
	if err := sendListener(handoff, listener); err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(listener)
	_ = handoff.Close()
	buffer := make([]byte, 1024)
	flags := 0
	controlSize := 128
	if mode == "truncate" {
		buffer = buffer[:4]
		flags = unix.MSG_TRUNC
	}
	if mode == "peek" || mode == "source-peek" {
		flags = unix.MSG_PEEK
	}
	if mode == "control-trunc" {
		controlSize = 1
	}
	if mode == "control-partial" {
		controlSize = 20
	}
	n, oobn, returned, from, err := unix.Recvmsg(4, buffer, make([]byte, controlSize), flags)
	if mode == "timeout" {
		if !errors.Is(err, unix.EAGAIN) {
			t.Fatalf("timeout receive: %v", err)
		}
		fmt.Println("replay-ok")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if expected := os.Getenv("DNS_RECVMSG_REVIEW_SOURCE"); expected != "" {
		actual, ok := from.(*unix.SockaddrInet4)
		if !ok || fmt.Sprintf("%s:%d", net.IP(actual.Addr[:]), actual.Port) != expected {
			t.Fatalf("unexpected datagram source: %v, want %s", from, expected)
		}
	}
	if n != 96 {
		t.Fatalf("full response length %d", n)
	}
	if !bytes.HasPrefix(buffer, []byte("dns-")) {
		t.Fatalf("wrong response %q", buffer)
	}
	if mode == "truncate" && returned&unix.MSG_TRUNC == 0 {
		t.Fatal("truncation flag lost")
	}
	if mode == "control-trunc" {
		if oobn != 0 || returned&unix.MSG_CTRUNC == 0 {
			t.Fatal("control truncation changed")
		}
	} else if mode == "control-partial" {
		if oobn != 20 || returned&unix.MSG_CTRUNC == 0 {
			t.Fatal("partial control truncation changed")
		}
	} else if oobn == 0 {
		t.Fatal("ancillary data lost")
	}
	fmt.Println("replay-ok")
}

func TestDNSRecvmsgClosedQueue(t *testing.T) {
	q, err := newDNSRecvmsgQueue()
	if err != nil {
		t.Fatal(err)
	}
	q.close()
	q.close()
	q.submit(&dnsRecvmsg{fd: -1, listenerFD: -1})
	if len(q.add) != 0 {
		t.Fatal("closed queue accepted work")
	}
}

func BenchmarkDNSRecvmsgNonDNS(b *testing.B) {
	s := &egressSupervisor{dnsSockets: make(map[uint64]dnsSocketDestination)}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	b.Run("inode-filter", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if s.isDNSTargetSocket(os.Getpid(), fd) {
				b.Fatal("not DNS")
			}
		}
	})
	b.Run("duplicate-first", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			duplicate, err := duplicateTargetFD(os.Getpid(), fd)
			if err != nil {
				b.Skip(err)
			}
			_, _ = s.dnsSocketSource(duplicate)
			_ = unix.Close(duplicate)
		}
	})
}
