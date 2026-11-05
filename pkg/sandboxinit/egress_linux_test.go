//go:build linux

package sandboxinit

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"
)

// TestMain lets this test binary act as the init trampoline. The production
// entry point lives in cmd/ac-init, but the host tests run the init server
// in-process, so os.Executable() is this test binary; intercept the internal
// flag before the testing package parses its own flags.
func TestMain(m *testing.M) {
	if index := execChildIndex(os.Args); index >= 0 {
		args := os.Args[index+1:]
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if err := RunExecChild(args); err != nil {
			fmt.Fprintln(os.Stderr, "sandbox init exec child failed:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func execChildIndex(args []string) int {
	for index, arg := range args {
		if arg == "--exec-child" {
			return index
		}
	}
	return -1
}

func TestEgressFilterProgram(t *testing.T) {
	program := buildEgressFilter(0x1234, egressFilterSyscalls{Connect: 42, IOUringSetup: 43, Sendto: 44, Sendmsg: 45, Recvfrom: 46, Getpeername: 47, Recvmsg: 48})
	if len(program) != 23 {
		t.Fatalf("filter length = %d, want 23", len(program))
	}
	if program[0].K != seccompDataArch {
		t.Fatalf("first load offset = %d, want %d", program[0].K, seccompDataArch)
	}
	if program[4].K != uint32(unix.SECCOMP_RET_USER_NOTIF) {
		t.Fatalf("connect action = %#x, want SECCOMP_RET_USER_NOTIF", program[4].K)
	}
	if program[6].K != uint32(unix.SECCOMP_RET_ERRNO)|uint32(unix.EPERM) {
		t.Fatalf("io_uring action = %#x, want SECCOMP_RET_ERRNO(EPERM)", program[6].K)
	}
	if program[8].K != uint32(unix.SECCOMP_RET_USER_NOTIF) {
		t.Fatalf("sendmsg action = %#x, want SECCOMP_RET_USER_NOTIF", program[8].K)
	}
	if program[10].K != uint32(unix.SECCOMP_RET_USER_NOTIF) {
		t.Fatalf("recvfrom action = %#x, want SECCOMP_RET_USER_NOTIF", program[10].K)
	}
	if program[12].K != uint32(unix.SECCOMP_RET_USER_NOTIF) {
		t.Fatalf("getpeername action = %#x, want SECCOMP_RET_USER_NOTIF", program[12].K)
	}
	if program[14].K != uint32(unix.SECCOMP_RET_USER_NOTIF) {
		t.Fatalf("recvmsg action = %#x, want SECCOMP_RET_USER_NOTIF", program[14].K)
	}
	if program[16].K != seccompDataArgs+4*8 || program[18].K != seccompDataArgs+4*8+4 {
		t.Fatalf("sendto pointer offsets = %d/%d, want %d/%d", program[16].K, program[18].K, seccompDataArgs+4*8, seccompDataArgs+4*8+4)
	}
	if program[22].K != uint32(unix.SECCOMP_RET_KILL_PROCESS) {
		t.Fatalf("arch mismatch action = %#x, want SECCOMP_RET_KILL_PROCESS", program[22].K)
	}
}

func TestParseKernelRelease(t *testing.T) {
	cases := []struct {
		release string
		major   int
		minor   int
		wantErr bool
	}{
		{release: "6.8.0", major: 6, minor: 8},
		{release: "5.5.19-1-MANJARO", major: 5, minor: 5},
		{release: "4.19.0", major: 4, minor: 19},
		{release: "broken", wantErr: true},
	}
	for _, test := range cases {
		major, minor, err := parseKernelRelease(test.release)
		if test.wantErr {
			if err == nil {
				t.Fatalf("parseKernelRelease(%q) error = nil, want a failure", test.release)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseKernelRelease(%q) error = %v", test.release, err)
		}
		if major != test.major || minor != test.minor {
			t.Fatalf("parseKernelRelease(%q) = %d.%d, want %d.%d", test.release, major, minor, test.major, test.minor)
		}
	}
}

func TestInitEgressAuditReportsConnect(t *testing.T) {
	if err := egressInterceptionSupported(); err != nil {
		t.Skipf("seccomp user notification is unavailable: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
		accepted <- err
	}()

	events := make(chan EgressAuditEvent, 4)
	_, socket := startTestInitWithOptions(t, Options{
		MainShell:       "sleep 120",
		EgressMode:      EgressModeAudit,
		EgressAuditSink: func(event EgressAuditEvent) { events <- event },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	target := listener.Addr().String()
	output, err := runEgressHelper(ctx, socket, target)
	if err != nil {
		t.Fatalf("run helper: %v", err)
	}
	if !strings.Contains(output, "dial-ok") {
		t.Fatalf("helper output = %q, want a successful dial", output)
	}
	select {
	case event := <-events:
		assertAuditEvent(t, event, target)
	case <-time.After(10 * time.Second):
		t.Fatal("no egress audit event was reported")
	}
	if err := <-accepted; err != nil {
		t.Fatalf("listener accept error = %v", err)
	}
}

func TestInitEgressAuditTTYSession(t *testing.T) {
	if err := egressInterceptionSupported(); err != nil {
		t.Skipf("seccomp user notification is unavailable: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
		accepted <- acceptErr
	}()
	events := make(chan EgressAuditEvent, 2)
	_, socket := startTestInitWithOptions(t, Options{
		MainShell:       "sleep 120",
		EgressMode:      EgressModeAudit,
		EgressAuditSink: func(event EgressAuditEvent) { events <- event },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	target := listener.Addr().String()
	output, err := runEgressHelperTTY(ctx, socket, target)
	if err != nil {
		t.Fatalf("run tty helper: %v", err)
	}
	if !strings.Contains(output, "dial-ok") {
		t.Fatalf("tty helper output = %q, want a successful dial", output)
	}
	select {
	case event := <-events:
		assertAuditEvent(t, event, target)
	case <-time.After(10 * time.Second):
		t.Fatal("no egress audit event was reported for the tty session")
	}
	if err := <-accepted; err != nil {
		t.Fatalf("listener accept error = %v", err)
	}
}

func TestInitEgressAuditConcurrentSessions(t *testing.T) {
	if err := egressInterceptionSupported(); err != nil {
		t.Skipf("seccomp user notification is unavailable: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	const sessions = 3
	var acceptWG sync.WaitGroup
	acceptWG.Add(sessions)
	go func() {
		for i := 0; i < sessions; i++ {
			conn, acceptErr := listener.Accept()
			if acceptErr == nil {
				_ = conn.Close()
			}
			acceptWG.Done()
		}
	}()

	events := make(chan EgressAuditEvent, sessions*2)
	_, socket := startTestInitWithOptions(t, Options{
		MainShell:       "sleep 120",
		EgressMode:      EgressModeAudit,
		EgressAuditSink: func(event EgressAuditEvent) { events <- event },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	target := listener.Addr().String()
	errs := make(chan error, sessions)
	var runWG sync.WaitGroup
	for i := 0; i < sessions; i++ {
		runWG.Add(1)
		go func() {
			defer runWG.Done()
			output, err := runEgressHelper(ctx, socket, target)
			if err != nil {
				errs <- err
				return
			}
			if !strings.Contains(output, "dial-ok") {
				errs <- fmt.Errorf("helper output = %q, want dial-ok", output)
			}
		}()
	}
	runWG.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent helper: %v", err)
	}
	acceptWG.Wait()
	for i := 0; i < sessions; i++ {
		select {
		case event := <-events:
			assertAuditEvent(t, event, target)
		case <-time.After(10 * time.Second):
			t.Fatalf("collected %d of %d egress audit events", i, sessions)
		}
	}
}

func assertAuditEvent(t *testing.T, event EgressAuditEvent, target string) {
	t.Helper()
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if event.Family != "inet" || event.Host != host || event.Port != port {
		t.Fatalf("event = %+v, want %s/%d (%s)", event, host, port, target)
	}
	if event.PID <= 0 {
		t.Fatalf("event.PID = %d, want the connecting process", event.PID)
	}
}

// runEgressHelper runs this test binary as the workload under init. The helper
// function dials the target, which the init supervisor must observe.
func runEgressHelper(ctx context.Context, socket, target string) (string, error) {
	return runEgressHelperMode(ctx, socket, "dial", target)
}

func runEgressHelperMode(ctx context.Context, socket, mode, target string) (string, error) {
	return runEgressHelperRequest(ctx, socket, Request{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestEgressHelperProcess$"},
		Env: map[string]string{
			"GO_WANT_INIT_EGRESS_HELPER": "1",
			"INIT_EGRESS_HELPER_MODE":    mode,
			"INIT_EGRESS_HELPER_TARGET":  target,
		},
	})
}

func runEgressHelperTTY(ctx context.Context, socket, target string) (string, error) {
	return runEgressHelperRequest(ctx, socket, Request{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestEgressHelperProcess$"},
		Env: map[string]string{
			"GO_WANT_INIT_EGRESS_HELPER": "1",
			"INIT_EGRESS_HELPER_TARGET":  target,
		},
		TTY:  true,
		Rows: 24,
		Cols: 80,
	})
}

func runEgressHelperRequest(ctx context.Context, socket string, request Request) (string, error) {
	client := NewClient(socket)
	session, err := client.Exec(ctx, request)
	if err != nil {
		return "", fmt.Errorf("exec helper: %w", err)
	}
	defer func() { _ = session.Close() }()
	var output strings.Builder
	exitCode := -1
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			return output.String(), fmt.Errorf("read helper frame: %w", err)
		}
		switch frame.Type {
		case FrameStdout, FrameStderr:
			output.Write(frame.Data)
		case FrameExit:
			exitCode = frame.Code
		}
		if frame.Type == FrameExit {
			break
		}
	}
	if exitCode != 0 {
		return output.String(), fmt.Errorf("helper exit code = %d; output: %s", exitCode, output.String())
	}
	return output.String(), nil
}

// TestEgressHelperProcess is the workload. It only runs when the parent test
// starts this binary through init with GO_WANT_INIT_EGRESS_HELPER=1.
func TestEgressHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_INIT_EGRESS_HELPER") != "1" {
		return
	}
	status, err := os.ReadFile("/proc/self/status")
	if err == nil {
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "Seccomp_filters:") {
				fmt.Printf("filters:%s\n", strings.TrimSpace(strings.TrimPrefix(line, "Seccomp_filters:")))
				break
			}
		}
	}
	mode := os.Getenv("INIT_EGRESS_HELPER_MODE")
	if mode == "hardening" {
		fmt.Printf("unshare:%v\n", unix.Unshare(0))
		fmt.Printf("mount:%v\n", unix.Mount("none", "/tmp", "tmpfs", 0, ""))
		_, _, errno := unix.Syscall6(unix.SYS_SENDMMSG, 0, 0, 0, 0, 0, 0)
		fmt.Printf("sendmmsg:%v\n", errno)
		return
	}
	if mode == "env" {
		fmt.Printf("http-proxy:%s\n", os.Getenv("HTTP_PROXY"))
		fmt.Printf("https-proxy:%s\n", os.Getenv("HTTPS_PROXY"))
		fmt.Printf("no-proxy:%s\n", os.Getenv("NO_PROXY"))
		return
	}
	if mode == "udp-sendmsg-ancillary" {
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
		if err != nil {
			fmt.Printf("socket-error:%v\n", err)
			os.Exit(1)
		}
		defer func() { _ = unix.Close(fd) }()
		control := unix.PktInfo4(&unix.Inet4Pktinfo{Spec_dst: [4]byte{127, 0, 0, 1}})
		if _, err := unix.SendmsgN(fd, []byte("probe"), control, &unix.SockaddrInet4{Addr: [4]byte{8, 8, 8, 8}, Port: 9999}, 0); err != nil {
			fmt.Printf("sendmsg-error:%v\n", err)
			return
		}
		fmt.Println("sendmsg-ok")
		return
	}
	target := os.Getenv("INIT_EGRESS_HELPER_TARGET")
	if mode == "udp-connected-dns" {
		query := buildDNSQuery(os.Getenv("INIT_EGRESS_HELPER_DNS_NAME"), dnsmessage.TypeA)
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
		if err != nil {
			fmt.Printf("socket-error:%v\n", err)
			os.Exit(1)
		}
		defer func() { _ = unix.Close(fd) }()
		if err := unix.Connect(fd, &unix.SockaddrInet4{Addr: [4]byte{8, 8, 8, 8}, Port: 53}); err != nil {
			fmt.Printf("connect-error:%v\n", err)
			os.Exit(1)
		}
		peer, err := unix.Getpeername(fd)
		if err != nil {
			fmt.Printf("getpeername-error:%v\n", err)
			os.Exit(1)
		}
		peerAddr, _ := peer.(*unix.SockaddrInet4)
		if peerAddr == nil {
			fmt.Printf("peer-family:%T\n", peer)
			os.Exit(1)
		}
		fmt.Printf("peer %s:%d\n", net.IP(peerAddr.Addr[:]), peerAddr.Port)
		if err := unix.Send(fd, query, 0); err != nil {
			fmt.Printf("send-error:%v\n", err)
			os.Exit(1)
		}
		buffer := make([]byte, 4096)
		read, from, err := unix.Recvfrom(fd, buffer, 0)
		if err != nil {
			fmt.Printf("recvfrom-error:%v\n", err)
			os.Exit(1)
		}
		if source, ok := from.(*unix.SockaddrInet4); ok {
			fmt.Printf("source %s:%d\n", net.IP(source.Addr[:]), source.Port)
		}
		ip, err := firstAAnswer(buffer[:read])
		if err != nil {
			fmt.Printf("dns-parse-error:%v\n", err)
			os.Exit(1)
		}
		fmt.Printf("synthetic %s\n", ip)
		return
	}
	if mode == "udp-dns" || mode == "udp-sendmsg" {
		name := os.Getenv("INIT_EGRESS_HELPER_DNS_NAME")
		query := buildDNSQuery(name, dnsmessage.TypeA)
		var response []byte
		if mode == "udp-dns" {
			conn, err := net.ListenPacket("udp", "0.0.0.0:0")
			if err != nil {
				fmt.Printf("udp-error:%v\n", err)
				os.Exit(1)
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.WriteTo(query, &net.UDPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 53}); err != nil {
				fmt.Printf("sendto-error:%v\n", err)
				os.Exit(1)
			}
			buffer := make([]byte, 4096)
			read, _, err := conn.ReadFrom(buffer)
			if err != nil {
				fmt.Printf("recvfrom-error:%v\n", err)
				os.Exit(1)
			}
			response = buffer[:read]
		} else {
			fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
			if err != nil {
				fmt.Printf("socket-error:%v\n", err)
				os.Exit(1)
			}
			defer func() { _ = unix.Close(fd) }()
			if _, err := unix.SendmsgN(fd, query, nil, &unix.SockaddrInet4{Addr: [4]byte{8, 8, 8, 8}, Port: 53}, 0); err != nil {
				fmt.Printf("sendmsg-error:%v\n", err)
				os.Exit(1)
			}
			buffer := make([]byte, 4096)
			read, _, err := unix.Recvfrom(fd, buffer, 0)
			if err != nil {
				fmt.Printf("recvfrom-error:%v\n", err)
				os.Exit(1)
			}
			response = buffer[:read]
		}
		ip, err := firstAAnswer(response)
		if err != nil {
			fmt.Printf("dns-parse-error:%v\n", err)
			os.Exit(1)
		}
		fmt.Printf("synthetic %s\n", ip)
		return
	}
	if mode == "dns-echo" {
		name := os.Getenv("INIT_EGRESS_HELPER_DNS_NAME")
		addresses, err := net.LookupHost(name)
		if err != nil || len(addresses) == 0 {
			fmt.Printf("dns-error:%v\n", err)
			os.Exit(1)
		}
		fmt.Printf("synthetic %s\n", addresses[0])
		target = net.JoinHostPort(addresses[0], "443")
	}
	conn, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		fmt.Printf("dial-error:%v\n", err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()
	if mode == "echo" || mode == "dns-echo" {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			fmt.Printf("write-error:%v\n", err)
			os.Exit(1)
		}
		echoed := make([]byte, 4)
		if _, err := io.ReadFull(conn, echoed); err != nil {
			fmt.Printf("read-error:%v\n", err)
			os.Exit(1)
		}
		fmt.Printf("tunnel-ok %s\n", echoed)
		return
	}
	fmt.Println("dial-ok")
}

// buildDNSQuery builds one UDP DNS query for the helper process.
func buildDNSQuery(name string, queryType dnsmessage.Type) []byte {
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 9, RecursionDesired: true})
	_ = builder.StartQuestions()
	_ = builder.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: queryType, Class: dnsmessage.ClassINET})
	message, err := builder.Finish()
	if err != nil {
		return nil
	}
	return message
}

// firstAAnswer returns the first A record of a DNS response.
func firstAAnswer(message []byte) (net.IP, error) {
	var parser dnsmessage.Parser
	if _, err := parser.Start(message); err != nil {
		return nil, err
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return nil, err
	}
	answers, err := parser.AllAnswers()
	if err != nil {
		return nil, err
	}
	for _, answer := range answers {
		if resource, ok := answer.Body.(*dnsmessage.AResource); ok {
			return net.IP(resource.A[:]), nil
		}
	}
	return nil, fmt.Errorf("response has no A record")
}

func TestHardeningFilterProgram(t *testing.T) {
	program := buildHardeningFilter(0x1234, []uint32{42, 43})
	// arch load + arch check + nr load + two (check, ret) pairs + allow + kill.
	if len(program) != 9 {
		t.Fatalf("filter length = %d, want 9", len(program))
	}
	if program[1].K != 0x1234 {
		t.Fatalf("arch check = %#x, want 0x1234", program[1].K)
	}
	if program[1].Jf != 6 {
		t.Fatalf("arch mismatch jump = %d, want 6 (kill at 8)", program[1].Jf)
	}
	if program[3].K != 42 || program[5].K != 43 {
		t.Fatalf("syscall checks = %d/%d, want 42/43", program[3].K, program[5].K)
	}
	if program[4].K != uint32(unix.SECCOMP_RET_ERRNO)|uint32(unix.EPERM) || program[6].K != program[4].K {
		t.Fatalf("deny actions = %#x/%#x, want EPERM", program[4].K, program[6].K)
	}
	if program[7].K != uint32(unix.SECCOMP_RET_ALLOW) || program[8].K != uint32(unix.SECCOMP_RET_KILL_PROCESS) {
		t.Fatalf("terminal actions = %#x/%#x", program[7].K, program[8].K)
	}
}
