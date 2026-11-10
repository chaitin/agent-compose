//go:build linux

package sandboxinit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/sandboxsupervisor"
	"golang.org/x/sys/unix"
)

// egressRelayTarget is the independent supervisor the init process relays
// through when the workload container has no external network interface. The
// token is read from init's environment and removed before any workload
// process starts, so it is not part of the workload environment.
type egressRelayTarget struct {
	socketPath string
	token      string
	sandboxID  string
	timeout    time.Duration
}

// loadEgressRelayTarget resolves the relay configuration and removes its
// variables from init's environment. A configured relay without a token or
// socket is an error rather than a silent fallback to direct egress: the
// workload has no path to the daemon proxy and must fail closed.
func loadEgressRelayTarget(sandboxID string) (*egressRelayTarget, map[string][]net.IP, error) {
	socketPath := strings.TrimSpace(os.Getenv(EgressRelaySocketEnvVar))
	rawEngineHosts := strings.TrimSpace(os.Getenv(EgressEngineHostsEnvVar))
	passthrough, err := parseEgressEngineHosts(rawEngineHosts)
	if err != nil {
		return nil, nil, err
	}
	// The socket path and engine answers are not secrets; the token is
	// delivered later over the authenticated init control socket, so a Docker
	// exec process cannot read it from the container configuration.
	for _, name := range []string{EgressRelaySocketEnvVar, EgressEngineHostsEnvVar} {
		_ = os.Unsetenv(name)
	}
	if socketPath == "" {
		return nil, nil, fmt.Errorf("%s names no supervisor socket while %s=enforce", EgressRelaySocketEnvVar, EgressModeEnvVar)
	}
	return &egressRelayTarget{
		socketPath: socketPath,
		sandboxID:  sandboxID,
		timeout:    egressConnectTimeout,
	}, passthrough, nil
}

// parseEgressEngineHosts decodes the daemon-resolved engine hostnames. The
// sandbox resolver can no longer resolve them itself because it has no
// external interface, so the daemon supplies their addresses at creation.
func parseEgressEngineHosts(raw string) (map[string][]net.IP, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var values map[string][]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("parse %s: %w", EgressEngineHostsEnvVar, err)
	}
	passthrough := make(map[string][]net.IP, len(values))
	for host, addresses := range values {
		name, ok := normalizeDNSName(host)
		if !ok {
			return nil, fmt.Errorf("%s names an invalid engine host %q", EgressEngineHostsEnvVar, host)
		}
		for _, address := range addresses {
			ip := net.ParseIP(strings.TrimSpace(address))
			if ip == nil {
				return nil, fmt.Errorf("%s gives engine host %q an invalid address %q", EgressEngineHostsEnvVar, host, address)
			}
			passthrough[name] = append(passthrough[name], ip)
		}
	}
	return passthrough, nil
}

// relayConnect redirects one mediated TCP connection to the independent
// supervisor. The duplicated workload socket is connected to a per-connection
// loopback listener; the relay matches the accepted peer's source port against
// the socket's own bound port, so another process in the workload namespace
// cannot race in and inherit the trusted init's egress path.
//
//nolint:revive // the boundary payload is intentionally positional and self-describing at the call site
func (s *egressSupervisor) relayConnect(listenerFD int, id uint64, targetFD, domain int, destination EgressAuditEvent, event EgressAuditEvent) {
	token := s.relayToken()
	if token == "" {
		s.denyConnect(listenerFD, id, event, "sandbox supervisor relay is not configured")
		return
	}
	if err := s.relay.relay(targetFD, domain, destination, token); err != nil {
		s.denyConnect(listenerFD, id, event, "sandbox supervisor relay failed: "+err.Error())
		return
	}
	event.Decision = "allow"
	event.Reason = "supervisor"
	s.record(event)
	replyConnect(listenerFD, id, 0)
}

// relay bridges one duplicated workload socket to the supervisor. It returns
// after the supervisor accepted the destination and the copy goroutine owns
// both connections.
func (r *egressRelayTarget) relay(targetFD, domain int, destination EgressAuditEvent, token string) error {
	listener, err := listenLoopback(domain)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()

	sourcePort, err := bindAndConnectLoopback(targetFD, listener.Addr(), r.timeout)
	if err != nil {
		return err
	}
	accepted, err := acceptRelayedPeer(listener, sourcePort, r.timeout)
	if err != nil {
		return err
	}
	client := &sandboxsupervisor.Client{
		SocketPath: r.socketPath,
		Token:      token,
		SandboxID:  r.sandboxID,
		Timeout:    r.timeout,
	}
	upstream, err := client.Connect(context.Background(), net.JoinHostPort(destination.Host, strconv.Itoa(destination.Port)), sandboxsupervisor.Identity{
		SessionID: destination.SessionID,
		Marker:    destination.Marker,
		PID:       destination.PID,
		Binary:    destination.Binary,
	})
	if err != nil {
		_ = accepted.Close()
		return err
	}
	go copyRelayed(accepted, upstream)
	return nil
}

