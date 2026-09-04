package e2e

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestGracefulSandboxStopOutcomesUsePublicConnectContract(t *testing.T) {
	tests := []struct {
		name        string
		request     *agentcomposev2.StopSandboxRequest
		preparation sandboxes.StopPreparationOutcome
		want        agentcomposev2.SandboxStopOutcome
		wantOptions sandboxes.StopOptions
		wantCode    connect.Code
		sandboxVM   string
		wantForce   bool
		wantNoCall  bool
	}{
		{name: "graceful", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL}, preparation: sandboxes.StopPreparationGraceful, want: agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_GRACEFUL, wantOptions: sandboxes.StopOptions{Mode: sandboxes.StopModeGraceful}},
		{name: "explicit grace period", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL, GracePeriod: durationpb.New(12 * time.Second)}, preparation: sandboxes.StopPreparationGraceful, want: agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_GRACEFUL, wantOptions: sandboxes.StopOptions{Mode: sandboxes.StopModeGraceful, GracePeriod: 12 * time.Second}},
		{name: "timeout escalation", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL}, preparation: sandboxes.StopPreparationTimeout, want: agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE_AFTER_GRACE_TIMEOUT, wantOptions: sandboxes.StopOptions{Mode: sandboxes.StopModeGraceful}},
		{name: "error escalation", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL}, preparation: sandboxes.StopPreparationFailed, want: agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE_AFTER_GRACE_ERROR, wantOptions: sandboxes.StopOptions{Mode: sandboxes.StopModeGraceful}},
		{name: "force fallback", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL}, preparation: sandboxes.StopPreparationSkipped, want: agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE, wantOptions: sandboxes.StopOptions{Mode: sandboxes.StopModeGraceful}},
		{name: "explicit force", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_FORCE}, want: agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE, wantForce: true},
		{name: "stopped graceful is idempotent", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL}, want: agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_GRACEFUL, sandboxVM: domain.VMStatusStopped, wantNoCall: true},
		{name: "stopped force is idempotent", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_FORCE}, want: agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE, sandboxVM: domain.VMStatusStopped, wantNoCall: true},
		{name: "force rejects grace period", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_FORCE, GracePeriod: durationpb.New(time.Second)}, wantCode: connect.CodeInvalidArgument},
		{name: "graceful rejects zero period", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL, GracePeriod: durationpb.New(0)}, wantCode: connect.CodeInvalidArgument},
		{name: "graceful rejects invalid duration", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL, GracePeriod: &durationpb.Duration{Seconds: 315576000001}}, wantCode: connect.CodeInvalidArgument},
		{name: "rejects unknown mode", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode(99)}, wantCode: connect.CodeInvalidArgument},
		{name: "pending sandbox cannot stop", request: &agentcomposev2.StopSandboxRequest{Mode: agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL}, wantCode: connect.CodeFailedPrecondition, sandboxVM: domain.VMStatusPending},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sandboxID := identity.NewID(identity.ResourceSandbox, "graceful-stop-contract", test.name)
			sandboxVM := test.sandboxVM
			if sandboxVM == "" {
				sandboxVM = domain.VMStatusRunning
			}
			stored := &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: sandboxVM}}
			stopped := &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusStopped}}
			delegate := &e2eGracefulStopDelegate{outcome: sandboxes.StopOutcome{
				Sandbox:       stopped,
				Preparation:   sandboxes.StopPreparationResult{Outcome: test.preparation},
				DriverStopped: true,
			}}
			handler := api.NewSandboxHandler(api.SandboxHandlerDeps{Delegate: delegate, Store: &e2eGracefulStopStore{sandbox: stored}})
			servicePath, serviceHandler := agentcomposev2connect.NewSandboxServiceHandler(handler)
			mux := http.NewServeMux()
			mux.Handle(servicePath, serviceHandler)
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			client := agentcomposev2connect.NewSandboxServiceClient(server.Client(), server.URL)

			request := proto.Clone(test.request).(*agentcomposev2.StopSandboxRequest)
			request.SandboxId = sandboxID
			response, err := client.StopSandbox(context.Background(), connect.NewRequest(request))
			if test.wantCode != 0 {
				if connect.CodeOf(err) != test.wantCode {
					t.Fatalf("StopSandbox() error = %v, code = %s, want %s", err, connect.CodeOf(err), test.wantCode)
				}
				if delegate.sandboxID != "" || delegate.forceSandboxID != "" {
					t.Fatalf("invalid request reached delegate: graceful=%q force=%q", delegate.sandboxID, delegate.forceSandboxID)
				}
				return
			}
			if err != nil {
				t.Fatalf("StopSandbox() error = %v", err)
			}
			if response.Msg.GetOutcome() != test.want || response.Msg.GetSandbox().GetStatus() != agentcomposev2.SandboxStatus_SANDBOX_STATUS_STOPPED {
				t.Fatalf("StopSandbox() outcome/status = %s/%s, want %s/STOPPED", response.Msg.GetOutcome(), response.Msg.GetSandbox().GetStatus(), test.want)
			}
			if test.wantNoCall {
				if delegate.sandboxID != "" || delegate.forceSandboxID != "" {
					t.Fatalf("idempotent stop reached delegate: graceful=%q force=%q", delegate.sandboxID, delegate.forceSandboxID)
				}
				return
			}
			if test.wantForce {
				if delegate.forceSandboxID != sandboxID || delegate.sandboxID != "" {
					t.Fatalf("force delegate call = force %q graceful %q", delegate.forceSandboxID, delegate.sandboxID)
				}
				return
			}
			if delegate.sandboxID != sandboxID || delegate.options != test.wantOptions {
				t.Fatalf("delegate call = id %q options %#v", delegate.sandboxID, delegate.options)
			}
		})
	}
	t.Run("request cancellation still contains runtime", testCancelledGracefulStopContainment)
}

