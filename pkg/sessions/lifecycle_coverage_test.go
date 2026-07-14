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
	t.Run("running reachable fast path skips workspace ensure", func(t *testing.T) {
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

		session := lifecycleTestSession("session-running", driverpkg.RuntimeDriverDocker, domain.VMStatusRunning)
		proxyState := domain.ProxyState{Enabled: true, HostPort: listener.Addr().(*net.TCPAddr).Port, GuestPort: 8888}
		ensurer := &recordingWorkspaceEnsurer{err: errors.New("must not ensure")}
		driver := &recordingSandboxDriver{startErr: errors.New("must not start")}
		lifecycle := Lifecycle{
			Config:           &appconfig.Config{SandboxStartTimeout: time.Second},
			Store:            &fakeLifecycleStore{session: session, proxyState: proxyState},
			WorkspaceEnsurer: ensurer,
			Driver:           driver,
		}

		loaded, loadedProxy, err := lifecycle.EnsureProxyReady(context.Background(), session.Summary.ID)
		if err != nil {
			t.Fatalf("EnsureProxyReady returned error: %v", err)
		}
		<-accepted
		if loaded != session || loadedProxy != proxyState {
			t.Fatalf("loaded/proxy = %p/%#v, want %p/%#v", loaded, loadedProxy, session, proxyState)
		}
		if ensurer.calls != 0 || driver.calls != 0 {
			t.Fatalf("ensure/start calls = %d/%d, want 0/0", ensurer.calls, driver.calls)
		}
	})

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
			Config:           &appconfig.Config{SandboxStartTimeout: time.Second},
			Store:            store,
			WorkspaceEnsurer: &recordingWorkspaceEnsurer{},
			Driver:           fakeSandboxDriver{startErr: errors.New("start failed")},
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
			Config:           &appconfig.Config{SandboxStartTimeout: time.Second},
			Store:            store,
			WorkspaceEnsurer: &recordingWorkspaceEnsurer{},
			Driver:           driver,
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

func TestLifecycleEnsureProxyReadyWorkspaceEnsurerFailureMarksFailedBeforeDriver(t *testing.T) {
	session := lifecycleTestSession("session-workspace-fail", driverpkg.RuntimeDriverDocker, domain.VMStatusStopped)
	store := &fakeLifecycleStore{
		session:    session,
		proxyState: domain.ProxyState{Enabled: true, HostPort: unusedTCPPort(t), GuestPort: 8888},
	}
	ensureErr := errors.New("workspace provisioning failed")
	ensurer := &recordingWorkspaceEnsurer{err: ensureErr}
	driver := &recordingSandboxDriver{}
	lifecycle := Lifecycle{
		Config:           &appconfig.Config{SandboxStartTimeout: time.Second},
		Store:            store,
		WorkspaceEnsurer: ensurer,
		Driver:           driver,
	}

	_, _, err := lifecycle.EnsureProxyReady(context.Background(), session.Summary.ID)
	if !errors.Is(err, ensureErr) {
		t.Fatalf("EnsureProxyReady error = %v, want %v", err, ensureErr)
	}
	if ensurer.calls != 1 {
		t.Fatalf("workspace Ensure calls = %d, want 1", ensurer.calls)
	}
	if driver.calls != 0 {
		t.Fatalf("driver start calls = %d, want 0", driver.calls)
	}
	if store.updated != 1 || session.Summary.VMStatus != domain.VMStatusFailed {
		t.Fatalf("store updates/status = %d/%q, want 1/%q", store.updated, session.Summary.VMStatus, domain.VMStatusFailed)
	}
}

func TestLifecycleMissingWorkspaceEnsurerReturnsError(t *testing.T) {
	t.Run("ensure proxy ready", func(t *testing.T) {
		session := lifecycleTestSession("session-missing-ensurer", driverpkg.RuntimeDriverDocker, domain.VMStatusStopped)
		store := &fakeLifecycleStore{
			session:    session,
			proxyState: domain.ProxyState{Enabled: true, HostPort: unusedTCPPort(t), GuestPort: 8888},
		}
		lifecycle := Lifecycle{
			Config: &appconfig.Config{SandboxStartTimeout: time.Second},
			Store:  store,
		}

		_, _, err := lifecycle.EnsureProxyReady(context.Background(), session.Summary.ID)
		if err == nil || !stringsContains(err.Error(), "workspace ensurer is not configured") {
			t.Fatalf("EnsureProxyReady error = %v, want missing workspace ensurer error", err)
		}
		if store.updated != 1 || session.Summary.VMStatus != domain.VMStatusFailed {
			t.Fatalf("store updates/status = %d/%q, want 1/%q", store.updated, session.Summary.VMStatus, domain.VMStatusFailed)
		}
	})

	t.Run("resume loaded", func(t *testing.T) {
		session := lifecycleTestSession("session-resume-missing-ensurer", driverpkg.RuntimeDriverDocker, domain.VMStatusStopped)
		lifecycle := Lifecycle{}

		_, err := lifecycle.ResumeLoaded(context.Background(), session, nil)
		if err == nil || !stringsContains(err.Error(), "workspace ensurer is not configured") {
			t.Fatalf("ResumeLoaded error = %v, want missing workspace ensurer error", err)
		}
	})
}

