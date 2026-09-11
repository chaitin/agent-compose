package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

// fakeDaemon is a stand-in for agent-compose. It serves the generated Connect
// handler rather than imitating the protocol, so the framing, codec and
// procedure paths under test are the real ones. It serves h2c because Connect
// needs HTTP/2 for bidirectional streams.
type fakeDaemon struct {
	agentcomposev2connect.UnimplementedRunServiceHandler

	t      *testing.T
	server *httptest.Server

	// The hooks below answer one procedure each. A procedure with no hook
	// answers NotFound, which is what an unconfigured expectation should look
	// like rather than a nil dereference.
	listRuns      func(*agentcomposev2.ListRunsRequest) (*agentcomposev2.ListRunsResponse, error)
	getRun        func(*agentcomposev2.GetRunRequest) (*agentcomposev2.GetRunResponse, error)
	listRunEvents func(*agentcomposev2.ListRunEventsRequest) (*agentcomposev2.ListRunEventsResponse, error)
	stopRun       func(*agentcomposev2.StopRunRequest) (*agentcomposev2.StopRunResponse, error)
	listProjects  func(*agentcomposev2.ListProjectsRequest) (*agentcomposev2.ListProjectsResponse, error)
	getProject    func(*agentcomposev2.GetProjectRequest) (*agentcomposev2.GetProjectResponse, error)
	attach        func(*fakeStream)

	// hold keeps handlers that emulate a detached session parked until the
	// test ends.
	hold chan struct{}

	mu    sync.Mutex
	calls []string
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	daemon := &fakeDaemon{t: t, hold: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle(agentcomposev2connect.NewRunServiceHandler(daemon))
	mux.Handle(agentcomposev2connect.NewProjectServiceHandler(&fakeProjects{daemon: daemon}))
	daemon.server = httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(daemon.server.Close)
	// Registered second so it runs first: Close waits on outstanding handlers,
	// and a parked one would never return.
	t.Cleanup(func() { close(daemon.hold) })
	return daemon
}

func (d *fakeDaemon) client(t *testing.T) *Client {
	t.Helper()
	client, err := New(Config{BaseURL: d.server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func (d *fakeDaemon) record(procedure string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, procedure)
}

// count reports how many times a procedure was called.
func (d *fakeDaemon) count(procedure string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	total := 0
	for _, call := range d.calls {
		if call == procedure {
			total++
		}
	}
	return total
}

func unconfigured(procedure string) error {
	return connect.NewError(connect.CodeNotFound, errors.New(procedure+" is not configured"))
}

func (d *fakeDaemon) ListRuns(_ context.Context, request *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
	d.record("ListRuns")
	if d.listRuns == nil {
		return nil, unconfigured("ListRuns")
	}
	response, err := d.listRuns(request.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (d *fakeDaemon) GetRun(_ context.Context, request *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	d.record("GetRun")
	if d.getRun == nil {
		return nil, unconfigured("GetRun")
	}
	response, err := d.getRun(request.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (d *fakeDaemon) ListRunEvents(_ context.Context, request *connect.Request[agentcomposev2.ListRunEventsRequest]) (*connect.Response[agentcomposev2.ListRunEventsResponse], error) {
	d.record("ListRunEvents")
	if d.listRunEvents == nil {
		return nil, unconfigured("ListRunEvents")
	}
	response, err := d.listRunEvents(request.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (d *fakeDaemon) StopRun(_ context.Context, request *connect.Request[agentcomposev2.StopRunRequest]) (*connect.Response[agentcomposev2.StopRunResponse], error) {
	d.record("StopRun")
	if d.stopRun == nil {
		return connect.NewResponse(&agentcomposev2.StopRunResponse{}), nil
	}
	response, err := d.stopRun(request.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (d *fakeDaemon) AttachAgentRun(_ context.Context, stream *connect.BidiStream[agentcomposev2.AttachAgentRunRequest, agentcomposev2.AttachAgentRunResponse]) error {
	d.record("AttachAgentRun")
	if d.attach == nil {
		return nil
	}
	d.attach(&fakeStream{t: d.t, stream: stream, hold: d.hold})
	return nil
}

// fakeStream is one attach, as the test script sees it.
type fakeStream struct {
	t      *testing.T
	stream *connect.BidiStream[agentcomposev2.AttachAgentRunRequest, agentcomposev2.AttachAgentRunResponse]
	// hold blocks a handler that should keep the session open the way a
	// detached daemon does. It is closed when the test's server shuts down.
	hold chan struct{}
}

// recv reads the next client frame, reporting false once the client stops
// sending. It never fails the test itself: this runs on the server's
// goroutine, which may outlive the test.
func (s *fakeStream) recv() (*agentcomposev2.AttachAgentRunRequest, bool) {
	frame, err := s.stream.Receive()
	if err != nil {
		return nil, false
	}
	return frame, true
}

func (s *fakeStream) send(frame *agentcomposev2.AttachAgentRunResponse) {
	_ = s.stream.Send(frame)
}

// agentEvent sends one provider-neutral agent event.
func (s *fakeStream) agentEvent(kind string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.send(&agentcomposev2.AttachAgentRunResponse{
		Frame: &agentcomposev2.AttachAgentRunResponse_AgentEvent{
			AgentEvent: &agentcomposev2.AttachAgentEvent{Name: kind, PayloadJson: string(encoded)},
		},
	})
}

// The constructors below keep a test's intent - "the daemon says the run
// started" - from being buried under the oneof wrapper the wire needs.

func started(runID, sandboxID string) *agentcomposev2.AttachAgentRunResponse {
	return &agentcomposev2.AttachAgentRunResponse{
		Frame: &agentcomposev2.AttachAgentRunResponse_Started{
			Started: &agentcomposev2.AttachStarted{RunId: runID, SandboxId: sandboxID},
		},
	}
}

func turnCompleted(resultJSON string) *agentcomposev2.AttachAgentRunResponse {
	return &agentcomposev2.AttachAgentRunResponse{
		Frame: &agentcomposev2.AttachAgentRunResponse_AgentTurnCompleted{
			AgentTurnCompleted: &agentcomposev2.AttachAgentTurnCompleted{ResultJson: resultJSON},
		},
	}
}

func runResult(success bool, failure string) *agentcomposev2.AttachAgentRunResponse {
	return &agentcomposev2.AttachAgentRunResponse{
		Frame: &agentcomposev2.AttachAgentRunResponse_Result{
			Result: &agentcomposev2.AttachResult{Success: success, Error: failure},
		},
	}
}

func attachFailure(code, message string, terminal bool) *agentcomposev2.AttachAgentRunResponse {
	return &agentcomposev2.AttachAgentRunResponse{
		Frame: &agentcomposev2.AttachAgentRunResponse_Error{
			Error: &agentcomposev2.AttachError{Code: code, Message: message, Terminal: terminal},
		},
	}
}

// runSummary builds a summary with the fields these tests actually assert on.
func runSummary(runID, status, sandboxID string, created ...*timestamppb.Timestamp) *agentcomposev2.RunSummary {
	summary := &agentcomposev2.RunSummary{
		RunId:     runID,
		SandboxId: sandboxID,
		Status:    agentcomposev2.RunStatus(agentcomposev2.RunStatus_value[strings.ToUpper(status)]),
	}
	if len(created) > 0 {
		summary.CreatedAt = created[0]
	}
	return summary
}

// fakeProjects serves the catalog half of the daemon. It is a type of its own
// because a handler value implements one service.
type fakeProjects struct {
	agentcomposev2connect.UnimplementedProjectServiceHandler

	daemon *fakeDaemon
}

func (p *fakeProjects) ListProjects(_ context.Context, request *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error) {
	p.daemon.record("ListProjects")
	if p.daemon.listProjects == nil {
		return nil, unconfigured("ListProjects")
	}
	response, err := p.daemon.listProjects(request.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (p *fakeProjects) GetProject(_ context.Context, request *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
	p.daemon.record("GetProject")
	if p.daemon.getProject == nil {
		return nil, unconfigured("GetProject")
	}
	response, err := p.daemon.getProject(request.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}
