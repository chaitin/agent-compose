//go:build linux

package sandboxinit

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// egressConnectTimeout bounds the connect and CONNECT handshake init
	// performs for one mediated connection. The target thread stays blocked in
	// the seccomp notification for that long, so it must stay small.
	egressConnectTimeout = 10 * time.Second
	// egressProxyHeaderLimit bounds the CONNECT response header.
	egressProxyHeaderLimit = 4096
	// egressProxyEnvNames are removed from init's environment in enforce mode
	// after the credential is read, so a workload started later does not inherit
	// the proxy credential.
	egressProxyEnvNames = 6
)

// proxyEnvNames lists the proxy variables in both cases.
var proxyEnvNames = [egressProxyEnvNames]string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",
}

// egressProxyTarget is the daemon egress proxy init connects to. The address is
// resolved once at init startup; the credential is held only in this process.
type egressProxyTarget struct {
	rawAddr    []byte
	authority  string
	authHeader string
}

// egressDenied reports a proxy refusal rather than a transport failure.
var errEgressDenied = errors.New("sandbox init egress proxy denied the destination")

// denyErrno is the error a workload sees for a refused mediated connect.
const denyErrno = unix.ECONNREFUSED

func loadEgressProxyTarget() (*egressProxyTarget, error) {
	raw := firstProxyEnv()
	if raw == "" {
		return nil, errors.New("no sandbox proxy credential is present; the daemon did not install the egress proxy for this sandbox")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse sandbox proxy url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("sandbox proxy url has unsupported scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, errors.New("sandbox proxy url has no host")
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	address, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox proxy %s: %w", net.JoinHostPort(host, port), err)
	}
	rawAddr, err := sockaddrFromTCPAddr(address)
	if err != nil {
		return nil, err
	}
	authHeader := ""
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(parsed.User.Username()+":"+password))
	}
	target := &egressProxyTarget{
		rawAddr:    rawAddr,
		authority:  net.JoinHostPort(host, port),
		authHeader: authHeader,
	}
	for _, name := range proxyEnvNames {
		_ = os.Unsetenv(name)
	}
	return target, nil
}

