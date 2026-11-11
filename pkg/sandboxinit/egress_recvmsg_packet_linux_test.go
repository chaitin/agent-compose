//go:build linux

package sandboxinit

import (
	"bytes"
	"errors"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestDNSRecvmsgPacketAllocation(t *testing.T) {
	for _, size := range []int{0, 96, 4000, 8000} {
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, timestamp := range []bool{false, true} {
			if timestamp {
				if err := unix.SetsockoptInt(pair[0], unix.SOL_SOCKET, unix.SO_TIMESTAMP, 1); err != nil {
					t.Fatal(err)
				}
			}
			payload := bytes.Repeat([]byte{0xab}, size)
			if err := unix.Sendto(pair[1], payload, 0, nil); err != nil {
				t.Fatal(err)
			}
			packet, err := readDNSRecvmsgPacket(pair[0], 0)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(payload, packet.payload) || cap(packet.payload) != size {
				t.Fatalf("payload size/capacity: %d/%d want %d", len(packet.payload), cap(packet.payload), size)
			}
			if !timestamp && packet.control != nil {
				t.Fatal("socket without ancillary data retained a control buffer")
			}
			if timestamp && len(packet.control) != unix.CmsgSpace(int(unsafe.Sizeof(unix.Timeval{}))) {
				t.Fatalf("timestamp control %d", len(packet.control))
			}
		}
		_ = unix.Close(pair[0])
		_ = unix.Close(pair[1])
	}
}

func BenchmarkDNSRecvmsgPacket(b *testing.B) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
	payload := make([]byte, 96)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := unix.Sendto(pair[1], payload, 0, nil); err != nil {
			b.Fatal(err)
		}
		if _, err := readDNSRecvmsgPacket(pair[0], 0); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDNSRecvmsgRecoveryCacheBound(t *testing.T) {
	saved := make(dnsRecvmsgPackets)
	fd, err := unix.Dup(0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if p := saved[1]; p != nil {
			_ = unix.Close(p.fd)
		}
	}()
	oldest := time.Now()
	saved[1] = &dnsRecvmsgPacket{fd: fd, expires: oldest}
	for inode := uint64(2); inode <= dnsRecvmsgPendingLimit; inode++ {
		saved[inode] = &dnsRecvmsgPacket{fd: -1, expires: oldest.Add(time.Second)}
	}
	fresh := &dnsRecvmsgPacket{fd: -1, expires: oldest.Add(time.Minute)}
	saved.retain(dnsRecvmsgPendingLimit+1, fresh)
	if len(saved) != dnsRecvmsgPendingLimit || saved[1] != nil || saved[dnsRecvmsgPendingLimit+1] != fresh {
		t.Fatal("recovery cache was not bounded or evicted the wrong reply")
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatal("evicted packet kept its fd")
	}
}
