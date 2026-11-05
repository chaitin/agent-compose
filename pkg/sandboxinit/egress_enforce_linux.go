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

	"github.com/chaitin/agent-compose/pkg/egress"
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

// dnsSocketDestination remembers the nameserver a workload socket was
// redirected away from, so a later recvfrom can report the same source address
// the client configured. Keyed by socket inode, which duplication and fork
// share.
type dnsSocketDestination struct {
	raw     []byte
	expires time.Time
}

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
// reach directly for any socket type) from the daemon network addresses that
// must not be proxied twice. The proxy endpoint itself is matched by host and
// port in isBypassed; an IP-wide bypass here would expose every other service
// on the daemon host to the workload, which is exactly what the outer default
// deny exists to prevent.
func buildAddressSets(proxy *egressProxyTarget) (map[string]struct{}, map[string]struct{}) {
	local := make(map[string]struct{})
	bypass := make(map[string]struct{})
	_ = proxy
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
		Binary:    processBinary(int(request.PID)),
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
	destination.Binary = event.Binary
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
	domain, err := unix.GetsockoptInt(targetFD, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil {
		s.denyConnect(listenerFD, request.ID, event, "cannot read the workload socket family")
		return
	}
	// Every DNS query goes to the sandbox's policy resolver, whatever address
	// the workload was configured with.
	if destination.Port == 53 && s.dns != nil {
		s.redirectToDNS(dnsRedirect{
			listenerFD:     listenerFD,
			id:             request.ID,
			targetFD:       targetFD,
			domain:         domain,
			rawDestination: rawAddress,
			event:          event,
		})
		return
	}
	// A synthetic answer is only meaningful as the name the workload asked
	// for; map it back so the daemon policy is evaluated against the name. An
	// unmapped synthetic address is refused rather than sent anywhere.
	if s.dns != nil {
		if ip := net.ParseIP(destination.Host); ip != nil {
			if hostname, ok := s.dns.hostnameForIP(ip); ok {
				destination.Host = hostname
				event = destination
			} else if s.dns.syntheticIP(ip) {
				s.denyConnect(listenerFD, request.ID, event, "synthetic DNS address is not mapped to a name")
				return
			}
		}
	}
	// Local IPC and sandbox-local services are not egress; they keep working
	// for stream, datagram, and Unix sockets alike.
	if destination.Family == "unix" || s.isLocalDestination(destination) {
		s.connectDirect(listenerFD, request.ID, targetFD, rawAddress, event)
		return
	}
	if socketType != unix.SOCK_STREAM {
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
	if s.isLocalDestination(destination) {
		return true
	}
	if s.proxyHost != "" && destination.Host == s.proxyHost && destination.Port == s.proxyPort {
		return true
	}
	return s.ipInSet(destination.Host, s.bypassIPs)
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

// redirectToDNS connects the duplicated socket to the local policy resolver,
// keeping the workload's own DNS wire format intact for UDP and TCP alike.
type dnsRedirect struct {
	listenerFD     int
	id             uint64
	targetFD       int
	domain         int
	rawDestination []byte
	event          EgressAuditEvent
}

func (s *egressSupervisor) redirectToDNS(redirect dnsRedirect) {
	listenerFD := redirect.listenerFD
	id := redirect.id
	targetFD := redirect.targetFD
	event := redirect.event
	rawAddress := s.dns.rawAddrForDomain(redirect.domain)
	if rawAddress == nil {
		s.denyConnect(listenerFD, id, event, "no sandbox DNS resolver for this address family")
		return
	}
	if errno := connectSockaddr(targetFD, rawAddress, s.connectTimeout); errno != 0 {
		event.Decision = "deny"
		event.Reason = fmt.Sprintf("connect to the sandbox DNS resolver failed: %v", errno)
		s.record(event)
		replyConnect(listenerFD, id, errno)
		return
	}
	s.rememberDNSSocket(targetFD, redirect.rawDestination)
	event.Decision = "allow"
	event.Reason = "dns-stub"
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
	writeIdentityHeader(&builder, egress.IdentityHeaderSandbox, destination.SandboxID)
	writeIdentityHeader(&builder, egress.IdentityHeaderSession, destination.SessionID)
	writeIdentityHeader(&builder, egress.IdentityHeaderMarker, destination.Marker)
	writeIdentityHeader(&builder, egress.IdentityHeaderPID, strconv.Itoa(destination.PID))
	if destination.Binary != "" {
		// Base64 keeps an arbitrary executable path from breaking the request
		// line or injecting another header. The proxy validates the decoded
		// value as an absolute executable path.
		writeIdentityHeader(&builder, egress.IdentityHeaderBinary, base64.StdEncoding.EncodeToString([]byte(destination.Binary)))
	}
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
	tgid := threadGroupID(pid)
	pidfd, err := unix.PidfdOpen(tgid, 0)
	if err != nil {
		return -1, fmt.Errorf("open pidfd for %d (thread %d): %w", tgid, pid, err)
	}
	defer func() { _ = unix.Close(pidfd) }()
	fd, err := unix.PidfdGetfd(pidfd, targetFD, 0)
	if err != nil {
		return -1, fmt.Errorf("pidfd_getfd(%d, %d): %w", tgid, targetFD, err)
	}
	unix.CloseOnExec(fd)
	return fd, nil
}

// processBinary returns the absolute executable path of a process, read from
// /proc/<pid>/exe. The kernel appends " (deleted)" when the executable was
// replaced after the process started; the suffix is stripped so the reported
// path is the one the process is actually running. A path that cannot be read,
// is not absolute, or contains a line break is reported as empty: a binary
// policy must fail closed for an attribution init could not establish.
func processBinary(pid int) string {
	if pid <= 0 {
		return ""
	}
	target, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return ""
	}
	target = strings.TrimSuffix(target, " (deleted)")
	if target == "" || !strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\r\n") {
		return ""
	}
	return target
}

// threadGroupID maps a seccomp notification's thread ID to its thread-group
// leader. The notification reports the thread that executed connect, which for
// pooled runtimes is usually not the leader, and pidfd_open only accepts the
// leader unless the newer PIDFD_THREAD flag is supported. The fd table is
// shared by the whole thread group, so any live thread resolves the same file
// description.
func threadGroupID(pid int) int {
	if pid <= 0 {
		return pid
	}
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return pid
	}
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "Tgid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return pid
		}
		tgid, err := strconv.Atoi(fields[1])
		if err != nil || tgid <= 0 {
			return pid
		}
		return tgid
	}
	return pid
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

