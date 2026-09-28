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
// caller's trace context links agent telemetry to the caller's trace.
func TestRunSupervisorDetachedRunsKeepRequestMetadata(t *testing.T) {
	want := runSupervisorRequestMetadata{
		headers: []domain.TrustedHeader{{Name: "x-mpi-username", Value: "alice"}},
		trace: domain.TraceContext{
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
			requestCtx := domain.NewContextWithTrustedHeaders(context.Background(), want.headers)
			requestCtx = domain.NewContextWithTraceContext(requestCtx, want.trace)
			requestCtx, cancelRequest := context.WithCancel(requestCtx)
			t.Cleanup(cancelRequest)

			done := make(chan error, 1)
			go func() { done <- run(requestCtx, supervisor) }()
			var got runSupervisorRequestMetadata
			select {
			case got = <-controller.metadata:
			case <-time.After(time.Second):
				t.Fatal("run did not start")
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("execution request metadata = %#v, want %#v", got, want)
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
	case got := <-controller.metadata:
		if !reflect.DeepEqual(got, runSupervisorRequestMetadata{}) {
			t.Fatalf("execution request metadata = %#v, want none", got)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not start")
	}
	close(controller.finish)
}

// runSupervisorRequestMetadata is the request metadata visible on an execution
// context.
type runSupervisorRequestMetadata struct {
	headers []domain.TrustedHeader
	trace   domain.TraceContext
}

// runSupervisorHeaderController reports the request metadata visible on the
// execution context, which is the context StartProjectRun reads it from.
type runSupervisorHeaderController struct {
	metadata chan runSupervisorRequestMetadata
	canceled chan struct{}
	finish   chan struct{}
}

func newRunSupervisorHeaderController() *runSupervisorHeaderController {
	return &runSupervisorHeaderController{
		metadata: make(chan runSupervisorRequestMetadata, 1),
		canceled: make(chan struct{}),
		finish:   make(chan struct{}),
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
	c.metadata <- runSupervisorRequestMetadata{
		headers: domain.TrustedHeadersFromContext(execCtx),
		trace:   domain.TraceContextFromContext(execCtx),
	}
	select {
	case <-execCtx.Done():
		close(c.canceled)
		return context.Cause(execCtx)
	case <-c.finish:
		return nil
	}
}
