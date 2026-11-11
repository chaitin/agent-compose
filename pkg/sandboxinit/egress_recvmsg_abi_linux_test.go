//go:build linux

package sandboxinit

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestInitDNSRecvmsgABI(t *testing.T) {
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "200 Connection Established", true)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	modes := []string{"short-name", "null-name", "null-name-negative-length", "long-timeout", "zero-iov", "many-iov", "null-control", "control-trunc", "control", "invalid-header", "invalid-iov", "invalid-payload", "oversized-iov", "negative-name", "errqueue", "dontwait", "dontwait-empty", "oob-empty", "large-iov", "invalid-length", "ipv6", "timeout", "timeout-empty-control"}
	kernelResults := make(map[string]string)
	dns, err := startEgressDNS("abi-kernel", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range modes {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDNSRecvmsgABIHelper$")
		cmd.Env = append(os.Environ(), "DNS_RECVMSG_ABI="+mode)
		output, err := cmd.CombinedOutput()
		if err != nil {
			dns.close()
			t.Fatalf("kernel %s: %v %s", mode, err, output)
		}
		kernelResults[mode] = string(output)
	}
	dns.close()
	_, socket := startTestInitWithOptions(t, Options{SandboxID: "sandbox-1", MainShell: "sleep 120", EgressMode: EgressModeEnforce})
	waitForDNSPort(t)
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			kernel := kernelResults[mode]
			actual, err := runEgressHelperRequest(ctx, socket, Request{Command: os.Args[0], Args: []string{"-test.run=^TestDNSRecvmsgABIHelper$"}, Env: map[string]string{"DNS_RECVMSG_ABI": mode, "DNS_RECVMSG_REDIRECT": "1"}})
			if err != nil {
				t.Fatalf("mediated helper: %v %s", err, actual)
			}
			decode := func(output string) map[string]any {
				t.Helper()
				line := strings.Split(output, "\n")[0]
				var result map[string]any
				if err := json.Unmarshal([]byte(line), &result); err != nil {
					t.Fatalf("decode %s: %v", output, err)
				}
				return result
			}
			expected, observed := decode(string(kernel)), decode(actual)
			t.Logf("kernel=%v mediated=%v", expected, observed)
			if !reflect.DeepEqual(expected, observed) {
				t.Fatal("recvmsg differs from the kernel")
			}
		})
	}
}

