package dashboard

import (
	driverpkg "agent-compose/internal/driver"
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestDashboardOverviewAggregatorCountsRuns(t *testing.T) {
	testDashboardOverviewAggregatorCountsRuns(t)
}

func testDashboardOverviewAggregatorCountsRuns(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)

	running, err := service.store.CreateSession(ctx, "running", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession running returned error: %v", err)
	}
	running.Summary.VMStatus = VMStatusRunning
	if err := service.store.UpdateSession(ctx, running); err != nil {
		t.Fatalf("UpdateSession running returned error: %v", err)
	}
	failed, err := service.store.CreateSession(ctx, "failed", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession failed returned error: %v", err)
	}
	failed.Summary.VMStatus = VMStatusFailed
	if err := service.store.UpdateSession(ctx, failed); err != nil {
		t.Fatalf("UpdateSession failed returned error: %v", err)
	}

	now := time.Now().UTC()
	loader, err := service.configDB.CreateLoader(ctx, Loader{
		Summary: LoaderSummary{ID: "loader-a", Name: "loader a", Runtime: LoaderRuntimeScheduler, Enabled: true},
		Script:  "export default {}",
	})
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	for _, run := range []LoaderRunSummary{
		{ID: "run-running", LoaderID: loader.Summary.ID, Status: LoaderRunStatusRunning, StartedAt: now},
		{ID: "run-skipped", LoaderID: loader.Summary.ID, Status: LoaderRunStatusSkipped, StartedAt: now.Add(-time.Second)},
	} {
		if err := service.configDB.CreateLoaderRun(ctx, run); err != nil {
			t.Fatalf("CreateLoaderRun %s returned error: %v", run.ID, err)
		}
	}

	overview, err := service.dashboard.Current(ctx)
	if err != nil {
		t.Fatalf("Current returned error: %v", err)
	}
	if got, want := overview.GetRuns().GetRunningCount(), uint32(2); got != want {
		t.Fatalf("running count = %d, want %d", got, want)
	}
	if got, want := overview.GetRuns().GetRecentCount(), uint32(4); got != want {
		t.Fatalf("recent count = %d, want %d", got, want)
	}
	if got, want := overview.GetRuns().GetAttentionCount(), uint32(2); got != want {
		t.Fatalf("attention count = %d, want %d", got, want)
	}
	resp, err := service.GetDashboardOverview(ctx, connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("GetDashboardOverview returned error: %v", err)
	}
	if got, want := resp.Msg.GetOverview().GetRuns().GetRunningCount(), uint32(2); got != want {
		t.Fatalf("service running count = %d, want %d", got, want)
	}
}

func TestDashboardOverviewHubWatchInitialAndNotify(t *testing.T) {
	testDashboardOverviewHubWatchInitialAndNotify(t)
}

func testDashboardOverviewHubWatchInitialAndNotify(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, _, _ := newTestServiceAPIHarness(t)
	service.dashboard.debounce = time.Millisecond

	events, unsubscribe := service.dashboard.Watch(ctx)
	defer unsubscribe()
	initial, err := service.dashboard.Current(ctx)
	if err != nil {
		t.Fatalf("Current returned error: %v", err)
	}
	if initial.GetUpdatedAt() == "" {
		t.Fatalf("initial overview missing updated_at")
	}

	service.dashboard.Notify("test_notify")
	select {
	case event := <-events:
		if event.Reason != "test_notify" {
			t.Fatalf("event reason = %q, want test_notify", event.Reason)
		}
		if event.Overview.GetUpdatedAt() == "" {
			t.Fatalf("event overview missing updated_at")
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for dashboard event")
	}
}