// egressDatagramLimit bounds one emulated datagram DNS message.
const egressDatagramLimit = 64 * 1024

// targetMsghdr mirrors the 64-bit Linux struct msghdr. Only the fields the
// supervisor needs to identify an unconnected datagram destination and its
// payload are read.
type targetMsghdr struct {
	Name       uint64
	Namelen    uint32
	_          uint32
	Iov        uint64
	Iovlen     uint64
	Control    uint64
	Controllen uint64
	Flags      int32
	_          int32
}

// handleEnforceDatagram mediates sendto/sendmsg. Connected sendmessages and
// local destinations are continued unchanged; a datagram to port 53 is sent to
// the policy DNS from the duplicated workload socket, whose response arrives in
// the workload's own socket queue.
func (s *egressSupervisor) handleEnforceDatagram(listenerFD int, request *seccompNotif, sessionID, marker string) {
	event := EgressAuditEvent{
		SandboxID: s.sandboxID,
		SessionID: sessionID,
		Marker:    marker,
		PID:       int(request.PID),
		Binary:    processBinary(int(request.PID)),
	}
	if !notificationIDValid(listenerFD, request.ID) {
		return
	}
	pid := int(request.PID)
	nr := int32(request.Data.NR)
	var (
		destination    EgressAuditEvent
		rawDestination []byte
		payload        []byte
		spoofLen       int
	)
	switch nr {
	case int32(unix.SYS_SENDTO):
		destPointer := request.Data.Args[4]
		destLength := uint32(request.Data.Args[5])
		if destPointer == 0 || destLength == 0 {
			replyContinue(listenerFD, request.ID)
			return
		}
		var err error
		rawDestination, err = readConnectAddress(pid, destPointer, destLength)
		if err != nil {
			s.denyDatagram(listenerFD, request.ID, event, "cannot read the sendto destination")
			return
		}
		destination, err = decodeConnectAddress(rawDestination)
		if err != nil {
			s.denyDatagram(listenerFD, request.ID, event, "cannot decode the sendto destination")
			return
		}
		payload, err = readTargetMemoryBytes(pid, request.Data.Args[1], int(request.Data.Args[2]))
		if err != nil {
			s.denyDatagram(listenerFD, request.ID, event, "cannot read the datagram payload")
			return
		}
		spoofLen = len(payload)
	case int32(unix.SYS_SENDMSG):
		message, err := readTargetMsghdr(pid, request.Data.Args[1])
		if err != nil {
			s.denyDatagram(listenerFD, request.ID, event, "cannot read the sendmsg header")
			return
		}
		if message.Name == 0 || message.Namelen == 0 {
			// A connected sendmsg is already pointed at the policy DNS by its
			// connect, and intercepting every stream sendmsg would add an
			// upcall per write.
			replyContinue(listenerFD, request.ID)
			return
		}
		rawDestination, err = readConnectAddress(pid, message.Name, message.Namelen)
		if err != nil {
			s.denyDatagram(listenerFD, request.ID, event, "cannot read the sendmsg destination")
			return
		}
		destination, err = decodeConnectAddress(rawDestination)
		if err != nil {
			s.denyDatagram(listenerFD, request.ID, event, "cannot decode the sendmsg destination")
			return
		}
		if message.Control != 0 && message.Controllen > 0 {
			// Ancillary data has no safe mediated representation.
			replyContinue(listenerFD, request.ID)
			return
		}
		payload, err = readTargetIovecs(pid, message.Iov, message.Iovlen)
		if err != nil {
			s.denyDatagram(listenerFD, request.ID, event, "cannot read the sendmsg payload")
			return
		}
		spoofLen = len(payload)
	default:
		replyContinue(listenerFD, request.ID)
		return
	}
	destination.SandboxID = event.SandboxID
	destination.SessionID = event.SessionID
	destination.Marker = event.Marker
	destination.PID = event.PID
	destination.Binary = event.Binary
	event = destination
	if destination.Family == "unix" || s.isLocalDestination(destination) {
		replyContinue(listenerFD, request.ID)
		return
	}
	if destination.Port != 53 || s.dns == nil {
		s.denyDatagram(listenerFD, request.ID, event, "datagram egress is not mediated")
		return
	}
	targetFD, err := duplicateTargetFD(pid, int(request.Data.Args[0]))
	if err != nil {
		s.denyDatagram(listenerFD, request.ID, event, "cannot duplicate the workload socket")
		return
	}
	defer func() { _ = unix.Close(targetFD) }()
	socketType, err := unix.GetsockoptInt(targetFD, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil {
		s.denyDatagram(listenerFD, request.ID, event, "cannot read the workload socket type")
		return
	}
	if socketType != unix.SOCK_DGRAM {
		replyContinue(listenerFD, request.ID)
		return
	}
	domain, err := unix.GetsockoptInt(targetFD, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil {
		s.denyDatagram(listenerFD, request.ID, event, "cannot read the workload socket family")
		return
	}
	stubAddress := s.dns.rawAddrForDomain(domain)
	s.rememberDNSSocket(targetFD, rawDestination)
	if errno := sendDatagram(targetFD, payload, stubAddress, s.connectTimeout); errno != 0 {
		event.Decision = "deny"
		event.Reason = fmt.Sprintf("send to the sandbox DNS resolver failed: %v", errno)
		s.record(event)
		replyConnect(listenerFD, request.ID, errno)
		return
	}
	event.Decision = "allow"
	event.Reason = "dns-stub-datagram"
	s.record(event)
	_ = ioctlNotifSend(listenerFD, &seccompNotifResp{ID: request.ID, Val: int64(spoofLen)})
}

func (s *egressSupervisor) denyDatagram(listenerFD int, id uint64, event EgressAuditEvent, reason string) {
	event.Decision = "deny"
	event.Reason = reason
	s.record(event)
	replyConnect(listenerFD, id, denyErrno)
}

func replyContinue(listenerFD int, id uint64) {
	_ = ioctlNotifSend(listenerFD, &seccompNotifResp{ID: id, Flags: seccompUserNotifContinue})
}

func readTargetMemoryBytes(pid int, address uint64, length int) ([]byte, error) {
	if length < 0 || length > egressDatagramLimit {
		return nil, fmt.Errorf("datagram length %d is out of range", length)
	}
	if length == 0 {
		return nil, nil
	}
	buffer := make([]byte, length)
	if err := readTargetMemory(pid, address, buffer); err != nil {
		return nil, err
	}
	return buffer, nil
}

func readTargetMsghdr(pid int, address uint64) (targetMsghdr, error) {
	var message targetMsghdr
	buffer := make([]byte, unsafe.Sizeof(message))
	if err := readTargetMemory(pid, address, buffer); err != nil {
		return targetMsghdr{}, err
	}
	message.Name = binary.NativeEndian.Uint64(buffer[0:8])
	message.Namelen = binary.NativeEndian.Uint32(buffer[8:12])
	message.Iov = binary.NativeEndian.Uint64(buffer[16:24])
	message.Iovlen = binary.NativeEndian.Uint64(buffer[24:32])
	message.Control = binary.NativeEndian.Uint64(buffer[32:40])
	message.Controllen = binary.NativeEndian.Uint64(buffer[40:48])
	return message, nil
}

func readTargetIovecs(pid int, address uint64, count uint64) ([]byte, error) {
	if count == 0 || count > 64 {
		return nil, fmt.Errorf("datagram iovec count %d is out of range", count)
	}
	descriptors := make([]byte, int(count)*16)
	if err := readTargetMemory(pid, address, descriptors); err != nil {
		return nil, err
	}
	payload := make([]byte, 0, 512)
	for index := uint64(0); index < count; index++ {
		base := binary.NativeEndian.Uint64(descriptors[index*16 : index*16+8])
		length := binary.NativeEndian.Uint64(descriptors[index*16+8 : index*16+16])
		if length == 0 {
			continue
		}
		if length > egressDatagramLimit || len(payload)+int(length) > egressDatagramLimit {
			return nil, fmt.Errorf("datagram payload exceeds %d bytes", egressDatagramLimit)
		}
		part, err := readTargetMemoryBytes(pid, base, int(length))
		if err != nil {
			return nil, err
		}
		payload = append(payload, part...)
	}
	return payload, nil
}

// sendDatagram sends payload from fd to a raw sockaddr. A non-blocking socket
// may return EAGAIN before the resolver is writable.
func sendDatagram(fd int, payload, address []byte, timeout time.Duration) unix.Errno {
	if len(address) < 2 {
		return unix.EINVAL
	}
	if len(payload) == 0 {
		return 0
	}
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return unix.ETIMEDOUT
		}
		if err := waitFD(fd, unix.POLLOUT, remaining); err != nil {
			return unix.ETIMEDOUT
		}
		_, _, errno := unix.Syscall6(unix.SYS_SENDTO, uintptr(fd), uintptr(unsafe.Pointer(&payload[0])), uintptr(len(payload)), 0, uintptr(unsafe.Pointer(&address[0])), uintptr(len(address)))
		switch unix.Errno(errno) {
		case 0:
			return 0
		case unix.EAGAIN, unix.EINTR:
			continue
		default:
			return unix.Errno(errno)
		}
	}
}