func firstProxyEnv() string {
	for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// buildAddressSets separates sandbox-local addresses (local services, safe to
// reach directly for any socket type) from the wider bypass set that also
// includes the proxy itself (a proxy-aware client must not be proxied twice).
func buildAddressSets(proxy *egressProxyTarget) (map[string]struct{}, map[string]struct{}) {
	local := make(map[string]struct{})
	bypass := make(map[string]struct{})
	if proxy != nil {
		if address, ok := tcpAddrFromSockaddr(proxy.rawAddr); ok {
			bypass[address.IP.String()] = struct{}{}
		}
	}
	addresses, err := net.InterfaceAddrs()
	if err == nil {
		for _, address := range addresses {
			if ip, _, err := net.ParseCIDR(address.String()); err == nil {
				local[ip.String()] = struct{}{}
				bypass[ip.String()] = struct{}{}
			}
		}
	}
	return local, bypass
}

// handleEnforce is the mediated path: duplicate the target socket, connect it
// to the proxy, perform the CONNECT handshake, and spoof the outcome.
func (s *egressSupervisor) handleEnforce(listenerFD int, request *seccompNotif, sessionID, marker string) {
	event := EgressAuditEvent{
		SandboxID: s.sandboxID,
		SessionID: sessionID,
		Marker:    marker,
		PID:       int(request.PID),
	}
	if !notificationIDValid(listenerFD, request.ID) {
		return
	}
	rawAddress, err := readConnectAddress(int(request.PID), request.Data.Args[1], uint32(request.Data.Args[2]))
	if err != nil {
		s.denyConnect(listenerFD, request.ID, event, "cannot read the connect address")
		return
	}
	destination, err := decodeConnectAddress(rawAddress)
	if err != nil {
		s.denyConnect(listenerFD, request.ID, event, "cannot decode the connect address")
		return
	}
	destination.SandboxID = event.SandboxID
	destination.SessionID = event.SessionID
	destination.Marker = event.Marker
	destination.PID = event.PID
	event = destination

	targetFD, err := duplicateTargetFD(int(request.PID), int(request.Data.Args[0]))
	if err != nil {
		s.denyConnect(listenerFD, request.ID, event, "cannot duplicate the workload socket")
		return
	}
	defer func() { _ = unix.Close(targetFD) }()

	socketType, err := unix.GetsockoptInt(targetFD, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil {
		s.denyConnect(listenerFD, request.ID, event, "cannot read the workload socket type")
		return
	}
	// Local IPC and sandbox-local services are not egress; they keep working
	// for stream, datagram, and Unix sockets alike.
	if destination.Family == "unix" || s.isLocalDestination(destination) {
		s.connectDirect(listenerFD, request.ID, targetFD, rawAddress, event)
		return
	}
	if socketType != unix.SOCK_STREAM {
		// DNS stays on its current path until the policy DNS stub lands. Any
		// other datagram traffic has no mediated representation yet.
		if destination.Port == 53 {
			s.connectDirect(listenerFD, request.ID, targetFD, rawAddress, event)
			return
		}
		s.denyConnect(listenerFD, request.ID, event, "datagram egress is not mediated")
		return
	}
	if s.isBypassed(destination) {
		s.connectDirect(listenerFD, request.ID, targetFD, rawAddress, event)
		return
	}
	s.mediateConnect(listenerFD, request.ID, targetFD, destination, event)
}

func (s *egressSupervisor) isBypassed(destination EgressAuditEvent) bool {
	return s.isLocalDestination(destination) || s.ipInSet(destination.Host, s.bypassIPs)
}

// isLocalDestination reports a sandbox-local address: loopback or one of the
// sandbox's own interfaces. Local traffic never leaves the sandbox, so it is
// not subject to egress policy.
func (s *egressSupervisor) isLocalDestination(destination EgressAuditEvent) bool {
	if destination.Family == "unix" {
		return true
	}
	if ip := net.ParseIP(destination.Host); ip != nil && ip.IsLoopback() {
		return true
	}
	return s.ipInSet(destination.Host, s.localIPs)
}

func (s *egressSupervisor) ipInSet(host string, set map[string]struct{}) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	_, ok := set[ip.String()]
	return ok
}

// connectDirect connects the duplicated socket to the address the workload
// supplied. It is used for local destinations and the proxy itself.
func (s *egressSupervisor) connectDirect(listenerFD int, id uint64, targetFD int, rawAddress []byte, event EgressAuditEvent) {
	if errno := connectSockaddr(targetFD, rawAddress, s.connectTimeout); errno != 0 {
		event.Decision = "deny"
		event.Reason = fmt.Sprintf("direct connect failed: %v", errno)
		s.record(event)
		replyConnect(listenerFD, id, errno)
		return
	}
	event.Decision = "allow"
	event.Reason = "bypass"
	s.record(event)
	replyConnect(listenerFD, id, 0)
}

// mediateConnect redirects one TCP connection through the daemon egress proxy.
func (s *egressSupervisor) mediateConnect(listenerFD int, id uint64, targetFD int, destination EgressAuditEvent, event EgressAuditEvent) {
	if s.proxy == nil {
		s.denyConnect(listenerFD, id, event, "no sandbox egress proxy is configured")
		return
	}
	if errno := connectSockaddr(targetFD, s.proxy.rawAddr, s.connectTimeout); errno != 0 {
		s.denyConnect(listenerFD, id, event, "sandbox egress proxy is unreachable")
		return
	}
	if err := s.proxy.handshake(targetFD, destination, s.connectTimeout); err != nil {
		reason := "sandbox egress proxy handshake failed"
		if errors.Is(err, errEgressDenied) {
			reason = "sandbox egress proxy denied the destination"
		}
		s.denyConnect(listenerFD, id, event, reason)
		return
	}
	event.Decision = "allow"
	event.Reason = "proxied"
	s.record(event)
	replyConnect(listenerFD, id, 0)
}

func (s *egressSupervisor) denyConnect(listenerFD int, id uint64, event EgressAuditEvent, reason string) {
	event.Decision = "deny"
	event.Reason = reason
	s.record(event)
	replyConnect(listenerFD, id, denyErrno)
}

// handshake writes the CONNECT request and classifies the proxy response.
func (p *egressProxyTarget) handshake(fd int, destination EgressAuditEvent, timeout time.Duration) error {
	request := p.connectRequest(destination)
	if err := writeAll(fd, request, timeout); err != nil {
		return fmt.Errorf("write CONNECT request: %w", err)
	}
	header, err := readHeader(fd, timeout)
	if err != nil {
		return fmt.Errorf("read CONNECT response: %w", err)
	}
	statusLine := header
	if index := bytes.IndexByte(statusLine, '\n'); index >= 0 {
		statusLine = statusLine[:index]
	}
	fields := strings.Fields(string(statusLine))
	if len(fields) < 2 {
		return fmt.Errorf("malformed CONNECT response %q", statusLine)
	}
	switch fields[1] {
	case "200":
		return nil
	case "403", "407":
		return errEgressDenied
	default:
		return fmt.Errorf("sandbox egress proxy answered %s", fields[1])
	}
}

func (p *egressProxyTarget) connectRequest(destination EgressAuditEvent) []byte {
	authority := net.JoinHostPort(destination.Host, strconv.Itoa(destination.Port))
	var builder strings.Builder
	builder.WriteString("CONNECT ")
	builder.WriteString(authority)
	builder.WriteString(" HTTP/1.1\r\nHost: ")
	builder.WriteString(authority)
	builder.WriteString("\r\n")
	if p.authHeader != "" {
		builder.WriteString("Proxy-Authorization: ")
		builder.WriteString(p.authHeader)
		builder.WriteString("\r\n")
	}
	writeIdentityHeader(&builder, "X-Agent-Compose-Sandbox", destination.SandboxID)
	writeIdentityHeader(&builder, "X-Agent-Compose-Session", destination.SessionID)
	writeIdentityHeader(&builder, "X-Agent-Compose-Marker", destination.Marker)
	builder.WriteString("\r\n")
	return []byte(builder.String())
}

func writeIdentityHeader(builder *strings.Builder, name, value string) {
	value = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, value)
	if value == "" {
		return
	}
	builder.WriteString(name)
	builder.WriteString(": ")
	builder.WriteString(value)
	builder.WriteString("\r\n")
}

