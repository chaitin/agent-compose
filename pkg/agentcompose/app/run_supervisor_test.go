package app

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samber/do/v2"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
)

func TestRunSupervisorAttachMapsMissingStartFrameToInvalidRequest(t *testing.T) {
	supervisor := &RunSupervisor{}
	err := supervisor.Attach(context.Background(), func() (runs.RunAttachInput, error) {
		return runs.RunAttachInput{}, io.EOF
	}, func(runs.RunAttachOutput) error { return nil })
	if !errors.Is(err, runs.ErrInvalidRequest) {
		t.Fatalf("Attach() error = %v, want %v", err, runs.ErrInvalidRequest)
	}
}

func TestRunSupervisorAttachDetachCancelsInputWithoutCancelingExecution(t *testing.T) {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	t.Cleanup(cancelRoot)
	controller := newRunSupervisorAttachController()
	supervisor := &RunSupervisor{
		root:       rootCtx,
		controller: controller,
		active:     map[string]*activeRun{},
	}
	connectionCtx, cancelConnection := context.WithCancel(context.Background())
	attachDone := make(chan error, 1)
	first := true
	go func() {
		attachDone <- supervisor.Attach(connectionCtx, func() (runs.RunAttachInput, error) {
			if first {
				first = false
				return runs.RunAttachInput{
					Kind:             runs.RunAttachInputStart,
					Mode:             runs.RunAttachModePrompt,
					Request:          runs.RunAgentRequest{Prompt: "keep running"},
					DisconnectPolicy: runs.AttachDisconnectDetach,
				}, nil
			}
			return runs.RunAttachInput{}, io.EOF
		}, func(runs.RunAttachOutput) error { return nil })
	}()

	select {
	case <-controller.started:
	case <-time.After(time.Second):
		t.Fatal("detached run did not start")
	}
	cancelConnection()
	select {
	case <-controller.inputCanceled:
	case <-time.After(time.Second):
		t.Fatal("connection cancellation did not reach the attach input context")
	}
	select {
	case err := <-attachDone:
		t.Fatalf("detached execution ended with its connection: %v", err)
	default:
	}
	supervisor.mu.Lock()
	_, active := supervisor.active["run-1"]
	supervisor.mu.Unlock()
	if !active {
		t.Fatal("detached run was unregistered after connection cancellation")
	}

	close(controller.finishExecution)
	select {
	case err := <-attachDone:
		if err != nil {
			t.Fatalf("Attach() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Attach() did not finish after execution completed")
	}
}

func TestRunSupervisorInteractiveStartRequestCancellationDoesNotCancelExecution(t *testing.T) {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	t.Cleanup(cancelRoot)
	controller := newRunSupervisorInteractiveController(true)
	supervisor := &RunSupervisor{
		root:       rootCtx,
		controller: controller,
		active:     map[string]*activeRun{},
	}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() {
		_, err := supervisor.StartRun(requestCtx, runs.RunAgentRequest{Interactive: true, Prompt: "keep running"})
		startDone <- err
	}()

	<-controller.started
	cancelRequest()
	if err := <-startDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("StartRun() error = %v, want %v", err, context.Canceled)
	}
	select {
	case <-controller.executionCanceled:
		t.Fatal("background interactive execution was canceled with its request")
	default:
	}
	supervisor.mu.Lock()
	_, active := supervisor.active["run-1"]
	supervisor.mu.Unlock()
	if !active {
		t.Fatal("background interactive execution was unregistered with its request")
	}

	close(controller.finish)
	select {
	case <-controller.returned:
	case <-time.After(time.Second):
		t.Fatal("interactive execution did not finish")
	}
}

func TestRunSupervisorInteractiveStartRegistersWaitBeforeGoroutineRuns(t *testing.T) {
	controller := newRunSupervisorInteractiveController(false)
	supervisor := &RunSupervisor{
		root:       context.Background(),
		controller: controller,
		active:     map[string]*activeRun{},
	}
	startDone := make(chan error, 1)
	go func() {
		_, err := supervisor.StartRun(context.Background(), runs.RunAgentRequest{Interactive: true, Prompt: "wait for me"})
		startDone <- err
	}()
	<-controller.entered

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelShutdown()
	if err := supervisor.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want %v while interactive start is blocked", err, context.DeadlineExceeded)
	}
	close(controller.finish)
	if err := <-startDone; !errors.Is(err, errRunSupervisorInteractiveStopped) {
		t.Fatalf("StartRun() error = %v, want %v", err, errRunSupervisorInteractiveStopped)
	}
	if err := supervisor.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() after execution exit error = %v", err)
	}
}

