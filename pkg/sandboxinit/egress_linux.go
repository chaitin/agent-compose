//go:build linux

package sandboxinit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chaitin/agent-compose/pkg/sandboxsupervisor"
	"golang.org/x/sys/unix"
)

// egressSupervisor receives the seccomp user-notification listener of one exec
// session and reports the connections it observes. Audit mode never changes the
// syscall outcome; the decision stays in the daemon.
type egressSupervisor struct {
	mode           EgressMode
	sandboxID      string
	binary         string
	generation     uint64
	sink           EgressAuditSink
	logger         *slog.Logger
	proxy          *egressProxyTarget
	relay          *egressRelayTarget
	dns            *egressDNS
	bypassIPs      map[string]struct{}
	localIPs       map[string]struct{}
	proxyHost      string
	proxyPort      int
	dnsMu          sync.Mutex
	dnsSockets     map[uint64]dnsSocketDestination
	connectTimeout time.Duration

	// relayMu guards the token the daemon delivers over the init control
	// socket. relayReady is closed once the token is installed, which is what
	// lets Run block the workload until supervisor mode is usable.
	relayMu       sync.Mutex
	relayReady    chan struct{}
	relayReadyOne sync.Once
}

// newEgressSupervisor resolves the init binary the trampoline re-executes. A
// supervisor that cannot find it cannot start a filtered child, so the failure
// is returned at init startup rather than at the first exec.
func newEgressSupervisor(mode EgressMode, sandboxID string, sink EgressAuditSink, generation uint64) (*egressSupervisor, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox init binary for egress audit: %w", err)
	}
	supervisor := &egressSupervisor{
		mode:           mode,
		sandboxID:      sandboxID,
		binary:         binary,
		generation:     generation,
		sink:           sink,
		logger:         slog.Default(),
		connectTimeout: egressConnectTimeout,
		dnsSockets:     make(map[uint64]dnsSocketDestination),
		relayReady:     make(chan struct{}),
	}
	if mode == EgressModeEnforce {
		if egressRelayConfigured() {
			relay, passthrough, err := loadEgressRelayTarget(sandboxID)
			if err != nil {
				return nil, err
			}
			supervisor.relay = relay
			supervisor.localIPs, supervisor.bypassIPs = buildAddressSets(nil)
			supervisor.startPolicyDNS(sandboxID, passthrough)
			return supervisor, nil
		}
		proxy, err := loadEgressProxyTarget()
		if err != nil {
			return nil, err
		}
		supervisor.proxy = proxy
		supervisor.localIPs, supervisor.bypassIPs = buildAddressSets(proxy)
		if address, ok := tcpAddrFromSockaddr(proxy.rawAddr); ok {
			supervisor.proxyHost = address.IP.String()
			supervisor.proxyPort = address.Port
		}
		supervisor.startPolicyDNS(sandboxID, egressDNSPassthrough(proxy))
	}
	return supervisor, nil
}

// startPolicyDNS starts the policy resolver and records a warning when the
// kernel or the guest image cannot provide it. Mediation still works without
// the stub, but hostname policy is weaker, so the degradation is reported
// instead of failing all egress.
func (s *egressSupervisor) startPolicyDNS(sandboxID string, passthrough map[string][]net.IP) {
	dns, err := startEgressDNS(sandboxID, passthrough)
	if err != nil {
		slog.Warn("sandbox init policy DNS is unavailable; DNS stays on its previous path", "error", err)
		return
	}
	s.dns = dns
}

// egressRelayConfigured reports whether the daemon selected the independent
// supervisor mode for this sandbox. The token is not part of this test: it
// arrives later over the authenticated init control socket.
func egressRelayConfigured() bool {
	return strings.TrimSpace(os.Getenv(EgressRelaySocketEnvVar)) != ""
}

func (s *egressSupervisor) enabled() bool {
	return s != nil && (s.mode == EgressModeAudit || s.mode == EgressModeEnforce)
}