// listenLoopback binds the per-connection relay on the workload's own
// loopback. IPv4 and IPv6 sockets get a listener of their own family so the
// duplicated socket can connect without a family mismatch.
func listenLoopback(domain int) (net.Listener, error) {
	network := "tcp4"
	address := "127.0.0.1:0"
	if domain == unix.AF_INET6 {
		network = "tcp6"
		address = "[::1]:0"
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, fmt.Errorf("listen on the sandbox relay loopback: %w", err)
	}
	return listener, nil
}

// bindAndConnectLoopback binds the duplicated socket to a loopback source
// address, records the source port the kernel selected, and connects it to the
// relay listener. Binding is what makes the accept check authoritative: the
// port stays reserved for this socket, so no other process can present it.
func bindAndConnectLoopback(targetFD int, listenerAddress net.Addr, timeout time.Duration) (int, error) {
	family, err := socketFamily(targetFD)
	if err != nil {
		return 0, err
	}
	if err := bindLoopbackSource(targetFD, family); err != nil {
		return 0, err
	}
	local, err := unix.Getsockname(targetFD)
	if err != nil {
		return 0, fmt.Errorf("read the mediated socket's local address: %w", err)
	}
	sourcePort := sockaddrPort(local)
	if sourcePort <= 0 {
		return 0, errors.New("mediated socket has no bound source port")
	}
	tcpAddress, ok := listenerAddress.(*net.TCPAddr)
	if !ok {
		return 0, errors.New("sandbox relay listener address is not TCP")
	}
	rawAddress, err := sockaddrFromTCPAddr(tcpAddress)
	if err != nil {
		return 0, err
	}
	if errno := connectSockaddr(targetFD, rawAddress, timeout); errno != 0 {
		return 0, fmt.Errorf("connect the mediated socket to the sandbox relay: %w", errno)
	}
	return sourcePort, nil
}

func socketFamily(fd int) (int, error) {
	domain, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil {
		return 0, fmt.Errorf("read the mediated socket's family: %w", err)
	}
	return domain, nil
}

func bindLoopbackSource(fd, family int) error {
	var address unix.Sockaddr
	switch family {
	case unix.AF_INET6:
		sockaddr := &unix.SockaddrInet6{}
		copy(sockaddr.Addr[:], net.IPv6loopback.To16())
		address = sockaddr
	default:
		address = &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}
	}
	if err := unix.Bind(fd, address); err != nil {
		// A client that already bound its socket keeps that binding; the
		// source port it holds is still sufficient for the accept check.
		if errors.Is(err, unix.EINVAL) {
			return nil
		}
		return fmt.Errorf("bind the mediated socket to loopback: %w", err)
	}
	return nil
}

func sockaddrPort(address unix.Sockaddr) int {
	switch value := address.(type) {
	case *unix.SockaddrInet4:
		return value.Port
	case *unix.SockaddrInet6:
		return value.Port
	default:
		return 0
	}
}

// acceptRelayedPeer accepts exactly the connection the duplicated socket just
// made. A connection from any other source port is closed and the relay keeps
// waiting until the deadline, so a workload process that scans loopback cannot
// take the initiative.
func acceptRelayedPeer(listener net.Listener, sourcePort int, timeout time.Duration) (net.Conn, error) {
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		return nil, errors.New("sandbox relay listener is not TCP")
	}
	deadline := time.Now().Add(timeout)
	if err := tcpListener.SetDeadline(deadline); err != nil {
		return nil, err
	}
	for {
		conn, err := tcpListener.Accept()
		if err != nil {
			return nil, fmt.Errorf("accept the mediated socket on the sandbox relay: %w", err)
		}
		peer, ok := conn.RemoteAddr().(*net.TCPAddr)
		if ok && peer.Port == sourcePort {
			return conn, nil
		}
		_ = conn.Close()
	}
}

// copyRelayed bridges the loopback socket and the supervisor stream until
// either side closes.
func copyRelayed(left, right net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(left, right)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(right, left)
		done <- struct{}{}
	}()
	<-done
	_ = left.Close()
	_ = right.Close()
	<-done
}