var errRunSupervisorInteractiveStopped = errors.New("interactive controller stopped")

type runSupervisorInteractiveController struct {
	callStarted       bool
	entered           chan struct{}
	started           chan struct{}
	executionCanceled chan struct{}
	finish            chan struct{}
	returned          chan struct{}
}

func newRunSupervisorInteractiveController(callStarted bool) *runSupervisorInteractiveController {
	return &runSupervisorInteractiveController{
		callStarted:       callStarted,
		entered:           make(chan struct{}),
		started:           make(chan struct{}),
		executionCanceled: make(chan struct{}),
		finish:            make(chan struct{}),
		returned:          make(chan struct{}),
	}
}

func (*runSupervisorInteractiveController) ReattachDetachedRun(context.Context, domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	return domain.ProjectRunRecord{}, errors.New("unexpected ReattachDetachedRun call")
}

func (*runSupervisorInteractiveController) StartProjectRun(context.Context, runs.RunAgentRequest) (runs.StartedProjectRun, error) {
	return runs.StartedProjectRun{}, errors.New("unexpected StartProjectRun call")
}

func (c *runSupervisorInteractiveController) RunProjectCommandAttachRegistered(
	execCtx context.Context,
	_ context.Context,
	receive runs.RunAttachReceiver,
	_ runs.RunAttachSender,
	onStarted func(string, <-chan struct{}),
) error {
	defer close(c.returned)
	if _, err := receive(); err != nil {
		return err
	}
	close(c.entered)
	if c.callStarted {
		onStarted("run-1", make(chan struct{}))
		close(c.started)
	}
	select {
	case <-execCtx.Done():
		close(c.executionCanceled)
		return context.Cause(execCtx)
	case <-c.finish:
		return errRunSupervisorInteractiveStopped
	}
}

func (*runSupervisorInteractiveController) StopInteractiveRun(context.Context, string, string) (bool, error) {
	return false, nil
}

type runSupervisorAttachController struct {
	started         chan struct{}
	inputCanceled   chan struct{}
	finishExecution chan struct{}
	startOnce       sync.Once
}

func newRunSupervisorAttachController() *runSupervisorAttachController {
	return &runSupervisorAttachController{
		started:         make(chan struct{}),
		inputCanceled:   make(chan struct{}),
		finishExecution: make(chan struct{}),
	}
}

func (*runSupervisorAttachController) ReattachDetachedRun(context.Context, domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	return domain.ProjectRunRecord{}, errors.New("unexpected ReattachDetachedRun call")
}

func (*runSupervisorAttachController) StartProjectRun(context.Context, runs.RunAgentRequest) (runs.StartedProjectRun, error) {
	return runs.StartedProjectRun{}, errors.New("unexpected StartProjectRun call")
}

func (c *runSupervisorAttachController) RunProjectCommandAttachRegistered(
	execCtx context.Context,
	inputCtx context.Context,
	receive runs.RunAttachReceiver,
	_ runs.RunAttachSender,
	onStarted func(string, <-chan struct{}),
) error {
	if _, err := receive(); err != nil {
		return err
	}
	released := make(chan struct{})
	onStarted("run-1", released)
	c.startOnce.Do(func() { close(c.started) })
	<-inputCtx.Done()
	close(released)
	close(c.inputCanceled)
	select {
	case <-execCtx.Done():
		return errors.New("execution context canceled with connection")
	case <-c.finishExecution:
		return nil
	}
}

