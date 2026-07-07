package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestAggregatorAndHubWorkflows(t *testing.T) {
	ctx := context.Background()
	store := dashboardSessionStore{sessions: []*domain.Session{
		{Summary: domain.SessionSummary{ID: "pending", VMStatus: domain.VMStatusPending}},
		{Summary: domain.SessionSummary{ID: "failed", VMStatus: domain.VMStatusFailed}},
	}}
	runs := dashboardRunStore{runs: []domain.LoaderRunSummary{{ID: "run", Status: "running"}, {ID: "skip", Status: "skipped"}}}
	aggregator := NewAggregator(store, runs)
	aggregator.SetClock(func() time.Time { return time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC) })
	overview, err := aggregator.Build(ctx)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if overview.GetRuns().GetRecentCount() != 4 || overview.GetRuns().GetRunningCount() != 2 || overview.GetRuns().GetAttentionCount() != 2 {
		t.Fatalf("overview = %#v", overview.GetRuns())
	}
	if CloneOverview(nil) != nil || !IsRunningStatus(" pending ") || !IsAttentionStatus("cancelled") {
		t.Fatalf("status helpers failed")
	}

	hub := NewHub(ctx, aggregator, time.Millisecond)
	defer hub.cancel()
	hub.SetDebounce(0)
	hub.SetDebounce(time.Millisecond)
	var nilHub *Hub
	nilHub.SetDebounce(time.Millisecond)
	current, err := hub.Current(ctx)
	if err != nil || current.GetUpdatedAt() == "" {
		t.Fatalf("Current overview=%#v err=%v", current, err)
	}
	watchCtx, cancelWatch := context.WithCancel(ctx)
	ch, cancel := hub.Watch(watchCtx)
	hub.Notify("")
	select {
	case event := <-ch:
		if event.Reason != "updated" || event.Overview.GetRuns().GetRecentCount() != 4 {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for dashboard event")
	}
	cancel()
	cancelWatch()

	shutdownCtx, shutdownCancel := context.WithCancel(ctx)
	shutdownHub := NewHub(shutdownCtx, aggregator, time.Millisecond)
	shutdownCh, _ := shutdownHub.Watch(context.Background())
	shutdownCancel()
	select {
	case _, ok := <-shutdownCh:
		if ok {
			t.Fatalf("shutdown subscriber channel remained open")
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for shutdown subscriber close")
	}
}

func TestAggregatorReturnsStoreErrors(t *testing.T) {
	_, err := NewAggregator(dashboardSessionStore{err: errors.New("sessions")}, dashboardRunStore{}).Build(context.Background())
	if err == nil {
		t.Fatalf("expected session store error")
	}
	_, err = NewAggregator(dashboardSessionStore{}, dashboardRunStore{err: errors.New("runs")}).Build(context.Background())
	if err == nil {
		t.Fatalf("expected run store error")
	}
}

func TestIntegrationDashboardOverviewWorkflows(t *testing.T) {
	TestAggregatorAndHubWorkflows(t)
	TestAggregatorReturnsStoreErrors(t)
}

func TestE2EDashboardOverviewWorkflows(t *testing.T) {
	TestIntegrationDashboardOverviewWorkflows(t)
}

type dashboardSessionStore struct {
	sessions []*domain.Session
	err      error
}

func (s dashboardSessionStore) ListSessions(context.Context, domain.SessionListOptions) (domain.SessionListResult, error) {
	return domain.SessionListResult{Sessions: s.sessions}, s.err
}

type dashboardRunStore struct {
	runs []domain.LoaderRunSummary
	err  error
}

func (s dashboardRunStore) ListRecentLoaderRuns(context.Context, int) ([]domain.LoaderRunSummary, error) {
	return s.runs, s.err
}