func TestLifecycleEnsureProxyReadyRuntimeFailurePreservesReadyProvisioning(t *testing.T) {
	session := lifecycleTestSession("session-runtime-fail", driverpkg.RuntimeDriverDocker, domain.VMStatusStopped)
	session.Workspace = &domain.SandboxWorkspace{ID: "workspace-1"}
	session.WorkspaceProvisioning = &domain.SandboxWorkspaceProvisioning{
		Version: domain.SandboxWorkspaceProvisioningVersion,
		Status:  domain.SandboxWorkspaceProvisioningStatusPending,
	}
	store := &fakeLifecycleStore{
		session:    session,
		proxyState: domain.ProxyState{Enabled: true, HostPort: unusedTCPPort(t), GuestPort: 8888},
	}
	order := []string{}
	ensurer := &recordingWorkspaceEnsurer{
		order: &order,
		onEnsure: func(session *domain.Sandbox) {
			session.WorkspaceProvisioning.Status = domain.SandboxWorkspaceProvisioningStatusReady
		},
	}
	startErr := errors.New("runtime start failed")
	driver := &recordingSandboxDriver{order: &order, startErr: startErr}
	lifecycle := Lifecycle{
		Config:           &appconfig.Config{SandboxStartTimeout: time.Second},
		Store:            store,
		WorkspaceEnsurer: ensurer,
		Driver:           driver,
	}

	_, _, err := lifecycle.EnsureProxyReady(context.Background(), session.Summary.ID)
	if !errors.Is(err, startErr) {
		t.Fatalf("EnsureProxyReady error = %v, want %v", err, startErr)
	}
	if ensurer.calls != 1 || driver.calls != 1 {
		t.Fatalf("ensure/start calls = %d/%d, want 1/1", ensurer.calls, driver.calls)
	}
	if got := strings.Join(order, ","); got != "ensure,driver.start" {
		t.Fatalf("call order = %q, want %q", got, "ensure,driver.start")
	}
	if session.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("workspace provisioning status = %q, want ready", session.WorkspaceProvisioning.Status)
	}
	if session.Summary.VMStatus != domain.VMStatusFailed {
		t.Fatalf("VM status = %q, want failed", session.Summary.VMStatus)
	}
}

func TestLifecycleResumeLoadedWorkspaceEnsurerOrdering(t *testing.T) {
	session := lifecycleTestSession("session-resume", driverpkg.RuntimeDriverDocker, domain.VMStatusStopped)
	order := []string{}
	store := &fakeLifecycleStore{
		session: session,
		onUpdate: func() {
			order = append(order, "store.update")
		},
		onEvent: func() {
			order = append(order, "event.persist")
		},
	}
	ensurer := &recordingWorkspaceEnsurer{order: &order}
	driver := &recordingSandboxDriver{order: &order}
	notifier := &fakeLifecycleNotifier{order: &order}
	lifecycle := Lifecycle{
		Store:            store,
		WorkspaceEnsurer: ensurer,
		Driver:           driver,
		Notifier:         notifier,
		GuideWriter: func(context.Context, *domain.Sandbox, []string) {
			order = append(order, "guide")
		},
	}

	loaded, err := lifecycle.ResumeLoaded(context.Background(), session, []string{"capset-1"})
	if err != nil {
		t.Fatalf("ResumeLoaded returned error: %v", err)
	}
	if loaded != session {
		t.Fatalf("loaded session = %p, want %p", loaded, session)
	}
	if ensurer.calls != 1 || driver.calls != 1 {
		t.Fatalf("ensure/start calls = %d/%d, want 1/1", ensurer.calls, driver.calls)
	}
	wantOrder := "ensure,guide,driver.start,store.update,notify.updated,notify.dashboard,event.persist,notify.event"
	if got := strings.Join(order, ","); got != wantOrder {
		t.Fatalf("call order = %q, want %q", got, wantOrder)
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
		t.Fatalf("VM status = %q, want running", session.Summary.VMStatus)
	}
}