func (*runSupervisorAttachController) StopInteractiveRun(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestRunSupervisorStopActiveRunRequestsCancellationWithoutMarkingTerminal(t *testing.T) {
	ctx := context.Background()
	store := newRunSupervisorTestConfigStore(t)
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID:         "project-1",
		Name:       "project",
		SourcePath: "/tmp/project",
		SourceJSON: "{}",
	}); err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	agent, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ProjectID: "project-1", AgentName: "worker"})
	if err != nil {
		t.Fatalf("UpsertProjectAgent returned error: %v", err)
	}
	if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID:       "run-1",
		ProjectID:   "project-1",
		ProjectName: "project",
		AgentName:   "worker",
		AgentID:     agent.ID,
		Source:      domain.ProjectRunSourceManual,
		Status:      domain.ProjectRunStatusRunning,
		ResultJSON:  "{}",
	}); err != nil {
		t.Fatalf("CreateProjectRun returned error: %v", err)
	}

	cancelCalls := 0
	var cancelCause error
	supervisor := &RunSupervisor{
		store:  store,
		active: map[string]*activeRun{"run-1": {cancel: func(cause error) { cancelCalls++; cancelCause = cause }}},
	}
	stopped, err := supervisor.StopActiveRun(ctx, "run-1", "user stop")
	if err != nil {
		t.Fatalf("StopActiveRun returned error: %v", err)
	}
	if !stopped || cancelCalls != 1 || cancelCause == nil || cancelCause.Error() != "user stop" {
		t.Fatalf("first stop stopped=%v cancelCalls=%d, want true/1", stopped, cancelCalls)
	}
	if _, ok := supervisor.active["run-1"]; !ok {
		t.Fatalf("run was removed before execution exited")
	}

	stopped, err = supervisor.StopActiveRun(ctx, "run-1", "second stop")
	if err != nil {
		t.Fatalf("second StopActiveRun returned error: %v", err)
	}
	if !stopped || cancelCalls != 1 {
		t.Fatalf("second stop stopped=%v cancelCalls=%d, want true/1", stopped, cancelCalls)
	}
	run, err := store.GetProjectRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	if run.Status != domain.ProjectRunStatusRunning || run.Error != "" {
		t.Fatalf("run after stop = %#v", run)
	}
	supervisor.unregister("run-1")
	if _, ok := supervisor.active["run-1"]; ok {
		t.Fatal("run remained active after execution exit")
	}
}

func TestRunSupervisorUnregisterRemovesStoppingRun(t *testing.T) {
	active := &activeRun{
		cancel:   func(error) {},
		stopping: true,
	}
	supervisor := &RunSupervisor{
		active: map[string]*activeRun{"run-1": active},
	}
	supervisor.unregister("run-1")
	if _, ok := supervisor.active["run-1"]; ok {
		t.Fatalf("stopping run remained registered")
	}
}

// runSupervisorStartController returns a fixed run for StartProjectRun and
// counts Execute calls so the idempotency dedup in StartRun/Run can be asserted.
type runSupervisorStartController struct {
	run      domain.ProjectRunRecord
	executed atomic.Int32
}

func (c *runSupervisorStartController) StartProjectRun(_ context.Context, _ runs.RunAgentRequest) (runs.StartedProjectRun, error) {
	return runs.StartedProjectRun{
		Run: c.run,
		Execute: func(context.Context, *runs.StreamSink) (domain.ProjectRunRecord, error, error) {
			c.executed.Add(1)
			return c.run, nil, nil
		},
	}, nil
}

func (*runSupervisorStartController) RunProjectCommandAttachRegistered(context.Context, context.Context, runs.RunAttachReceiver, runs.RunAttachSender, func(string, <-chan struct{})) error {
	return errors.New("unexpected RunProjectCommandAttachRegistered call")
}

func (*runSupervisorStartController) ReattachDetachedRun(context.Context, domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	return domain.ProjectRunRecord{}, errors.New("unexpected ReattachDetachedRun call")
}

