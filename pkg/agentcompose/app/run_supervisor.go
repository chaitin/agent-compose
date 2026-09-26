package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/samber/do/v2"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
)

type RunSupervisor struct {
	root       context.Context
	controller runSupervisorController
	store      *configstore.ConfigStore

	mu     sync.Mutex
	active map[string]*activeRun
	wg     sync.WaitGroup
}

type runSupervisorController interface {
	StartProjectRun(context.Context, runs.RunAgentRequest) (runs.StartedProjectRun, error)
	RunProjectCommandAttachRegistered(context.Context, context.Context, runs.RunAttachReceiver, runs.RunAttachSender, func(string, <-chan struct{})) error
}

type activeRun struct {
	cancel   context.CancelCauseFunc
	stopOnce sync.Once
	stopping bool
	stopped  bool
	stopErr  error
}

func NewRunSupervisor(di do.Injector) (*RunSupervisor, error) {
	return &RunSupervisor{
		root:       do.MustInvoke[context.Context](di),
		controller: do.MustInvoke[*runs.Controller](di),
		store:      do.MustInvoke[*configstore.ConfigStore](di),
		active:     map[string]*activeRun{},
	}, nil
}

// detachedRunContext is the parent of a run that outlives the request starting
// it. The run's lifetime belongs to the daemon root, not to the request, but
// the request's trusted ingress headers must still reach sandbox preparation,
// where they become the sandbox's capability binding. Only that metadata is
// carried over, not the transport context, matching how StartProjectRun's
// asynchronous Execute restores it (runs.Controller.StartProjectRun).
func (s *RunSupervisor) detachedRunContext(request context.Context) context.Context {
	return domain.NewContextWithTrustedHeaders(s.root, domain.TrustedHeadersFromContext(request))
}

func (s *RunSupervisor) StartRun(ctx context.Context, req runs.RunAgentRequest) (domain.ProjectRunRecord, error) {
	if req.Interactive {
		return s.startInteractiveRun(ctx, req)
	}
	started, err := s.controller.StartProjectRun(ctx, req)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if runs.StatusIsTerminal(started.Run.Status) {
		return started.Run, nil
	}
	execCtx, cancel := context.WithCancelCause(s.root)
	s.register(started.Run.RunID, cancel)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.unregister(started.Run.RunID)
		_, _, _ = started.Execute(execCtx, nil)
	}()
	return started.Run, nil
}

func (s *RunSupervisor) startInteractiveRun(ctx context.Context, req runs.RunAgentRequest) (domain.ProjectRunRecord, error) {
	execCtx, cancel := context.WithCancelCause(s.detachedRunContext(ctx))
	type interactiveRunStarted struct {
		runID         string
		inputReleased <-chan struct{}
	}
	started := make(chan interactiveRunStarted, 1)
	failed := make(chan error, 1)
	first := true
	receive := func() (runs.RunAttachInput, error) {
		if first {
			first = false
			return runs.RunAttachInput{Kind: runs.RunAttachInputStart, Mode: runs.RunAttachModePrompt, Request: req, DisconnectPolicy: runs.AttachDisconnectDetach}, nil
		}
		return runs.RunAttachInput{}, io.EOF
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel(nil)
		var runID string
		err := s.controller.RunProjectCommandAttachRegistered(execCtx, execCtx, receive, func(runs.RunAttachOutput) error { return nil }, func(id string, inputReleased <-chan struct{}) {
			runID = id
			s.register(id, cancel)
			started <- interactiveRunStarted{runID: id, inputReleased: inputReleased}
		})
		if runID != "" {
			s.unregister(runID)
		}
		failed <- err
	}()
	select {
	case run := <-started:
		select {
		case <-run.inputReleased:
			return s.store.GetProjectRun(ctx, run.runID)
		case <-ctx.Done():
			return domain.ProjectRunRecord{}, ctx.Err()
		}
	case err := <-failed:
		return domain.ProjectRunRecord{}, err
	case <-ctx.Done():
		return domain.ProjectRunRecord{}, ctx.Err()
	}
}

func (s *RunSupervisor) Run(ctx context.Context, req runs.RunAgentRequest, stream *runs.StreamSink) (domain.ProjectRunRecord, error, error) {
	started, err := s.controller.StartProjectRun(ctx, req)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	if runs.StatusIsTerminal(started.Run.Status) {
		return started.Run, nil, nil
	}
	execCtx, cancel := context.WithCancelCause(ctx)
	s.register(started.Run.RunID, cancel)
	s.wg.Add(1)
	defer s.wg.Done()
	defer s.unregister(started.Run.RunID)
	return started.Execute(execCtx, stream)
}

func (s *RunSupervisor) Attach(ctx context.Context, receive runs.RunAttachReceiver, send runs.RunAttachSender) error {
	first, err := receive()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: run attach start frame is required", runs.ErrInvalidRequest)
		}
		return err
	}
	replayed := false
	receiveWithFirst := func() (runs.RunAttachInput, error) {
		if !replayed {
			replayed = true
			return first, nil
		}
		return receive()
	}
	parent := ctx
	if first.DisconnectPolicy == runs.AttachDisconnectDetach && first.RunID == "" {
		parent = s.detachedRunContext(ctx)
	}
	execCtx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	var runID string
	err = s.controller.RunProjectCommandAttachRegistered(execCtx, ctx, receiveWithFirst, send, func(startedRunID string, _ <-chan struct{}) {
		runID = startedRunID
		s.register(runID, cancel)
		s.wg.Add(1)
	})
	if runID != "" {
		s.wg.Done()
		s.unregister(runID)
	}
	return err
}

func (s *RunSupervisor) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *RunSupervisor) StopActiveRun(ctx context.Context, runID, reason string) (bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false, nil
	}
	s.mu.Lock()
	active, ok := s.active[runID]
	if ok {
		active.stopping = true
	}
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	active.stopOnce.Do(func() {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			reason = "stop requested"
		}
		active.cancel(errors.New(reason))
		active.stopped = true
	})
	return active.stopped, active.stopErr
}

func (s *RunSupervisor) register(runID string, cancel context.CancelCauseFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[runID] = &activeRun{cancel: cancel}
}

func (s *RunSupervisor) unregister(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, runID)
}
