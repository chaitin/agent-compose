package app

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
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