// rememberDNSSocket records the nameserver a socket was redirected away from.
func (s *egressSupervisor) rememberDNSSocket(fd int, rawDestination []byte) {
	if s == nil || fd < 0 || len(rawDestination) == 0 {
		return
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return
	}
	now := time.Now()
	entry := dnsSocketDestination{
		raw:     append([]byte(nil), rawDestination...),
		expires: now.Add(5 * time.Minute),
	}
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	if len(s.dnsSockets) > 4096 {
		for inode, candidate := range s.dnsSockets {
			if now.After(candidate.expires) || len(s.dnsSockets) > 4096 {
				delete(s.dnsSockets, inode)
			}
		}
	}
	s.dnsSockets[stat.Ino] = entry
}

// dnsSocketSource returns the nameserver address a redirected socket was
// configured with.
func (s *egressSupervisor) dnsSocketSource(fd int) ([]byte, bool) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, false
	}
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	entry, ok := s.dnsSockets[stat.Ino]
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return append([]byte(nil), entry.raw...), true
}

// handleEnforceRecvfrom restores the configured nameserver address on a DNS
// response. A client such as c-ares verifies the response source and would
// otherwise reject every answer because the socket was redirected to the
// loopback stub.
func (s *egressSupervisor) handleEnforceRecvfrom(listenerFD int, request *seccompNotif, sessionID, marker string) {
	pid := int(request.PID)
	targetFD := int(request.Data.Args[0])
	duplicate, err := duplicateTargetFD(pid, targetFD)
	if err != nil {
		replyContinue(listenerFD, request.ID)
		return
	}
	defer func() { _ = unix.Close(duplicate) }()
	original, ok := s.dnsSocketSource(duplicate)
	if !ok {
		replyContinue(listenerFD, request.ID)
		return
	}
	event := EgressAuditEvent{
		SandboxID: s.sandboxID,
		SessionID: sessionID,
		Marker:    marker,
		PID:       pid,
		Binary:    processBinary(pid),
	}
	length := int(request.Data.Args[2])
	if length <= 0 {
		replyContinue(listenerFD, request.ID)
		return
	}
	// The client's buffer can be the full UDP datagram limit; the response is
	// bounded by what the resolver can return.
	if length > egressDatagramLimit {
		length = egressDatagramLimit
	}
	buffer := make([]byte, length)
	read, errno := recvDatagram(duplicate, buffer, s.connectTimeout, isNonblocking(duplicate))
	if errno != 0 {
		event.Decision = "deny"
		event.Reason = fmt.Sprintf("receive from the sandbox DNS resolver failed: %v", errno)
		s.record(event)
		replyConnect(listenerFD, request.ID, errno)
		return
	}
	if err := writeTargetMemory(pid, request.Data.Args[1], buffer[:read]); err != nil {
		event.Decision = "deny"
		event.Reason = "cannot write the DNS response into the workload"
		s.record(event)
		replyConnect(listenerFD, request.ID, unix.EIO)
		return
	}
	sourceAddress := request.Data.Args[4]
	if sourceAddress != 0 {
		if err := writeTargetMemory(pid, sourceAddress, original); err != nil {
			event.Decision = "deny"
			event.Reason = "cannot restore the nameserver address"
			s.record(event)
			replyConnect(listenerFD, request.ID, unix.EIO)
			return
		}
	}
	lengthPointer := request.Data.Args[5]
	if lengthPointer != 0 {
		var encoded [4]byte
		binary.NativeEndian.PutUint32(encoded[:], uint32(len(original)))
		if err := writeTargetMemory(pid, lengthPointer, encoded[:]); err != nil {
			event.Decision = "deny"
			event.Reason = "cannot restore the nameserver address length"
			s.record(event)
			replyConnect(listenerFD, request.ID, unix.EIO)
			return
		}
	}
	event.Decision = "allow"
	event.Reason = "dns-stub-recvfrom"
	s.record(event)
	_ = ioctlNotifSend(listenerFD, &seccompNotifResp{ID: request.ID, Val: int64(read)})
}