func testCancelledGracefulStopContainment(t *testing.T) {
	t.Helper()
	sandboxID := identity.NewID(identity.ResourceSandbox, "graceful-stop-contract", "cancelled-request")
	store := &e2eLifecycleStopStore{sandbox: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, Driver: "docker", VMStatus: domain.VMStatusRunning}}}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	driver := &e2eCancellationStopDriver{preparationStarted: make(chan struct{})}
	delegate := &e2eLifecycleStopDelegate{
		lifecycle: sandboxes.Lifecycle{
			Config: &appconfig.Config{SandboxStopTimeout: time.Second},
			Store:  store,
			Driver: driver,
			Locks:  sandboxes.NewLifecycleLocks(),
		},
		store:  store,
		result: make(chan e2eLifecycleStopResult, 1),
	}
	handler := api.NewSandboxHandler(api.SandboxHandlerDeps{Delegate: delegate, Store: store})
	servicePath, serviceHandler := agentcomposev2connect.NewSandboxServiceHandler(handler)
	mux := http.NewServeMux()
	mux.Handle(servicePath, serviceHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := agentcomposev2connect.NewSandboxServiceClient(server.Client(), server.URL)
	clientResult := make(chan error, 1)
	go func() {
		_, err := client.StopSandbox(requestCtx, connect.NewRequest(&agentcomposev2.StopSandboxRequest{
			SandboxId: sandboxID,
			Mode:      agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL,
		}))
		clientResult <- err
	}()

	select {
	case <-driver.preparationStarted:
		cancelRequest()
	case <-time.After(time.Second):
		t.Fatal("graceful stop preparation did not start")
	}

	select {
	case result := <-delegate.result:
		if !errors.Is(result.err, context.Canceled) || !result.outcome.DriverStopped {
			t.Fatalf("lifecycle stop result = %#v, %v; want stopped with request cancellation", result.outcome, result.err)
		}
		if driver.stopCtxErr != nil || !driver.finished || store.sandbox.Summary.VMStatus != domain.VMStatusStopped {
			t.Fatalf("containment state = context %v, finished %v, status %s", driver.stopCtxErr, driver.finished, store.sandbox.Summary.VMStatus)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon containment did not finish after request cancellation")
	}

	select {
	case err := <-clientResult:
		if !errors.Is(err, context.Canceled) && connect.CodeOf(err) != connect.CodeCanceled {
			t.Fatalf("client StopSandbox() error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled client call did not return")
	}
}

type e2eGracefulStopDelegate struct {
	outcome        sandboxes.StopOutcome
	sandboxID      string
	forceSandboxID string
	options        sandboxes.StopOptions
}

func (*e2eGracefulStopDelegate) ResumeSandbox(context.Context, string) (*domain.Sandbox, error) {
	return nil, nil
}

func (d *e2eGracefulStopDelegate) StopSandbox(_ context.Context, sandboxID string) (*domain.Sandbox, error) {
	d.forceSandboxID = sandboxID
	return d.outcome.Sandbox, nil
}

func (*e2eGracefulStopDelegate) GetSandboxProxy(context.Context, string) (api.SandboxProxy, error) {
	return api.SandboxProxy{}, nil
}

func (d *e2eGracefulStopDelegate) StopSandboxWithOptions(_ context.Context, sandboxID string, options sandboxes.StopOptions) (sandboxes.StopOutcome, error) {
	d.sandboxID = sandboxID
	d.options = options
	return d.outcome, nil
}

type e2eGracefulStopStore struct {
	sandbox *domain.Sandbox
}

func (s *e2eGracefulStopStore) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	return s.sandbox, nil
}

func (*e2eGracefulStopStore) RemoveSandbox(context.Context, string) error {
	return nil
}

type e2eLifecycleStopResult struct {
	outcome sandboxes.StopOutcome
	err     error
}

type e2eLifecycleStopDelegate struct {
	lifecycle sandboxes.Lifecycle
	store     *e2eLifecycleStopStore
	result    chan e2eLifecycleStopResult
}

func (*e2eLifecycleStopDelegate) ResumeSandbox(context.Context, string) (*domain.Sandbox, error) {
	return nil, nil
}

func (*e2eLifecycleStopDelegate) StopSandbox(context.Context, string) (*domain.Sandbox, error) {
	return nil, nil
}

func (*e2eLifecycleStopDelegate) GetSandboxProxy(context.Context, string) (api.SandboxProxy, error) {
	return api.SandboxProxy{}, nil
}

func (d *e2eLifecycleStopDelegate) StopSandboxWithOptions(ctx context.Context, sandboxID string, options sandboxes.StopOptions) (sandboxes.StopOutcome, error) {
	sandbox, err := d.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return sandboxes.StopOutcome{}, err
	}
	outcome, err := d.lifecycle.StopLoadedWithOptions(ctx, sandbox, options)
	d.result <- e2eLifecycleStopResult{outcome: outcome, err: err}
	return outcome, err
}