func TestDNSRecvmsgABIHelper(t *testing.T) {
	mode := os.Getenv("DNS_RECVMSG_ABI")
	if mode == "" {
		return
	}
	domain := unix.AF_INET
	var server unix.Sockaddr = &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}, Port: 53}
	if mode == "ipv6" {
		domain = unix.AF_INET6
		server = &unix.SockaddrInet6{Addr: [16]byte{15: 1}, Port: 53}
	}
	if os.Getenv("DNS_RECVMSG_REDIRECT") == "1" {
		server = &unix.SockaddrInet4{Addr: [4]byte{8, 8, 8, 8}, Port: 53}
		if domain == unix.AF_INET6 {
			server = &unix.SockaddrInet6{Addr: [16]byte{0: 0x20, 1: 1, 2: 0x48, 3: 0x60, 4: 0x48, 5: 0x60, 14: 0x88, 15: 0x88}, Port: 53}
		}
	}
	fd, err := unix.Socket(domain, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Connect(fd, server); err != nil {
		t.Fatal(err)
	}
	timeout := unix.Timeval{Usec: 20000}
	if mode == "long-timeout" {
		timeout = unix.Timeval{Sec: 1 << 34}
	}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &timeout); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(mode, "control") || mode == "null-control" {
		if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMP, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Sendto(fd, buildDNSQuery("abi.example", 1), 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := waitFD(fd, unix.POLLIN, time.Second); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4096)
	name := make([]byte, 128)
	control := make([]byte, 128)
	invalid, err := unix.Mmap(-1, 0, 4096, unix.PROT_NONE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Munmap(invalid) }()
	vectors := []unix.Iovec{{Base: &buffer[0], Len: uint64(len(buffer))}}
	if mode == "many-iov" {
		vectors = make([]unix.Iovec, 1024)
		for i := range vectors {
			vectors[i] = unix.Iovec{Base: &buffer[i], Len: 1}
		}
	}
	message := unix.Msghdr{Name: &name[0], Namelen: uint32(len(name)), Iov: &vectors[0], Iovlen: uint64(len(vectors))}
	switch mode {
	case "short-name":
		message.Namelen = 2
	case "null-name":
		message.Name = nil
	case "null-name-negative-length":
		message.Name = nil
		message.Namelen = 0xffffffff
	case "zero-iov":
		message.Iov = nil
		message.Iovlen = 0
	case "large-iov":
		vectors = make([]unix.Iovec, 3)
		for i := range vectors {
			vectors[i] = unix.Iovec{Base: &buffer[0], Len: 1 << 62}
		}
		message.Iov = &vectors[0]
		message.Iovlen = 3
	case "invalid-length":
		vectors[0].Len = 1 << 63
	case "invalid-iov":
		message.Iov = (*unix.Iovec)(unsafe.Pointer(&invalid[0]))
	case "invalid-payload":
		vectors[0].Base = &invalid[0]
	case "oversized-iov":
		message.Iovlen = 1025
	case "negative-name":
		message.Namelen = 0xffffffff
	case "null-control":
		message.Controllen = 128
	case "control-trunc":
		message.Control = &control[0]
		message.Controllen = 1
	case "control":
		message.Control = &control[0]
		message.Controllen = 128
	}
	header := unsafe.Pointer(&message)
	if mode == "invalid-header" {
		header = unsafe.Pointer(&invalid[0])
	}
	flags := 0
	if strings.HasPrefix(mode, "dontwait") {
		flags = unix.MSG_DONTWAIT
	}
	if mode == "errqueue" {
		flags = unix.MSG_ERRQUEUE
	}
	if mode == "oob-empty" {
		flags = unix.MSG_OOB
	}
	if mode == "dontwait-empty" || mode == "oob-empty" {
		if _, _, _, _, err := unix.Recvmsg(fd, make([]byte, 4096), nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	receive := func(flags int) (uintptr, unix.Errno) {
		for {
			n, _, errno := unix.Syscall(unix.SYS_RECVMSG, uintptr(fd), uintptr(header), uintptr(flags))
			if errno != unix.EINTR {
				return n, errno
			}
		}
	}
	n, errno := receive(flags)
	if strings.HasPrefix(mode, "timeout") {
		if errno != 0 {
			t.Fatal(errno)
		}
		if mode == "timeout-empty-control" {
			message.Control = nil
			message.Controllen = 128
		}
		started := time.Now()
		n, errno = receive(0)
		if elapsed := time.Since(started); elapsed > time.Second || elapsed < 10*time.Millisecond {
			t.Fatalf("socket timeout: %s", elapsed)
		}
	}
	result := map[string]any{"errno": int(errno)}
	if errno == 0 {
		result["n"] = uint64(n)
		result["namelen"] = message.Namelen
		result["controllen"] = message.Controllen
		result["flags"] = message.Flags
		if message.Name != nil && message.Namelen >= 16 {
			switch destination := server.(type) {
			case *unix.SockaddrInet4:
				result["source_matches"] = binary.BigEndian.Uint16(name[2:4]) == uint16(destination.Port) && bytes.Equal(name[4:8], destination.Addr[:])
			case *unix.SockaddrInet6:
				result["source_matches"] = binary.BigEndian.Uint16(name[2:4]) == uint16(destination.Port) && bytes.Equal(name[8:24], destination.Addr[:])
			}
		}
	}
	encoded, _ := json.Marshal(result)
	fmt.Println(string(encoded))
}
