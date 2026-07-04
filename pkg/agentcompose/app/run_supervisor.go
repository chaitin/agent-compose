package app

import (
	"context"
	"strings"
	"sync"

	"github.com/samber/do/v2"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	"agent-compose/pkg/storage/configstore"
)

type RunSupervisor struct {
	root       context.Context
	controller *runs.Controller
	store      *configstore.ConfigStore

	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func NewRunSupervisor(di do.Injector) (*RunSupervisor, error) {
	return &RunSupervisor{
		root:       do.MustInvoke[context.Context](di),
		controller: do.MustInvoke[*runs.Controller](di),
		store:      do.MustInvoke[*configstore.ConfigStore](di),
		active:     map[string]context.CancelFunc{},
	}, nil
}

func (s *RunSupervisor) StartRun(ctx context.Context, req runs.RunAgentRequest) (domain.ProjectRunRecord, error) {
	started, err := s.controller.StartProjectRun(ctx, req)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if runs.StatusIsTerminal(started.Run.Status) {
		return started.Run, nil
	}
	execCtx, cancel := context.WithCancel(s.root)
	s.register(started.Run.RunID, cancel)
	go func() {
		defer s.unregister(started.Run.RunID)
		_, _, _ = started.Execute(execCtx, nil)
	}()
	return started.Run, nil
}

func (s *RunSupervisor) StopActiveRun(ctx context.Context, runID, reason string) (bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false, nil
	}
	s.mu.Lock()
	cancel, ok := s.active[runID]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	cancel()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "stop requested"
	}
	coordinator := runs.NewCoordinator(s.store, domain.StableProjectRunID)
	if _, err := coordinator.MarkCanceled(ctx, runs.TransitionRequest{RunID: runID, Error: reason}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *RunSupervisor) register(runID string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[runID] = cancel
}

func (s *RunSupervisor) unregister(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, runID)
}