// duplicateTargetFD duplicates the workload's socket with pidfd_getfd, which is
// why init needs CAP_SYS_PTRACE inside Docker and Kubernetes sandboxes.
func duplicateTargetFD(pid, targetFD int) (int, error) {
	if pid <= 0 || targetFD < 0 {
		return -1, errors.New("invalid pid or file descriptor")
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return -1, fmt.Errorf("open pidfd for %d: %w", pid, err)
	}
	defer func() { _ = unix.Close(pidfd) }()
	fd, err := unix.PidfdGetfd(pidfd, targetFD, 0)
	if err != nil {
		return -1, fmt.Errorf("pidfd_getfd(%d, %d): %w", pid, targetFD, err)
	}
	unix.CloseOnExec(fd)
	return fd, nil
}

// connectSockaddr connects fd to a raw sockaddr. A non-blocking socket returns
// EINPROGRESS; the final result is read from SO_ERROR after polling.
func connectSockaddr(fd int, address []byte, timeout time.Duration) unix.Errno {
	if len(address) < 2 {
		return unix.EINVAL
	}
	errno := rawConnect(fd, address)
	if errno == 0 {
		return 0
	}
	if errno != unix.EINPROGRESS {
		return errno
	}
	if err := waitFD(fd, unix.POLLOUT, timeout); err != nil {
		return unix.ETIMEDOUT
	}
	soError, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
	if err != nil {
		return unix.EIO
	}
	return unix.Errno(soError)
}

