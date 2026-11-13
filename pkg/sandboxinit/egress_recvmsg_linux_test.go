//go:build linux

package sandboxinit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"
)

func TestInitEgressPolicyDNSRecvmsgSource(t *testing.T) {
	for _, mode := range []string{"connected", "unconnected", "scatter", "peek", "truncate", "nonblocking-empty"} {
		t.Run(mode, func(t *testing.T) {
			requireEnforceSupport(t)
			proxy := startFakeEgressProxy(t, "200 Connection Established", true)
			t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
			_, socket := startTestInitWithOptions(t, Options{SandboxID: "sandbox-1", MainShell: "sleep 120", EgressMode: EgressModeEnforce})
			waitForDNSPort(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			output, err := runEgressHelperRequest(ctx, socket, Request{
				Command: os.Args[0], Args: []string{"-test.run=^TestDNSRecvmsgHelperProcess$"},
				Env: map[string]string{"GO_WANT_DNS_RECVMSG_HELPER": mode},
			})
			if err != nil || !strings.Contains(output, "source 8.8.8.8:53") || !strings.Contains(output, "dns-recvmsg-ok") {
				t.Fatalf("recvmsg source was not restored: output=%q error=%v", output, err)
			}
		})
	}
}

func TestDNSRecvmsgHelperProcess(t *testing.T) {
	mode := os.Getenv("GO_WANT_DNS_RECVMSG_HELPER")
	if mode == "" {
		return
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	server := &unix.SockaddrInet4{Addr: [4]byte{8, 8, 8, 8}, Port: 53}
	var destination unix.Sockaddr = server
	query := buildDNSQuery("recvmsg.example", dnsmessage.TypeA)
	if mode == "connected" {
		if err := unix.Connect(fd, server); err != nil {
			t.Fatal(err)
		}
		destination = nil
	}
	if err := unix.Sendto(fd, query, 0, destination); err != nil {
		t.Fatal(err)
	}
	if err := waitFD(fd, unix.POLLIN, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4096)
	flags := 0
	if mode == "peek" {
		flags = unix.MSG_PEEK
	}
	if mode == "truncate" {
		buffer = buffer[:8]
		flags = unix.MSG_TRUNC
	}
	buffers := [][]byte{buffer}
	if mode == "scatter" {
		buffers = [][]byte{buffer[:3], buffer[3:]}
	}
	n, _, returnedFlags, from, err := unix.RecvmsgBuffers(fd, buffers, nil, flags)
	if err != nil {
		t.Fatal(err)
	}
	if source, ok := from.(*unix.SockaddrInet4); ok {
		fmt.Printf("source %s:%d\n", net.IP(source.Addr[:]), source.Port)
	}
	if mode == "truncate" {
		if n <= len(buffer) || returnedFlags&unix.MSG_TRUNC == 0 {
			t.Fatalf("truncation n=%d flags=%d", n, returnedFlags)
		}
		fmt.Println("dns-recvmsg-ok")
		return
	}
	if mode == "peek" {
		second := make([]byte, 4096)
		read, _, _, _, err := unix.Recvmsg(fd, second, nil, 0)
		if err != nil || !bytes.Equal(buffer[:n], second[:read]) {
			t.Fatalf("peek consumed or changed DNS response: %v", err)
		}
	}
	if mode == "nonblocking-empty" {
		if _, _, _, _, err := unix.Recvmsg(fd, make([]byte, 8), nil, 0); !errors.Is(err, unix.EAGAIN) {
			t.Fatalf("empty redirected nonblocking receive: %v", err)
		}
	}
	ip, err := firstAAnswer(buffer[:n])
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("synthetic %s\n", ip)
	fmt.Println("dns-recvmsg-ok")
}