type e2eLifecycleStopStore struct {
	sandbox *domain.Sandbox
	vmState domain.VMState
}

func (s *e2eLifecycleStopStore) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	return s.sandbox, nil
}

func (s *e2eLifecycleStopStore) UpdateSandbox(_ context.Context, sandbox *domain.Sandbox) error {
	s.sandbox = sandbox
	return nil
}

func (s *e2eLifecycleStopStore) GetVMState(string) (domain.VMState, error) {
	return s.vmState, nil
}

func (s *e2eLifecycleStopStore) SaveVMState(_ string, vmState domain.VMState) error {
	s.vmState = vmState
	return nil
}

func (*e2eLifecycleStopStore) GetProxyState(string) (domain.ProxyState, error) {
	return domain.ProxyState{}, nil
}

func (*e2eLifecycleStopStore) AddEvent(context.Context, string, domain.SandboxEvent) error {
	return nil
}

func (*e2eLifecycleStopStore) RemoveSandbox(context.Context, string) error {
	return nil
}

type e2eCancellationStopDriver struct {
	preparationStarted chan struct{}
	stopCtxErr         error
	finished           bool
}

func (*e2eCancellationStopDriver) StartSandboxVM(context.Context, *domain.Sandbox) error {
	return nil
}

func (d *e2eCancellationStopDriver) StopSandboxVM(ctx context.Context, _ *domain.Sandbox) error {
	d.stopCtxErr = ctx.Err()
	return d.stopCtxErr
}

func (d *e2eCancellationStopDriver) PrepareSandboxStop(ctx context.Context, _ *domain.Sandbox, _ domain.VMState, _ time.Duration) (sandboxes.StopPreparationResult, error) {
	close(d.preparationStarted)
	<-ctx.Done()
	return sandboxes.StopPreparationResult{Outcome: sandboxes.StopPreparationFailed, Error: ctx.Err()}, nil
}

func (d *e2eCancellationStopDriver) FinishSandboxStop(*domain.Sandbox) {
	d.finished = true
}
