package app

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
)

// Runs that outlive their request execute under the daemon root context, but
// the metadata of the request that started them must still reach execution:
// trusted ingress headers become the sandbox's capability binding, and the
// caller's trace context links agent telemetry to the caller's trace. Nothing
// else from the request context is carried over.
func TestRunSupervisorDetachedRunsKeepRequestMetadata(t *testing.T) {
	want := domain.RequestMetadata{
		TrustedHeaders: []domain.TrustedHeader{{Name: "x-mpi-username", Value: "alice"}},
		TraceContext: domain.TraceContext{
			Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			Tracestate:  "vendor=value",
		},
	}
	start := map[string]func(context.Context, *RunSupervisor) error{
		"detached attach": func(ctx context.Context, supervisor *RunSupervisor) error {
			first := true
			return supervisor.Attach(ctx, func() (runs.RunAttachInput, error) {
				if first {
					first = false
					return runs.RunAttachInput{
						Kind:             runs.RunAttachInputStart,
						Mode:             runs.RunAttachModePrompt,
						Request:          runs.RunAgentRequest{Prompt: "hello"},
						DisconnectPolicy: runs.AttachDisconnectDetach,
					}, nil
				}
				return runs.RunAttachInput{}, io.EOF
			}, func(runs.RunAttachOutput) error { return nil })
		},
		"interactive start": func(ctx context.Context, supervisor *RunSupervisor) error {
			_, err := supervisor.StartRun(ctx, runs.RunAgentRequest{Interactive: true, Prompt: "hello"})
			return err
		},
	}
	for name, run := range start {
		t.Run(name, func(t *testing.T) {
			rootCtx, cancelRoot := context.WithCancel(context.Background())
			t.Cleanup(cancelRoot)
			controller := newRunSupervisorHeaderController()
			supervisor := &RunSupervisor{root: rootCtx, controller: controller, active: map[string]*activeRun{}}
			requestCtx := domain.NewContextWithRequestMetadata(context.Background(), want)
			requestCtx = context.WithValue(requestCtx, runSupervisorTransportValueKey{}, "transport")
			requestCtx, cancelRequest := context.WithCancel(requestCtx)
			t.Cleanup(cancelRequest)

			done := make(chan error, 1)
			go func() { done <- run(requestCtx, supervisor) }()
			var got runSupervisorExecution
			select {
			case got = <-controller.executions:
			case <-time.After(time.Second):
				t.Fatal("run did not start")
			}
			if !reflect.DeepEqual(got.metadata, want) {
				t.Fatalf("execution request metadata = %#v, want %#v", got.metadata, want)
			}
			if got.transportValue != nil {
				t.Fatalf("execution context retained request value %#v", got.transportValue)
			}

			// Carrying the headers must not change the run's lifetime: it still
			// belongs to the daemon root, so root cancellation stops it.
			cancelRoot()
			select {
			case <-controller.canceled:
			case <-time.After(time.Second):
				t.Fatal("daemon root cancellation did not reach the detached run")
			}
			// An interactive start waits for the run to release its input, which
			// this stub never does; ending the request lets both starts return.
			cancelRequest()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("run start did not return")
			}
		})
	}
}

// A request without trusted headers or trace context still starts a run
// without any.
func TestRunSupervisorDetachedRunWithoutRequestMetadata(t *testing.T) {
	controller := newRunSupervisorHeaderController()
	supervisor := &RunSupervisor{root: context.Background(), controller: controller, active: map[string]*activeRun{}}
	go func() {
		_, _ = supervisor.StartRun(context.Background(), runs.RunAgentRequest{Interactive: true, Prompt: "hello"})
	}()
	select {
	case got := <-controller.executions:
		if !reflect.DeepEqual(got.metadata, domain.RequestMetadata{}) {
			t.Fatalf("execution request metadata = %#v, want none", got.metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not start")
	}
	close(controller.finish)
}

// runSupervisorTransportValueKey marks a request context value that is not
// request metadata, so a detached run must not see it.
type runSupervisorTransportValueKey struct{}

// runSupervisorExecution is what an execution context carries from the request
// that started the run.
type runSupervisorExecution struct {
	metadata       domain.RequestMetadata
	transportValue any
}

// runSupervisorHeaderController reports what the execution context carries from
// the request, which is the context StartProjectRun reads metadata from.
type runSupervisorHeaderController struct {
	executions chan runSupervisorExecution
	canceled   chan struct{}
	finish     chan struct{}
}

func newRunSupervisorHeaderController() *runSupervisorHeaderController {
	return &runSupervisorHeaderController{
		executions: make(chan runSupervisorExecution, 1),
		canceled:   make(chan struct{}),
		finish:     make(chan struct{}),
	}
}

func (*runSupervisorHeaderController) StartProjectRun(context.Context, runs.RunAgentRequest) (runs.StartedProjectRun, error) {
	return runs.StartedProjectRun{}, errors.New("unexpected StartProjectRun call")
}

func (c *runSupervisorHeaderController) RunProjectCommandAttachRegistered(
	execCtx context.Context,
	_ context.Context,
	receive runs.RunAttachReceiver,
	_ runs.RunAttachSender,
	onStarted func(string, <-chan struct{}),
) error {
	if _, err := receive(); err != nil {
		return err
	}
	onStarted("run-1", make(chan struct{}))
	c.executions <- runSupervisorExecution{
		metadata:       domain.RequestMetadataFromContext(execCtx),
		transportValue: execCtx.Value(runSupervisorTransportValueKey{}),
	}
	select {
	case <-execCtx.Done():
		close(c.canceled)
		return context.Cause(execCtx)
	case <-c.finish:
		return nil
	}
}