func recvDatagram(fd int, buffer []byte, timeout time.Duration, nonblocking bool) (int, unix.Errno) {
	if nonblocking {
		// A non-blocking client polls before receiving; blocking here would
		// delay its event loop instead of letting it observe EAGAIN.
		read, _, err := unix.Recvfrom(fd, buffer, 0)
		if read >= 0 {
			return read, 0
		}
		return 0, errnoFromError(err)
	}
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, unix.EAGAIN
		}
		if err := waitFD(fd, unix.POLLIN, remaining); err != nil {
			return 0, unix.EAGAIN
		}
		read, _, err := unix.Recvfrom(fd, buffer, 0)
		if read >= 0 {
			return read, 0
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
			continue
		}
		return 0, errnoFromError(err)
	}
}

func isNonblocking(fd int) bool {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	return err == nil && flags&unix.O_NONBLOCK != 0
}

func errnoFromError(err error) unix.Errno {
	var errno unix.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return unix.EIO
}

func writeTargetMemory(pid int, address uint64, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	local := []unix.Iovec{{Base: &data[0], Len: uint64(len(data))}}
	remote := []unix.RemoteIovec{{Base: uintptr(address), Len: len(data)}}
	written, err := unix.ProcessVMWritev(pid, local, remote, 0)
	if err != nil {
		return fmt.Errorf("write target memory: %w", err)
	}
	if written != len(data) {
		return fmt.Errorf("write target memory: wrote %d of %d bytes", written, len(data))
	}
	return nil
}

