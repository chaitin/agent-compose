//go:build linux

package sandboxinit

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// egressSupervisor receives the seccomp user-notification listener of one exec
// session and reports the connections it observes. Audit mode never changes the
// syscall outcome; the decision stays in the daemon.
type egressSupervisor struct {
	mode           EgressMode
	sandboxID      string
	binary         string
	sink           EgressAuditSink
	logger         *slog.Logger
	proxy          *egressProxyTarget
	dns            *egressDNS
	bypassIPs      map[string]struct{}
	localIPs       map[string]struct{}
	dnsMu          sync.Mutex
	dnsSockets     map[uint64]dnsSocketDestination
	connectTimeout time.Duration
}

// newEgressSupervisor resolves the init binary the trampoline re-executes. A
// supervisor that cannot find it cannot start a filtered child, so the failure
// is returned at init startup rather than at the first exec.
func newEgressSupervisor(mode EgressMode, sandboxID string, sink EgressAuditSink) (*egressSupervisor, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox init binary for egress audit: %w", err)
	}
	supervisor := &egressSupervisor{
		mode:           mode,
		sandboxID:      sandboxID,
		binary:         binary,
		sink:           sink,
		logger:         slog.Default(),
		connectTimeout: egressConnectTimeout,
		dnsSockets:     make(map[uint64]dnsSocketDestination),
	}
	if mode == EgressModeEnforce {
		proxy, err := loadEgressProxyTarget()
		if err != nil {
			return nil, err
		}
		supervisor.proxy = proxy
		supervisor.localIPs, supervisor.bypassIPs = buildAddressSets(proxy)
		dns, dnsErr := startEgressDNS(sandboxID, egressDNSPassthrough(proxy))
		if dnsErr != nil {
			// Mediation still works without the DNS stub; port 53 then keeps
			// its non-mediated path and hostname policy is weaker. Report it
			// rather than failing egress entirely.
			slog.Warn("sandbox init policy DNS is unavailable; DNS stays on its previous path", "error", dnsErr)
		} else {
			supervisor.dns = dns
		}
	}
	return supervisor, nil
}

func (s *egressSupervisor) enabled() bool {
	return s != nil && (s.mode == EgressModeAudit || s.mode == EgressModeEnforce)
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
		s.handleNotification(listenerFD, &request, sessionID, marker)
	}
}

func (s *egressSupervisor) handleNotification(listenerFD int, request *seccompNotif, sessionID, marker string) {
	if s.mode == EgressModeEnforce {
		switch int32(request.Data.NR) {
		case int32(unix.SYS_CONNECT):
			s.handleEnforce(listenerFD, request, sessionID, marker)
		case int32(unix.SYS_SENDTO), int32(unix.SYS_SENDMSG):
			s.handleEnforceDatagram(listenerFD, request, sessionID, marker)
		case int32(unix.SYS_RECVFROM):
			s.handleEnforceRecvfrom(listenerFD, request, sessionID, marker)
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
		"family", event.Family,
		"host", event.Host,
		"port", event.Port,
		"unix_path", event.UnixPath,
		"decision", event.Decision,
		"reason", event.Reason,
	)
}
