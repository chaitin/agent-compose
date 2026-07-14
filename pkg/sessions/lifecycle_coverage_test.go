package sessions

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestLifecycleReconcileRuntimeStateMicrosandboxLost(t *testing.T) {
	session := lifecycleTestSession("session-lost", driverpkg.RuntimeDriverMicrosandbox, domain.VMStatusRunning)
	store := &fakeLifecycleStore{
		session:    session,
		vmState:    domain.VMState{BoxID: "box-1", LastError: "old error"},
		proxyState: domain.ProxyState{Enabled: true, HostPort: unusedTCPPort(t), GuestPort: 8888},
	}
	notifier := &fakeLifecycleNotifier{}
	revoker := &fakeFacadeTokenRevoker{}
	lifecycle := Lifecycle{
		Config:       &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
		Store:        store,
		Liveness:     fakeRuntimeLiveness{alive: false, ok: true},
		TokenRevoker: revoker,
		Notifier:     notifier,
	}

	loaded, err := lifecycle.ReconcileRuntimeState(context.Background(), session)
	if err != nil {
		t.Fatalf("ReconcileRuntimeState returned error: %v", err)
	}
	if loaded.Summary.VMStatus != domain.VMStatusStopped || store.savedVM.BoxID != "" || !domain.TimeIsSet(store.savedVM.StoppedAt) {
		t.Fatalf("loaded=%#v savedVM=%#v", loaded, store.savedVM)
	}
	if store.updated != 1 || store.events != 1 || revoker.revoked != "session-lost" {
		t.Fatalf("updated/events/revoked = %d/%d/%q", store.updated, store.events, revoker.revoked)
	}
	if notifier.updated != 1 || notifier.dashboard != "sandbox_updated" || notifier.events != 1 {
		t.Fatalf("notifier = %#v", notifier)
	}
}

func TestLifecycleReconcileRuntimeStateDockerLost(t *testing.T) {
	session := lifecycleTestSession("session-docker-lost", driverpkg.RuntimeDriverDocker, domain.VMStatusRunning)
	store := &fakeLifecycleStore{session: session, vmState: domain.VMState{BoxID: "container-1"}}
	lifecycle := Lifecycle{
		Config:   &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
		Store:    store,
		Liveness: fakeRuntimeLiveness{alive: false, ok: true},
	}

	loaded, err := lifecycle.ReconcileRuntimeState(context.Background(), session)
	if err != nil {
		t.Fatalf("ReconcileRuntimeState returned error: %v", err)
	}
	if loaded.Summary.VMStatus != domain.VMStatusStopped || store.savedVM.BoxID != "" || !domain.TimeIsSet(store.savedVM.StoppedAt) {
		t.Fatalf("loaded=%#v savedVM=%#v", loaded, store.savedVM)
	}
}

func TestLifecycleReconcileRuntimeStateEarlyReturns(t *testing.T) {
	lifecycle := Lifecycle{Config: &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker}, Store: &fakeLifecycleStore{}}
	if got, err := lifecycle.ReconcileRuntimeState(context.Background(), nil); err != nil || got != nil {
		t.Fatalf("nil session = %#v/%v", got, err)
	}
	dockerSession := lifecycleTestSession("session-docker", driverpkg.RuntimeDriverDocker, domain.VMStatusRunning)
	if got, err := lifecycle.ReconcileRuntimeState(context.Background(), dockerSession); err != nil || got != dockerSession {
		t.Fatalf("docker session = %#v/%v", got, err)
	}
	stoppedSession := lifecycleTestSession("session-stopped", driverpkg.RuntimeDriverMicrosandbox, domain.VMStatusStopped)
	if got, err := lifecycle.ReconcileRuntimeState(context.Background(), stoppedSession); err != nil || got != stoppedSession {
		t.Fatalf("stopped session = %#v/%v", got, err)
	}
}