// configureRelay installs the per-sandbox supervisor token delivered over the
// authenticated init control socket. Repeating the same token is harmless; a
// second, different token is rejected so a local process that can reach the
// control socket cannot replace the supervisor path after setup.
func (s *egressSupervisor) configureRelay(token string) error {
	if s == nil || s.relay == nil {
		return errors.New("sandbox supervisor relay is not enabled for this init")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("sandbox supervisor relay token is required")
	}
	// Prove the token belongs to the supervisor the daemon started before
	// trusting it. The init control socket is reachable by workload processes,
	// so accepting the first token blindly would let a local process replace
	// the relay path. Only the daemon knows a token this ping accepts.
	if err := s.verifyRelayToken(token); err != nil {
		return err
	}
	s.relayMu.Lock()
	current := s.relay.token
	if current != "" && current != token {
		s.relayMu.Unlock()
		return errors.New("sandbox supervisor relay is already configured")
	}
	s.relay.token = token
	s.relayMu.Unlock()
	s.relayReadyOne.Do(func() { close(s.relayReady) })
	return nil
}

// verifyRelayToken checks the delivered token against the supervisor before it
// becomes the token this init process presents.
func (s *egressSupervisor) verifyRelayToken(token string) error {
	if s.relay == nil || strings.TrimSpace(s.relay.socketPath) == "" {
		return errors.New("sandbox supervisor relay socket is not configured")
	}
	client := &sandboxsupervisor.Client{
		SocketPath: s.relay.socketPath,
		Token:      token,
		SandboxID:  s.relay.sandboxID,
		Timeout:    s.connectTimeout,
	}
	if err := client.Ping(context.Background()); err != nil {
		return fmt.Errorf("sandbox supervisor rejected the relay token: %w", err)
	}
	return nil
}

// relayToken returns the configured relay token, or an empty string before the
// daemon has delivered it.
func (s *egressSupervisor) relayToken() string {
	if s == nil {
		return ""
	}
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	return s.relay.token
}

// waitForRelay blocks until the daemon delivers the supervisor token. The
// workload must not start before it: the main process is the first egress
// consumer and would otherwise have no usable mediation path.
func (s *egressSupervisor) waitForRelay(ctx context.Context, timeout time.Duration) error {
	if s == nil || s.relay == nil {
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.relayReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("sandbox supervisor relay was not configured within %s", timeout)
	}
}

// close releases the DNS listeners. Mediation itself has no long-lived
// resources beyond the per-session listeners.
func (s *egressSupervisor) close() {
	if s == nil {
		return
	}
	s.dns.close()
}

// adopt waits for the trampoline of one exec session to hand over its
// notification listener and then serves it until the session exits.
func (s *egressSupervisor) adopt(sock *os.File, sessionID, marker string, done <-chan struct{}) {
	if sock == nil {
		return
	}
	go func() {
		defer func() { _ = sock.Close() }()
		listenerFD, err := receiveListener(sock)
		if err != nil {
			if !errors.Is(err, os.ErrClosed) {
				s.logger.Debug("sandbox init egress listener was not received",
					"sandbox_id", s.sandboxID, "session_id", sessionID, "error", err)
			}
			return
		}
		var closeOnce sync.Once
		closeListener := func() { closeOnce.Do(func() { _ = unix.Close(listenerFD) }) }
		defer closeListener()
		go func() {
			<-done
			// A detached run's supervisor outlives the start session that
			// installed the filter. Closing the listener with the leader would
			// make every later connect from that tree fail with ENOSYS, so keep
			// serving while any process still carries this session's marker.
			for marker != "" && markedProcessesExist(marker) {
				time.Sleep(time.Second)
			}
			closeListener()
		}()
		s.serve(listenerFD, sessionID, marker)
	}()
}

