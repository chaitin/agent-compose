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

func TestLifecycleStopLoadedConfirmsUnresolvedStartAttempt(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	session := lifecycleTestSession("session-start-attempt", driverpkg.RuntimeDriverDocker, domain.VMStatusStopped)
	store := &fakeLifecycleStore{session: session, vmState: domain.VMState{
		StoppedAt: now.Add(-2 * time.Hour), StartAttemptedAt: now.Add(-time.Hour),
	}}
	driver := &fakeSandboxDriver{}
	lifecycle := Lifecycle{Store: store, Driver: driver, Locks: NewLifecycleLocks()}

	loaded, stopped, err := lifecycle.StopLoaded(context.Background(), session)
	if err != nil {
		t.Fatalf("StopLoaded returned error: %v", err)
	}
	if !stopped || !driver.stopped || loaded.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("stopped/driver/loaded = %v/%v/%#v", stopped, driver.stopped, loaded)
	}
	if store.updated != 1 || store.events != 1 {
		t.Fatalf("updates/events = %d/%d, want 1/1", store.updated, store.events)
	}
}

func TestLifecycleStopLoadedSkipsConfirmedStoppedSandbox(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	session := lifecycleTestSession("session-confirmed-stop", driverpkg.RuntimeDriverDocker, domain.VMStatusStopped)
	store := &fakeLifecycleStore{session: session, vmState: domain.VMState{
		StartAttemptedAt: now.Add(-2 * time.Hour), StoppedAt: now.Add(-time.Hour),
	}}
	driver := &fakeSandboxDriver{}
	lifecycle := Lifecycle{Store: store, Driver: driver, Locks: NewLifecycleLocks()}

	loaded, stopped, err := lifecycle.StopLoaded(context.Background(), session)
	if err != nil || stopped || driver.stopped || loaded != session {
		t.Fatalf("StopLoaded confirmed stop = %#v/%v/%v, driver stopped=%v", loaded, stopped, err, driver.stopped)
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
			Driver:           &fakeSandboxDriver{startErr: errors.New("start failed")},
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
		driver := &recordingSandboxDriver{onStart: func(started *domain.Sandbox) {
			if got := domain.SandboxEnvMap(started.RuntimeEnvItems)["AGENT_COMPOSE_SANDBOX_TOKEN"]; got != "retry-token" {
				t.Fatalf("runtime token = %q, want retry-token", got)
			}
		}}
		prepareCalls := 0
		lifecycle := Lifecycle{
			Config:           &appconfig.Config{SandboxStartTimeout: time.Second},
			Store:            store,
			WorkspaceEnsurer: &recordingWorkspaceEnsurer{},
			Driver:           driver,
			PrepareAgentEnvironment: func(_ context.Context, sandbox *domain.Sandbox) error {
				prepareCalls++
				sandbox.RuntimeEnvItems = []domain.SandboxEnvVar{{Name: "AGENT_COMPOSE_SANDBOX_TOKEN", Value: "retry-token"}}
				return nil
			},
		}
		loaded, loadedProxy, err := lifecycle.EnsureProxyReady(context.Background(), session.Summary.ID)
		if err != nil {
			t.Fatalf("EnsureProxyReady returned error: %v", err)
		}
		if prepareCalls != 1 || !driver.started || loaded.Summary.VMStatus != domain.VMStatusRunning || loadedProxy.ProxyPath != "/lab" {
			t.Fatalf("driver/loaded/proxy = %v/%#v/%#v", driver.started, loaded, loadedProxy)
		}
	})

	t.Run("previously started sandbox skips agent preparation", func(t *testing.T) {
		session := lifecycleTestSession("session-running-unreachable", driverpkg.RuntimeDriverDocker, domain.VMStatusRunning)
		store := &fakeLifecycleStore{
			session:    session,
			vmState:    domain.VMState{StartedAt: time.Now().UTC()},
			proxyState: domain.ProxyState{Enabled: true, HostPort: unusedTCPPort(t), GuestPort: 8888},
		}
		prepareCalls := 0
		lifecycle := Lifecycle{
			Config:           &appconfig.Config{SandboxStartTimeout: time.Second},
			Store:            store,
			WorkspaceEnsurer: &recordingWorkspaceEnsurer{},
			Driver:           &recordingSandboxDriver{},
			PrepareAgentEnvironment: func(context.Context, *domain.Sandbox) error {
				prepareCalls++
				return nil
			},
		}

		if _, _, err := lifecycle.EnsureProxyReady(context.Background(), session.Summary.ID); err != nil {
			t.Fatalf("EnsureProxyReady returned error: %v", err)
		}
		if prepareCalls != 0 {
			t.Fatalf("agent preparation calls = %d, want 0", prepareCalls)
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

func TestLifecycleRejectsUnsupportedRuntimeBeforeResumeSideEffects(t *testing.T) {
	unsupported := domain.ClassifyError(domain.ErrUnsupported, "", driverpkg.ErrRuntimeDriverNotCompiled)

	t.Run("proxy start", func(t *testing.T) {
		session := lifecycleTestSession("session-proxy-unsupported", driverpkg.RuntimeDriverMicrosandbox, domain.VMStatusStopped)
		store := &fakeLifecycleStore{session: session, proxyState: domain.ProxyState{Enabled: true, HostPort: unusedTCPPort(t), GuestPort: 8888}}
		driver := &fakeSandboxDriver{validateErr: unsupported}
		lifecycle := Lifecycle{
			Config: &appconfig.Config{SandboxStartTimeout: time.Second},
			Store:  store,
			Driver: driver,
		}

		_, _, err := lifecycle.EnsureProxyReady(context.Background(), session.Summary.ID)
		if !errors.Is(err, domain.ErrUnsupported) || !errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
			t.Fatalf("EnsureProxyReady error = %v, want unsupported runtime", err)
		}
		if driver.started || store.updated != 0 || store.events != 0 || session.Summary.VMStatus != domain.VMStatusStopped {
			t.Fatalf("unsupported proxy resume mutated state: driver=%#v store=%#v session=%#v", driver, store, session)
		}
	})

	t.Run("explicit resume", func(t *testing.T) {
		session := lifecycleTestSession("session-resume-unsupported", driverpkg.RuntimeDriverMicrosandbox, domain.VMStatusStopped)
		store := &fakeLifecycleStore{session: session}
		driver := &fakeSandboxDriver{validateErr: unsupported}
		guideWrites := 0
		lifecycle := Lifecycle{
			Config: &appconfig.Config{},
			Store:  store,
			Driver: driver,
			GuideWriter: func(context.Context, *domain.Sandbox, []string) {
				guideWrites++
			},
		}

		_, err := lifecycle.ResumeLoaded(context.Background(), session, []string{"dev"})
		if !errors.Is(err, domain.ErrUnsupported) || !errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
			t.Fatalf("ResumeLoaded error = %v, want unsupported runtime", err)
		}
		if driver.started || guideWrites != 0 || store.updated != 0 || store.events != 0 || session.Summary.VMStatus != domain.VMStatusStopped {
			t.Fatalf("unsupported explicit resume mutated state: driver=%#v guide=%d store=%#v session=%#v", driver, guideWrites, store, session)
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
		PrepareAgentEnvironment: func(_ context.Context, sandbox *domain.Sandbox) error {
			order = append(order, "agent.prepare")
			sandbox.RuntimeEnvItems = []domain.SandboxEnvVar{{Name: "AGENT_COMPOSE_SANDBOX_TOKEN", Value: "retry-token"}}
			return nil
		},
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
	wantOrder := "ensure,agent.prepare,guide,driver.start,store.update,notify.updated,notify.dashboard,event.persist,notify.event"
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

func TestIntegrationLifecycleRejectsUnsupportedRuntimeBeforeResumeSideEffects(t *testing.T) {
	TestLifecycleRejectsUnsupportedRuntimeBeforeResumeSideEffects(t)
}

func TestE2ELifecycleRejectsUnsupportedRuntimeBeforeResumeSideEffects(t *testing.T) {
	TestLifecycleRejectsUnsupportedRuntimeBeforeResumeSideEffects(t)
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
	validateErr error
	startErr    error
	stopErr     error
	started     bool
	stopped     bool
}

func (d *fakeSandboxDriver) ValidateSandboxRuntime(*domain.Sandbox) error {
	return d.validateErr
}

func (d *fakeSandboxDriver) StartSandboxVM(context.Context, *domain.Sandbox) error {
	d.started = true
	return d.startErr
}

func (d *fakeSandboxDriver) StopSandboxVM(context.Context, *domain.Sandbox) error {
	d.stopped = true
	return d.stopErr
}

type recordingSandboxDriver struct {
	started  bool
	calls    int
	order    *[]string
	onStart  func(*domain.Sandbox)
	startErr error
}

func (d *recordingSandboxDriver) StartSandboxVM(_ context.Context, sandbox *domain.Sandbox) error {
	d.started = true
	d.calls++
	if d.order != nil {
		*d.order = append(*d.order, "driver.start")
	}
	if d.onStart != nil {
		d.onStart(sandbox)
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
