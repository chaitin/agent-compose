//go:build linux

package sandboxinit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestInitDNSRecvmsgLifecycle(t *testing.T) {
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "200 Connection Established", true)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	_, socket := startTestInitWithOptions(t, Options{SandboxID: "sandbox-1", MainShell: "sleep 120", EgressMode: EgressModeEnforce})
	waitForDNSPort(t)
	for _, mode := range []string{"concurrent-send", "long-timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			output, err := runEgressHelperRequest(ctx, socket, Request{Command: os.Args[0], Args: []string{"-test.run=^TestDNSRecvmsgLifecycleHelper$"}, Env: map[string]string{"DNS_RECVMSG_LIFECYCLE": mode}})
			if err != nil || !strings.Contains(output, "concurrent-ok") {
				t.Fatalf("concurrent send: %v %s", err, output)
			}
		})
	}
	t.Run("cancel-releases-socket", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client := NewClient(socket)
		session, err := client.Exec(ctx, Request{Command: os.Args[0], Args: []string{"-test.run=^TestDNSRecvmsgLifecycleHelper$"}, Env: map[string]string{"DNS_RECVMSG_LIFECYCLE": "cancel"}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = session.Close() }()
		var inode uint64
		var tid int
		for inode == 0 {
			frame, err := session.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if frame.Type == FrameStdout {
				_, _ = fmt.Sscanf(string(frame.Data), "waiting %d %d", &inode, &tid)
			}
			if frame.Type == FrameExit {
				t.Fatal("helper exited before blocking")
			}
		}
		waitForRecvmsgSyscall(t, tid)
		// Wait until the supervisor has duplicated and retained this socket.
		waitForDNSCondition(t, func() bool { return socketCopies(inode) > 0 })
		if err := session.Signal(ctx, "KILL"); err != nil {
			t.Fatal(err)
		}
		// A canceled notification must release its duplicate without waiting for a
		// DNS response or the old supervisor's fixed ten-second receive budget.
		waitForDNSCondition(t, func() bool { return socketCopies(inode) == 0 })
	})
}

func socketCopies(inode uint64) int {
	files, _ := filepath.Glob("/proc/self/fd/*")
	count := 0
	for _, file := range files {
		link, _ := os.Readlink(file)
		if link == fmt.Sprintf("socket:[%d]", inode) {
			count++
		}
	}
	return count
}

func waitForDNSCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("DNS receive condition did not become true")
		}
		runtime.Gosched()
	}
}

func waitForRecvmsgSyscall(t *testing.T, tid int) {
	t.Helper()
	waitForDNSCondition(t, func() bool {
		data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/syscall", tid))
		fields := strings.Fields(string(data))
		return len(fields) > 0 && fields[0] == strconv.Itoa(unix.SYS_RECVMSG)
	})
}

func TestDNSRecvmsgLifecycleHelper(t *testing.T) {
	mode := os.Getenv("DNS_RECVMSG_LIFECYCLE")
	if mode == "" {
		return
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Connect(fd, &unix.SockaddrInet4{Addr: [4]byte{8, 8, 8, 8}, Port: 53}); err != nil {
		t.Fatal(err)
	}
	if mode == "long-timeout" {
		if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 1 << 34}); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "cancel" {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("waiting %d %d\n", stat.Ino, unix.Gettid())
		_, _, _, _, _ = unix.Recvmsg(fd, make([]byte, 4096), nil, 0)
		t.Fatal("indefinite receive unexpectedly returned")
	}
	tid := make(chan int)
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		tid <- unix.Gettid()
		n, _, _, source, err := unix.Recvmsg(fd, make([]byte, 4096), nil, 0)
		if err == nil {
			address, ok := source.(*unix.SockaddrInet4)
			if n == 0 || !ok || address.Addr != [4]byte{8, 8, 8, 8} || address.Port != 53 {
				err = fmt.Errorf("bad DNS receive: %d %v", n, source)
			}
		}
		done <- err
	}()
	waitForRecvmsgSyscall(t, <-tid)
	if _, err := unix.SendmsgN(fd, buildDNSQuery("concurrent.example", 1), nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send was blocked by receive")
	}
	fmt.Println("concurrent-ok")
}