func (*runSupervisorStartController) StopInteractiveRun(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestRunSupervisorStartRunDoesNotReexecuteRunningRun(t *testing.T) {
	// A detached run that survived a daemon restart is already running: a retry
	// with the same idempotency key must resolve to it without starting a second
	// execution.
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	t.Cleanup(cancelRoot)
	controller := &runSupervisorStartController{run: domain.ProjectRunRecord{RunID: "run-1", Status: domain.ProjectRunStatusRunning}}
	supervisor := &RunSupervisor{root: rootCtx, controller: controller, active: map[string]*activeRun{}}

	run, err := supervisor.StartRun(context.Background(), runs.RunAgentRequest{})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if run.RunID != "run-1" || run.Status != domain.ProjectRunStatusRunning {
		t.Fatalf("StartRun() run = %#v, want running run-1", run)
	}
	if controller.executed.Load() != 0 {
		t.Fatalf("Execute called %d times, want 0 for an already-running run", controller.executed.Load())
	}
	supervisor.mu.Lock()
	_, active := supervisor.active["run-1"]
	supervisor.mu.Unlock()
	if active {
		t.Fatal("already-running run was registered for a second execution")
	}
}

func TestRunSupervisorStartRunDoesNotReexecuteRegisteredRun(t *testing.T) {
	// Two concurrent requests with the same idempotency key both observe the
	// run as pending; the second must not execute again and must not overwrite
	// the first request's registration.
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	t.Cleanup(cancelRoot)
	controller := &runSupervisorStartController{run: domain.ProjectRunRecord{RunID: "run-1", Status: domain.ProjectRunStatusPending}}
	supervisor := &RunSupervisor{
		root:       rootCtx,
		controller: controller,
		active:     map[string]*activeRun{"run-1": {cancel: func(error) {}}},
	}

	run, err := supervisor.StartRun(context.Background(), runs.RunAgentRequest{})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if run.RunID != "run-1" {
		t.Fatalf("StartRun() run = %#v, want run-1", run)
	}
	if controller.executed.Load() != 0 {
		t.Fatalf("Execute called %d times, want 0 for an already-registered run", controller.executed.Load())
	}
	supervisor.mu.Lock()
	active, ok := supervisor.active["run-1"]
	supervisor.mu.Unlock()
	if !ok || active == nil {
		t.Fatal("existing registration was removed or overwritten")
	}
}

func TestRunSupervisorRunDoesNotReexecuteRunningRun(t *testing.T) {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	t.Cleanup(cancelRoot)
	controller := &runSupervisorStartController{run: domain.ProjectRunRecord{RunID: "run-1", Status: domain.ProjectRunStatusRunning}}
	supervisor := &RunSupervisor{root: rootCtx, controller: controller, active: map[string]*activeRun{}}

	run, _, err := supervisor.Run(context.Background(), runs.RunAgentRequest{}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if run.RunID != "run-1" {
		t.Fatalf("Run() run = %#v, want run-1", run)
	}
	if controller.executed.Load() != 0 {
		t.Fatalf("Execute called %d times, want 0 for an already-running run", controller.executed.Load())
	}
}

// runSupervisorInteractiveRunningController simulates an interactive start whose
// idempotency key resolves to an already-running run: onStarted fires and the
// attach returns immediately without running a second session.
type runSupervisorInteractiveRunningController struct {
	runID string
}

func (*runSupervisorInteractiveRunningController) StartProjectRun(context.Context, runs.RunAgentRequest) (runs.StartedProjectRun, error) {
	return runs.StartedProjectRun{}, errors.New("unexpected StartProjectRun call")
}

func (c *runSupervisorInteractiveRunningController) RunProjectCommandAttachRegistered(
	_ context.Context,
	_ context.Context,
	_ runs.RunAttachReceiver,
	_ runs.RunAttachSender,
	onStarted func(string, <-chan struct{}),
) error {
	if onStarted != nil {
		released := make(chan struct{})
		onStarted(c.runID, released)
		close(released)
	}
	return nil
}

func (*runSupervisorInteractiveRunningController) ReattachDetachedRun(context.Context, domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	return domain.ProjectRunRecord{}, errors.New("unexpected ReattachDetachedRun call")
}

func (*runSupervisorInteractiveRunningController) StopInteractiveRun(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestRunSupervisorInteractiveStartReturnsRunningRunOnRetry(t *testing.T) {
	// An interactive retry whose key resolves to an already-running run must
	// return that run, not race the started/failed channels into an empty run.
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	t.Cleanup(cancelRoot)
	store := newRunSupervisorTestConfigStore(t)
	if _, err := store.UpsertProject(context.Background(), domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: "/tmp/project", SourceJSON: "{}"}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	agent, err := store.UpsertProjectAgent(context.Background(), domain.ProjectAgentRecord{ProjectID: "project-1", AgentName: "worker"})
	if err != nil {
		t.Fatalf("UpsertProjectAgent: %v", err)
	}
	if _, err := store.CreateProjectRun(context.Background(), domain.ProjectRunRecord{
		RunID: "run-1", ProjectID: "project-1", ProjectName: "project", AgentName: "worker", AgentID: agent.ID,
		Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusRunning, ResultJSON: "{}",
	}); err != nil {
		t.Fatalf("CreateProjectRun: %v", err)
	}

	controller := &runSupervisorInteractiveRunningController{runID: "run-1"}
	supervisor := &RunSupervisor{root: rootCtx, controller: controller, store: store, active: map[string]*activeRun{}}

	run, err := supervisor.StartRun(context.Background(), runs.RunAgentRequest{Interactive: true, Prompt: "already running"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if run.RunID != "run-1" || run.Status != domain.ProjectRunStatusRunning {
		t.Fatalf("StartRun() run = %#v, want running run-1", run)
	}
}

func newRunSupervisorTestConfigStore(t *testing.T) *configstore.ConfigStore {
	t.Helper()
	root := t.TempDir()
	di := do.New()
	do.ProvideValue(di, context.Background())
	do.ProvideValue(di, &appconfig.Config{
		DataRoot: root,
		DbAddr:   filepath.Join(root, "data.db"),
	})
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	t.Cleanup(func() {
		if db := store.DB(); db != nil {
			_ = db.Close()
		}
	})
	return store
}

// runSupervisorReattachController blocks a re-attach until its context ends
// and reports why it ended.
type runSupervisorReattachController struct {
	started chan context.Context
	ended   chan error
	fail    error
}

func newRunSupervisorReattachController() *runSupervisorReattachController {
	return &runSupervisorReattachController{started: make(chan context.Context, 1), ended: make(chan error, 1)}
}

func (*runSupervisorReattachController) StartProjectRun(context.Context, runs.RunAgentRequest) (runs.StartedProjectRun, error) {
	return runs.StartedProjectRun{}, errors.New("unexpected StartProjectRun call")
}

func (*runSupervisorReattachController) RunProjectCommandAttachRegistered(context.Context, context.Context, runs.RunAttachReceiver, runs.RunAttachSender, func(string, <-chan struct{})) error {
	return errors.New("unexpected RunProjectCommandAttachRegistered call")
}

func (c *runSupervisorReattachController) ReattachDetachedRun(ctx context.Context, run domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	if c.fail != nil {
		return run, c.fail
	}
	c.started <- ctx
	<-ctx.Done()
	c.ended <- context.Cause(ctx)
	return run, nil
}

func (*runSupervisorReattachController) StopInteractiveRun(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestRunSupervisorReattachRunCanBeStopped(t *testing.T) {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	t.Cleanup(cancelRoot)
	controller := newRunSupervisorReattachController()
	supervisor := &RunSupervisor{root: rootCtx, controller: controller, active: map[string]*activeRun{}}

	supervisor.ReattachRun(domain.ProjectRunRecord{RunID: "run-1"}, nil)
	<-controller.started
	stopped, err := supervisor.StopActiveRun(context.Background(), "run-1", "user stop")
	if err != nil || !stopped {
		t.Fatalf("StopActiveRun() = %v, %v; want the re-attached run to be stoppable", stopped, err)
	}
	select {
	case cause := <-controller.ended:
		if cause == nil || cause.Error() != "user stop" {
			t.Fatalf("re-attach ended with %v, want the stop reason", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("re-attach was not cancelled by StopActiveRun")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := supervisor.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	supervisor.mu.Lock()
	_, active := supervisor.active["run-1"]
	supervisor.mu.Unlock()
	if active {
		t.Fatal("re-attached run still registered after it ended")
	}
}

func TestRunSupervisorReattachRunSurvivesShutdownSignal(t *testing.T) {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	controller := newRunSupervisorReattachController()
	supervisor := &RunSupervisor{root: rootCtx, controller: controller, active: map[string]*activeRun{}}

	supervisor.ReattachRun(domain.ProjectRunRecord{RunID: "run-1"}, nil)
	runCtx := <-controller.started
	cancelRoot()
	// The run context must not end on shutdown (the follower disconnects via
	// the carried signal instead), so the guest task is never stopped.
	shutdown, ok := runs.DaemonShutdown(runCtx)
	if !ok {
		t.Fatal("re-attach context does not carry the daemon shutdown signal")
	}
	<-shutdown
	select {
	case <-runCtx.Done():
		t.Fatal("re-attach context was cancelled by daemon shutdown")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := supervisor.StopActiveRun(context.Background(), "run-1", "cleanup"); err != nil {
		t.Fatalf("StopActiveRun() error = %v", err)
	}
	<-controller.ended
}

func TestRunSupervisorReattachRunReportsFailure(t *testing.T) {
	controller := newRunSupervisorReattachController()
	controller.fail = errors.New("sandbox lookup failed")
	supervisor := &RunSupervisor{root: context.Background(), controller: controller, active: map[string]*activeRun{}}

	failed := make(chan error, 1)
	supervisor.ReattachRun(domain.ProjectRunRecord{RunID: "run-1"}, func(err error) { failed <- err })
	select {
	case err := <-failed:
		if err == nil || err.Error() != "sandbox lookup failed" {
			t.Fatalf("onFailure got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("onFailure was not called")
	}
}

type runSupervisorStoppingController struct {
	handled bool
	stopErr error
	stops   chan string
}

func (*runSupervisorStoppingController) StartProjectRun(context.Context, runs.RunAgentRequest) (runs.StartedProjectRun, error) {
	return runs.StartedProjectRun{}, errors.New("unexpected StartProjectRun call")
}

func (*runSupervisorStoppingController) RunProjectCommandAttachRegistered(
	context.Context,
	context.Context,
	runs.RunAttachReceiver,
	runs.RunAttachSender,
	func(string, <-chan struct{}),
) error {
	return errors.New("unexpected RunProjectCommandAttachRegistered call")
}

func (c *runSupervisorStoppingController) StopInteractiveRun(_ context.Context, runID, reason string) (bool, error) {
	select {
	case c.stops <- runID + ":" + reason:
	default:
	}
	return c.handled, c.stopErr
}

func (*runSupervisorStoppingController) ReattachDetachedRun(context.Context, domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	return domain.ProjectRunRecord{}, errors.New("unexpected ReattachDetachedRun call")
}

func TestRunSupervisorStopActiveRunUsesInteractiveSessionStopBeforeCanceling(t *testing.T) {
	controller := &runSupervisorStoppingController{handled: true, stops: make(chan string, 2)}
	cancelCalls := 0
	supervisor := &RunSupervisor{
		controller: controller,
		active: map[string]*activeRun{
			"run-1": {cancel: func(error) { cancelCalls++ }},
		},
	}
	stopped, err := supervisor.StopActiveRun(context.Background(), "run-1", "audit reason")
	if err != nil || !stopped {
		t.Fatalf("StopActiveRun() stopped=%v err=%v, want true/nil", stopped, err)
	}
	if cancelCalls != 0 {
		t.Fatalf("interactive stop canceled the execution context %d times, want 0", cancelCalls)
	}
	select {
	case got := <-controller.stops:
		if got != "run-1:audit reason" {
			t.Fatalf("stop request = %q, want run-1:audit reason", got)
		}
	default:
		t.Fatal("controller did not receive the interactive stop")
	}
	stopped, err = supervisor.StopActiveRun(context.Background(), "run-1", "second stop")
	if err != nil || !stopped {
		t.Fatalf("second StopActiveRun() stopped=%v err=%v, want true/nil", stopped, err)
	}
	if cancelCalls != 0 {
		t.Fatalf("second stop canceled the execution context %d times, want 0", cancelCalls)
	}
	select {
	case got := <-controller.stops:
		t.Fatalf("second stop reached the controller as %q, want none", got)
	default:
	}
}

func TestRunSupervisorStopActiveRunFallsBackToContextCancelForRunningTurn(t *testing.T) {
	controller := &runSupervisorStoppingController{handled: false, stops: make(chan string, 1)}
	cancelCalls := 0
	supervisor := &RunSupervisor{
		controller: controller,
		active: map[string]*activeRun{
			"run-1": {cancel: func(error) { cancelCalls++ }},
		},
	}
	stopped, err := supervisor.StopActiveRun(context.Background(), "run-1", "turn interrupted")
	if err != nil || !stopped {
		t.Fatalf("StopActiveRun() stopped=%v err=%v, want true/nil", stopped, err)
	}
	if cancelCalls != 1 {
		t.Fatalf("execution context cancellation calls = %d, want 1", cancelCalls)
	}
	select {
	case got := <-controller.stops:
		if got != "run-1:turn interrupted" {
			t.Fatalf("stop request = %q, want run-1:turn interrupted", got)
		}
	default:
		t.Fatal("controller did not receive the running-turn stop")
	}
}
