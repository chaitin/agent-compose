//go:build linux

package sandboxinit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestInitDNSRecvmsgManyWaiters(t *testing.T) {
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "200 Connection Established", true)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	_, socket := startTestInitWithOptions(t, Options{SandboxID: "sandbox-1", MainShell: "sleep 120", EgressMode: EgressModeEnforce})
	waitForDNSPort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := NewClient(socket).Exec(ctx, Request{Command: os.Args[0], Args: []string{"-test.run=^TestDNSRecvmsgLoadHelper$", "-test.timeout=10s"}, Env: map[string]string{"DNS_RECVMSG_LOAD": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	var output strings.Builder
	for !strings.Contains(output.String(), "load-waiting") {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == FrameExit {
			t.Fatalf("load helper exited: %s", output.String())
		}
		output.Write(frame.Data)
	}
	var before, after unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &before); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	// This is a bounded idle CPU measurement, not synchronization by sleep.
	timer := time.NewTimer(time.Second)
	select {
	case <-timer.C:
	case <-ctx.Done():
		timer.Stop()
		t.Fatal(ctx.Err())
	}
	stopped := time.Now()
	if err := unix.Getrusage(unix.RUSAGE_SELF, &after); err != nil {
		t.Fatal(err)
	}
	cpu := time.Duration(after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano())
	t.Logf("128 waiting sockets: wall=%s supervisor CPU=%s measurement=[%d,%d]", stopped.Sub(started), cpu, started.UnixMicro(), stopped.UnixMicro())
	if err := session.SendStdin(ctx, []byte("go\n")); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatalf("load helper: %v %s", err, output.String())
		}
		output.Write(frame.Data)
		if frame.Type == FrameExit {
			if frame.Code != 0 || !strings.Contains(output.String(), "load-ok") {
				t.Fatalf("load helper failed: %s", output.String())
			}
			break
		}
	}
}

func TestDNSRecvmsgLoadHelper(t *testing.T) {
	if os.Getenv("DNS_RECVMSG_LOAD") != "1" {
		return
	}
	const count = 128
	var sockets []int
	defer func() {
		for _, fd := range sockets {
			_ = unix.Close(fd)
		}
	}()
	tids := make(chan int, count)
	results := make(chan error, count)
	for i := 0; i < count; i++ {
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
		if err != nil {
			t.Fatal(err)
		}
		sockets = append(sockets, fd)
		if err := unix.Connect(fd, &unix.SockaddrInet4{Addr: [4]byte{8, 8, 8, 8}, Port: 53}); err != nil {
			t.Fatal(err)
		}
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			tids <- unix.Gettid()
			var n int
			var err error
			for {
				n, _, _, _, err = unix.Recvmsg(fd, make([]byte, 4096), nil, 0)
				if !errors.Is(err, unix.EINTR) {
					break
				}
			}
			if err == nil && n == 0 {
				err = fmt.Errorf("empty response")
			}
			results <- err
		}()
	}
	for i := 0; i < count; i++ {
		tid := <-tids
		waitForRecvmsgSyscall(t, tid)
		// Interrupt queued receives while the supervisor acquires notifications.
		// ENOENT from NOTIF_RECV must not terminate the listener's serving loop.
		if err := unix.Tgkill(os.Getpid(), tid, unix.SIGURG); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("load-waiting")
	var command string
	if _, err := fmt.Fscanln(os.Stdin, &command); err != nil {
		t.Fatal(err)
	}
	for _, fd := range sockets {
		for {
			_, err := unix.SendmsgN(fd, buildDNSQuery("load.example", 1), nil, nil, 0)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for i := 0; i < count; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-deadline.C:
			t.Fatal("DNS waiters starved concurrent sendmsg")
		}
	}
	fmt.Println("load-ok")
}