func TestLifecycleResumeLoadedWorkspaceEnsurerFailureStopsBeforeGuideAndDriver(t *testing.T) {
	session := lifecycleTestSession("session-resume-workspace-fail", driverpkg.RuntimeDriverDocker, domain.VMStatusFailed)
	ensureErr := errors.New("workspace retry failed")
	ensurer := &recordingWorkspaceEnsurer{err: ensureErr}
	driver := &recordingSandboxDriver{}
	guideCalls := 0
	store := &fakeLifecycleStore{session: session}
	lifecycle := Lifecycle{
		Store:            store,
		WorkspaceEnsurer: ensurer,
		Driver:           driver,
		GuideWriter: func(context.Context, *domain.Sandbox, []string) {
			guideCalls++
		},
	}

	_, err := lifecycle.ResumeLoaded(context.Background(), session, nil)
	if !errors.Is(err, ensureErr) {
		t.Fatalf("ResumeLoaded error = %v, want %v", err, ensureErr)
	}
	if ensurer.calls != 1 || driver.calls != 0 || guideCalls != 0 {
		t.Fatalf("ensure/start/guide calls = %d/%d/%d, want 1/0/0", ensurer.calls, driver.calls, guideCalls)
	}
	if store.updated != 0 || session.Summary.VMStatus != domain.VMStatusFailed {
		t.Fatalf("store updates/status = %d/%q, want 0/%q", store.updated, session.Summary.VMStatus, domain.VMStatusFailed)
	}
}

func TestLifecycleResumeLoadedRuntimeFailurePreservesReadyProvisioning(t *testing.T) {
	session := lifecycleTestSession("session-resume-runtime-fail", driverpkg.RuntimeDriverDocker, domain.VMStatusStopped)
	session.Workspace = &domain.SandboxWorkspace{ID: "workspace-1"}
	session.WorkspaceProvisioning = &domain.SandboxWorkspaceProvisioning{
		Version: domain.SandboxWorkspaceProvisioningVersion,
		Status:  domain.SandboxWorkspaceProvisioningStatusPending,
	}
	ensurer := &recordingWorkspaceEnsurer{onEnsure: func(session *domain.Sandbox) {
		session.WorkspaceProvisioning.Status = domain.SandboxWorkspaceProvisioningStatusReady
	}}
	startErr := errors.New("runtime start failed")
	driver := &recordingSandboxDriver{startErr: startErr}
	store := &fakeLifecycleStore{session: session}
	lifecycle := Lifecycle{
		Store:            store,
		WorkspaceEnsurer: ensurer,
		Driver:           driver,
	}

	_, err := lifecycle.ResumeLoaded(context.Background(), session, nil)
	if !errors.Is(err, startErr) {
		t.Fatalf("ResumeLoaded error = %v, want %v", err, startErr)
	}
	if ensurer.calls != 1 || driver.calls != 1 || store.updated != 0 {
		t.Fatalf("ensure/start/store update calls = %d/%d/%d, want 1/1/0", ensurer.calls, driver.calls, store.updated)
	}
	if session.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("workspace provisioning status = %q, want ready", session.WorkspaceProvisioning.Status)
	}
	if session.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("VM status = %q, want existing stopped mapping", session.Summary.VMStatus)
	}
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
	onUpdate   func()
	onEvent    func()
}

func (s *fakeLifecycleStore) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	return s.session, nil
}

func (s *fakeLifecycleStore) UpdateSandbox(_ context.Context, session *domain.Sandbox) error {
	s.updated++
	s.session = session
	if s.onUpdate != nil {
		s.onUpdate()
	}
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
	if s.onEvent != nil {
		s.onEvent()
	}
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
	order     *[]string
}

func (n *fakeLifecycleNotifier) PublishSandboxUpdated(*domain.SandboxSummary) {
	n.updated++
	if n.order != nil {
		*n.order = append(*n.order, "notify.updated")
	}
}

func (n *fakeLifecycleNotifier) PublishEventAdded(string, domain.SandboxEvent) {
	n.events++
	if n.order != nil {
		*n.order = append(*n.order, "notify.event")
	}
}

func (n *fakeLifecycleNotifier) NotifyDashboard(event string) {
	n.dashboard = event
	if n.order != nil {
		*n.order = append(*n.order, "notify.dashboard")
	}
}

type recordingWorkspaceEnsurer struct {
	calls    int
	err      error
	order    *[]string
	onEnsure func(*domain.Sandbox)
}

func (e *recordingWorkspaceEnsurer) Ensure(_ context.Context, session *domain.Sandbox) error {
	e.calls++
	if e.order != nil {
		*e.order = append(*e.order, "ensure")
	}
	if e.onEnsure != nil {
		e.onEnsure(session)
	}
	return e.err
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
	started  bool
	calls    int
	order    *[]string
	startErr error
}

func (d *recordingSandboxDriver) StartSandboxVM(context.Context, *domain.Sandbox) error {
	d.started = true
	d.calls++
	if d.order != nil {
		*d.order = append(*d.order, "driver.start")
	}
	return d.startErr
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