// serve reads notifications until the listener is closed by session exit.
func (s *egressSupervisor) serve(listenerFD int, sessionID, marker string) {
	var receives *dnsRecvmsgQueue
	if s.mode == EgressModeEnforce {
		receives = newDNSRecvmsgQueue(listenerFD)
		defer receives.close()
	}
	if _, err := notificationSizes(); err != nil {
		s.logger.Warn("sandbox init egress audit disabled after a notification size check failed",
			"sandbox_id", s.sandboxID, "session_id", sessionID, "error", err)
		return
	}
	for {
		var request seccompNotif
		if err := ioctlNotifRecv(listenerFD, &request); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return
		}
		s.handleNotification(listenerFD, &request, sessionID, marker, receives)
	}
}

func (s *egressSupervisor) handleNotification(listenerFD int, request *seccompNotif, sessionID, marker string, receives *dnsRecvmsgQueue) {
	if s.mode == EgressModeEnforce {
		switch int32(request.Data.NR) {
		case int32(unix.SYS_CONNECT):
			s.handleEnforce(listenerFD, request, sessionID, marker)
		case int32(unix.SYS_SENDTO), int32(unix.SYS_SENDMSG):
			s.handleEnforceDatagram(listenerFD, request, sessionID, marker)
		case int32(unix.SYS_RECVFROM):
			s.handleEnforceRecvfrom(listenerFD, request, sessionID, marker)
		case int32(unix.SYS_RECVMSG):
			if receive := s.prepareDNSRecvmsg(listenerFD, request); receive != nil {
				receives.add <- receive
			}
		case int32(unix.SYS_GETPEERNAME):
			s.handleEnforceGetpeername(listenerFD, request)
		default:
			_ = ioctlNotifSend(listenerFD, &seccompNotifResp{ID: request.ID, Flags: seccompUserNotifContinue})
		}
		return
	}
	if request.Data.NR != int32(unix.SYS_CONNECT) {
		_ = ioctlNotifSend(listenerFD, &seccompNotifResp{
			ID:    request.ID,
			Flags: seccompUserNotifContinue,
		})
		return
	}
	event := EgressAuditEvent{
		SandboxID: s.sandboxID,
		SessionID: sessionID,
		Marker:    marker,
		PID:       int(request.PID),
		Binary:    processBinary(int(request.PID)),
	}
	if notificationIDValid(listenerFD, request.ID) {
		destination, err := connectDestination(int(request.PID), request.Data.Args[1], uint32(request.Data.Args[2]))
		if err != nil {
			event.Family = "unknown"
			s.logger.Debug("sandbox init egress audit could not decode a connect destination",
				"sandbox_id", s.sandboxID, "session_id", sessionID, "pid", request.PID, "error", err)
		} else {
			destination.SandboxID = event.SandboxID
			destination.SessionID = event.SessionID
			destination.Marker = event.Marker
			destination.PID = event.PID
			destination.Binary = event.Binary
			event = destination
		}
	}
	s.record(event)
	if err := ioctlNotifSend(listenerFD, &seccompNotifResp{
		ID:    request.ID,
		Flags: seccompUserNotifContinue,
	}); err != nil && !errors.Is(err, unix.ENOENT) {
		s.logger.Debug("sandbox init egress audit could not continue a connect",
			"sandbox_id", s.sandboxID, "session_id", sessionID, "pid", request.PID, "error", err)
	}
}

func (s *egressSupervisor) record(event EgressAuditEvent) {
	if event.Generation == 0 {
		event.Generation = s.generation
	}
	if s.sink != nil {
		s.sink(event)
		return
	}
	message := "sandbox init egress audit"
	if s.mode == EgressModeEnforce {
		message = "sandbox init egress decision"
	}
	s.logger.Info(message,
		"sandbox_id", event.SandboxID,
		"session_id", event.SessionID,
		"marker", event.Marker,
		"pid", event.PID,
		"binary", event.Binary,
		"family", event.Family,
		"host", event.Host,
		"port", event.Port,
		"unix_path", event.UnixPath,
		"decision", event.Decision,
		"reason", event.Reason,
	)
}