// handleEnforceGetpeername restores the configured nameserver on a redirected
// DNS socket. Clients that compare the peer address to their configured server
// (c-ares does) would otherwise abandon every redirected query.
func (s *egressSupervisor) handleEnforceGetpeername(listenerFD int, request *seccompNotif) {
	pid := int(request.PID)
	targetFD := int(request.Data.Args[0])
	duplicate, err := duplicateTargetFD(pid, targetFD)
	if err != nil {
		replyContinue(listenerFD, request.ID)
		return
	}
	defer func() { _ = unix.Close(duplicate) }()
	original, ok := s.dnsSocketSource(duplicate)
	if !ok {
		replyContinue(listenerFD, request.ID)
		return
	}
	addressPointer := request.Data.Args[1]
	lengthPointer := request.Data.Args[2]
	if addressPointer == 0 || lengthPointer == 0 {
		replyContinue(listenerFD, request.ID)
		return
	}
	var encodedLength [4]byte
	if err := readTargetMemory(pid, lengthPointer, encodedLength[:]); err != nil {
		replyContinue(listenerFD, request.ID)
		return
	}
	available := int(binary.NativeEndian.Uint32(encodedLength[:]))
	written := len(original)
	if available < written {
		written = available
	}
	if written > 0 {
		if err := writeTargetMemory(pid, addressPointer, original[:written]); err != nil {
			replyContinue(listenerFD, request.ID)
			return
		}
	}
	binary.NativeEndian.PutUint32(encodedLength[:], uint32(len(original)))
	if err := writeTargetMemory(pid, lengthPointer, encodedLength[:]); err != nil {
		replyContinue(listenerFD, request.ID)
		return
	}
	_ = ioctlNotifSend(listenerFD, &seccompNotifResp{ID: request.ID})
}