func TestLifecycleEnsureProxyReadyBranches(t *testing.T) {
	t.Run("proxy disabled", func(t *testing.T) {
		session := lifecycleTestSession("session-disabled", driverpkg.RuntimeDriverDocker, domain.VMStatusStopped)
		lifecycle := Lifecycle{
			Config: &appconfig.Config{SandboxStartTimeout: time.Second},
			Store:  &fakeLifecycleStore{session: session, proxyState: domain.ProxyState{}},
		}
		_, _, err := lifecycle.EnsureProxyReady(context.Background(), session.Summary.ID)
		if err == nil || !stringsContains(err.Error(), "jupyter is not enabled") {
			t.Fatalf("disabled proxy error = %v", err)
		}
	})

	t.Run("start failure marks failed", func(t *testing.T) {
		session := lifecycleTestSession("session-start-fail", driverpkg.RuntimeDriverDocker, domain.VMStatusStopped)
		store := &fakeLifecycleStore{session: session, proxyState: domain.ProxyState{Enabled: true, HostPort: unusedTCPPort(t), GuestPort: 8888}}
		lifecycle := Lifecycle{
			Config: &appconfig.Config{SandboxStartTimeout: time.Second},
			Store:  store,
			Driver: fakeSandboxDriver{startErr: errors.New("start failed")},
		}
		_, _, err := lifecycle.EnsureProxyReady(context.Background(), session.Summary.ID)
		if err == nil || !stringsContains(err.Error(), "start failed") || store.session.Summary.VMStatus != domain.VMStatusFailed {
			t.Fatalf("start failure err/session = %v/%#v", err, store.session)
		}
	})

	t.Run("start success reloads session and proxy", func(t *testing.T) {
		session := lifecycleTestSession("session-start", driverpkg.RuntimeDriverDocker, domain.VMStatusStopped)
		proxyState := domain.ProxyState{Enabled: true, HostPort: unusedTCPPort(t), GuestPort: 8888, ProxyPath: "/lab"}
		store := &fakeLifecycleStore{session: session, proxyState: proxyState}
		driver := &recordingSandboxDriver{}
		lifecycle := Lifecycle{
			Config: &appconfig.Config{SandboxStartTimeout: time.Second},
			Store:  store,
			Driver: driver,
		}
		loaded, loadedProxy, err := lifecycle.EnsureProxyReady(context.Background(), session.Summary.ID)
		if err != nil {
			t.Fatalf("EnsureProxyReady returned error: %v", err)
		}
		if !driver.started || loaded.Summary.VMStatus != domain.VMStatusRunning || loadedProxy.ProxyPath != "/lab" {
			t.Fatalf("driver/loaded/proxy = %v/%#v/%#v", driver.started, loaded, loadedProxy)
		}
	})
}

func TestJupyterTargetReachableCoverage(t *testing.T) {
	if JupyterTargetReachable(domain.ProxyState{}, 10*time.Millisecond) {
		t.Fatalf("empty proxy state should not be reachable")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
		close(accepted)
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	if !JupyterTargetReachable(domain.ProxyState{Enabled: true, HostPort: port, GuestPort: 8888}, time.Second) {
		t.Fatalf("listening proxy target should be reachable")
	}
	<-accepted
}

func lifecycleTestSession(id, driver, status string) *domain.Sandbox {
	return &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            id,
			Driver:        driver,
			VMStatus:      status,
			WorkspacePath: "",
		},
	}
}

type fakeLifecycleStore struct {
	session    *domain.Sandbox
	vmState    domain.VMState
	savedVM    domain.VMState
	proxyState domain.ProxyState
	updated    int
	events     int
}

func (s *fakeLifecycleStore) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	return s.session, nil
}

func (s *fakeLifecycleStore) UpdateSandbox(_ context.Context, session *domain.Sandbox) error {
	s.updated++
	s.session = session
	return nil
}

func (s *fakeLifecycleStore) GetVMState(string) (domain.VMState, error) {
	return s.vmState, nil
}

func (s *fakeLifecycleStore) SaveVMState(_ string, state domain.VMState) error {
	s.savedVM = state
	return nil
}

func (s *fakeLifecycleStore) GetProxyState(string) (domain.ProxyState, error) {
	return s.proxyState, nil
}

func (s *fakeLifecycleStore) AddEvent(context.Context, string, domain.SandboxEvent) error {
	s.events++
	return nil
}

type fakeRuntimeLiveness struct {
	alive bool
	ok    bool
	err   error
}

func (l fakeRuntimeLiveness) IsSandboxAlive(context.Context, string, *domain.Sandbox, domain.VMState) (bool, bool, error) {
	return l.alive, l.ok, l.err
}

type fakeFacadeTokenRevoker struct {
	revoked string
}

func (r *fakeFacadeTokenRevoker) RevokeLLMFacadeTokensForSandbox(_ context.Context, sessionID string) error {
	r.revoked = sessionID
	return nil
}

type fakeLifecycleNotifier struct {
	updated   int
	events    int
	dashboard string
}

func (n *fakeLifecycleNotifier) PublishSandboxUpdated(*domain.SandboxSummary) {
	n.updated++
}

func (n *fakeLifecycleNotifier) PublishEventAdded(string, domain.SandboxEvent) {
	n.events++
}

func (n *fakeLifecycleNotifier) NotifyDashboard(event string) {
	n.dashboard = event
}

type fakeSandboxDriver struct {
	startErr error
	stopErr  error
}

func (d fakeSandboxDriver) StartSandboxVM(context.Context, *domain.Sandbox) error {
	return d.startErr
}

func (d fakeSandboxDriver) StopSandboxVM(context.Context, *domain.Sandbox) error {
	return d.stopErr
}

type recordingSandboxDriver struct {
	started bool
}

func (d *recordingSandboxDriver) StartSandboxVM(context.Context, *domain.Sandbox) error {
	d.started = true
	return nil
}

func (d *recordingSandboxDriver) StopSandboxVM(context.Context, *domain.Sandbox) error {
	return nil
}

func unusedTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for unused port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close unused port listener: %v", err)
	}
	return port
}

func stringsContains(value, want string) bool {
	return strings.Contains(value, want)
}