func rawConnect(fd int, address []byte) unix.Errno {
	if len(address) == 0 {
		return unix.EINVAL
	}
	_, _, errno := unix.Syscall(unix.SYS_CONNECT, uintptr(fd), uintptr(unsafe.Pointer(&address[0])), uintptr(len(address)))
	return unix.Errno(errno)
}

// replyConnect spoofs the result of the intercepted connect. A zero errno is a
// successful connect; a non-zero one becomes -errno in the target.
func replyConnect(listenerFD int, id uint64, errno unix.Errno) {
	response := seccompNotifResp{ID: id}
	if errno != 0 {
		response.Error = -int32(errno)
	}
	_ = ioctlNotifSend(listenerFD, &response)
}

func waitFD(fd int, events int16, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return os.ErrDeadlineExceeded
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: events}}
		count, err := unix.Poll(fds, int(remaining/time.Millisecond)+1)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		if count == 0 {
			return os.ErrDeadlineExceeded
		}
		if fds[0].Revents&(events|unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return nil
		}
	}
}

func writeAll(fd int, data []byte, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for len(data) > 0 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return os.ErrDeadlineExceeded
		}
		if err := waitFD(fd, unix.POLLOUT, remaining); err != nil {
			return err
		}
		written, err := unix.Write(fd, data)
		if written > 0 {
			data = data[written:]
		}
		if err == nil || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
	return nil
}

func readHeader(fd int, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	buffer := make([]byte, 0, 256)
	one := make([]byte, 1)
	for {
		if len(buffer) > egressProxyHeaderLimit {
			return nil, errors.New("CONNECT response header is too large")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, os.ErrDeadlineExceeded
		}
		if err := waitFD(fd, unix.POLLIN, remaining); err != nil {
			return nil, err
		}
		read, err := unix.Read(fd, one)
		if read == 1 {
			buffer = append(buffer, one[0])
			if bytes.HasSuffix(buffer, []byte("\r\n\r\n")) {
				return buffer, nil
			}
			continue
		}
		if err == nil && read == 0 {
			return nil, errors.New("proxy closed the connection during CONNECT")
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
			continue
		}
		return nil, err
	}
}

func sockaddrFromTCPAddr(address *net.TCPAddr) ([]byte, error) {
	if ip4 := address.IP.To4(); ip4 != nil {
		buffer := make([]byte, 16)
		binary.NativeEndian.PutUint16(buffer[0:2], unix.AF_INET)
		binary.BigEndian.PutUint16(buffer[2:4], uint16(address.Port))
		copy(buffer[4:8], ip4)
		return buffer, nil
	}
	ip16 := address.IP.To16()
	if ip16 == nil {
		return nil, errors.New("sandbox proxy address is not an IP")
	}
	buffer := make([]byte, 28)
	binary.NativeEndian.PutUint16(buffer[0:2], unix.AF_INET6)
	binary.BigEndian.PutUint16(buffer[2:4], uint16(address.Port))
	copy(buffer[8:24], ip16)
	return buffer, nil
}

func tcpAddrFromSockaddr(raw []byte) (*net.TCPAddr, bool) {
	if len(raw) < 8 {
		return nil, false
	}
	switch binary.NativeEndian.Uint16(raw[0:2]) {
	case unix.AF_INET:
		return &net.TCPAddr{IP: net.IP(raw[4:8]), Port: int(binary.BigEndian.Uint16(raw[2:4]))}, true
	case unix.AF_INET6:
		if len(raw) < 24 {
			return nil, false
		}
		return &net.TCPAddr{IP: net.IP(raw[8:24]), Port: int(binary.BigEndian.Uint16(raw[2:4]))}, true
	default:
		return nil, false
	}
}
