package projects

import (
	"context"
	"errors"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func TestDisableProjectSchedulersAfterReconcileFailureFailsClosed(t *testing.T) {
	store := &reconcileFailureStore{items: []domain.ProjectSchedulerRecord{
		{ProjectID: "project-a", SchedulerID: "enabled-a", Enabled: true},
		{ProjectID: "project-a", SchedulerID: "disabled", Enabled: false},
		{ProjectID: "project-a", SchedulerID: "enabled-b", Enabled: true},
	}}
	validator := &reconcileFailureValidator{}
	controller := &Controller{store: store, schedulers: validator}

	if err := controller.disableProjectSchedulersAfterReconcileFailure(context.Background(), "project-a"); err != nil {
		t.Fatalf("disableProjectSchedulersAfterReconcileFailure returned error: %v", err)
	}
	if len(store.disabled) != 2 || store.disabled[0] != "enabled-a" || store.disabled[1] != "enabled-b" {
		t.Fatalf("disabled schedulers = %v, want [enabled-a enabled-b]", store.disabled)
	}
	if validator.refreshes != 1 {
		t.Fatalf("scheduler refreshes = %d, want 1", validator.refreshes)
	}
}

func TestDisableProjectSchedulersAfterReconcileFailureReportsCompensationErrors(t *testing.T) {
	store := &reconcileFailureStore{
		items:      []domain.ProjectSchedulerRecord{{ProjectID: "project-a", SchedulerID: "enabled", Enabled: true}},
		disableErr: errors.New("disable failed"),
	}
	validator := &reconcileFailureValidator{err: errors.New("refresh failed")}
	controller := &Controller{store: store, schedulers: validator}

	err := controller.disableProjectSchedulersAfterReconcileFailure(context.Background(), "project-a")
	if err == nil || !errors.Is(err, store.disableErr) || !errors.Is(err, validator.err) {
		t.Fatalf("compensation error = %v, want joined disable and refresh errors", err)
	}
}

type reconcileFailureStore struct {
	ControllerStore
	items      []domain.ProjectSchedulerRecord
	disabled   []string
	disableErr error
}

func (s *reconcileFailureStore) ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error) {
	return append([]domain.ProjectSchedulerRecord(nil), s.items...), nil
}

func (s *reconcileFailureStore) SetProjectSchedulerEnabled(_ context.Context, _, schedulerID string, enabled bool) (domain.ProjectSchedulerRecord, error) {
	if s.disableErr != nil {
		return domain.ProjectSchedulerRecord{}, s.disableErr
	}
	if !enabled {
		s.disabled = append(s.disabled, schedulerID)
	}
	return domain.ProjectSchedulerRecord{SchedulerID: schedulerID, Enabled: enabled}, nil
}

type reconcileFailureValidator struct {
	refreshes int
	err       error
}

func (*reconcileFailureValidator) Validate(context.Context, string, string) (schedulers.SchedulerValidationResult, error) {
	return schedulers.SchedulerValidationResult{}, nil
}

func (v *reconcileFailureValidator) Refresh(context.Context) error {
	v.refreshes++
	return v.err
}
