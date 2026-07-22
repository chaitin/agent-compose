package configstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

func TestLoaderRunPageUsesStableCrossLoaderCursor(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	for _, loaderID := range []string{"loader-a", "loader-b"} {
		if _, err := store.UpsertManagedLoader(ctx, domain.Loader{
			Summary: domain.LoaderSummary{
				ID:                 loaderID,
				Name:               loaderID,
				Runtime:            domain.LoaderRuntimeScheduler,
				ManagedProjectID:   "project-1",
				ManagedAgentName:   loaderID,
				ManagedSchedulerID: "scheduler-" + loaderID,
			},
			Script: "function main() {}",
		}); err != nil {
			t.Fatalf("upsert loader %s: %v", loaderID, err)
		}
	}
	newer := time.UnixMilli(1_720_000_000_500).UTC()
	older := newer.Add(-time.Second)
	for _, run := range []domain.LoaderRunSummary{
		{ID: "run-a", LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusSucceeded, StartedAt: newer},
		{ID: "run-b1", LoaderID: "loader-b", TriggerID: "trigger-b", Status: domain.LoaderRunStatusSucceeded, StartedAt: newer},
		{ID: "run-b2", LoaderID: "loader-b", TriggerID: "trigger-b", Status: domain.LoaderRunStatusSucceeded, StartedAt: newer},
		{ID: "run-old", LoaderID: "loader-b", TriggerID: "trigger-b", Status: domain.LoaderRunStatusSucceeded, StartedAt: older},
	} {
		if err := store.CreateLoaderRun(ctx, run); err != nil {
			t.Fatalf("create run %s: %v", run.ID, err)
		}
	}

	first, err := store.ListLoaderRunsPage(ctx, loaders.LoaderRunPageFilter{
		LoaderIDs: []string{" loader-a ", "loader-b", "loader-a"},
		Limit:     2,
	})
	if err != nil || len(first) != 2 || first[0].ID != "run-b2" || first[1].ID != "run-b1" {
		t.Fatalf("first page=%#v err=%v", first, err)
	}
	second, err := store.ListLoaderRunsPage(ctx, loaders.LoaderRunPageFilter{
		LoaderIDs:       []string{"loader-a", "loader-b"},
		BeforeStartedAt: first[1].StartedAt,
		BeforeLoaderID:  first[1].LoaderID,
		BeforeRunID:     first[1].ID,
		Limit:           2,
	})
	if err != nil || len(second) != 2 || second[0].ID != "run-a" || second[1].ID != "run-old" {
		t.Fatalf("second page=%#v err=%v", second, err)
	}
	filtered, err := store.ListLoaderRunsPage(ctx, loaders.LoaderRunPageFilter{LoaderIDs: []string{"loader-a"}, Limit: 10})
	if err != nil || len(filtered) != 1 || filtered[0].ID != "run-a" {
		t.Fatalf("filtered page=%#v err=%v", filtered, err)
	}
	byID, err := store.GetLoaderRunForLoaders(ctx, []string{"loader-b"}, "run-old")
	if err != nil || byID.LoaderID != "loader-b" {
		t.Fatalf("GetLoaderRunForLoaders run=%#v err=%v", byID, err)
	}
	if _, err := store.GetLoaderRunForLoaders(ctx, []string{"loader-a"}, "run-old"); err == nil {
		t.Fatal("GetLoaderRunForLoaders accepted a run from another loader")
	}
	if _, err := store.GetLoaderRunForLoaders(ctx, nil, "missing"); err == nil {
		t.Fatal("GetLoaderRunForLoaders missing returned nil error")
	}
}

func TestGetLoaderRunForLoadersResolvesTriggerRunShortIDs(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.UpsertManagedLoader(ctx, domain.Loader{Summary: domain.LoaderSummary{ID: "loader-a", Runtime: domain.LoaderRuntimeScheduler, ManagedProjectID: "project-1", ManagedAgentName: "agent-1", ManagedSchedulerID: "scheduler-1"}, Script: "function main() {}"}); err != nil {
		t.Fatalf("upsert loader: %v", err)
	}
	prefix := "abcdef123456"
	firstID := prefix + strings.Repeat("1", 52)
	secondID := prefix + strings.Repeat("2", 52)
	for _, run := range []domain.LoaderRunSummary{
		{ID: firstID, LoaderID: "loader-a", TriggerID: "trigger-1", Status: domain.LoaderRunStatusSucceeded, StartedAt: time.Now().UTC()},
		{ID: secondID, LoaderID: "loader-a", Status: domain.LoaderRunStatusSucceeded, StartedAt: time.Now().UTC()},
	} {
		if err := store.CreateLoaderRun(ctx, run); err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
	resolved, err := store.GetLoaderRunForLoaders(ctx, []string{"loader-a"}, prefix)
	if err != nil || resolved.ID != firstID {
		t.Fatalf("resolved run=%#v err=%v", resolved, err)
	}
	if err := store.CreateLoaderRun(ctx, domain.LoaderRunSummary{ID: secondID, LoaderID: "loader-a", TriggerID: "trigger-2", Status: domain.LoaderRunStatusSucceeded, StartedAt: time.Now().UTC()}); err == nil {
		t.Fatal("expected duplicate run update setup to fail")
	}
	thirdID := prefix + strings.Repeat("3", 52)
	if err := store.CreateLoaderRun(ctx, domain.LoaderRunSummary{ID: thirdID, LoaderID: "loader-a", TriggerID: "trigger-2", Status: domain.LoaderRunStatusSucceeded, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("create ambiguous run: %v", err)
	}
	if _, err := store.GetLoaderRunForLoaders(ctx, []string{"loader-a"}, prefix); !errors.Is(err, domain.ErrAmbiguous) {
		t.Fatalf("ambiguous short id error=%v", err)
	}
}

func TestLoaderRunPageFiltersTriggerRunsBeforeLimitAndBatchesSandboxes(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.UpsertManagedLoader(ctx, domain.Loader{Summary: domain.LoaderSummary{ID: "loader-a", Runtime: domain.LoaderRuntimeScheduler, ManagedProjectID: "project-1", ManagedAgentName: "agent-1", ManagedSchedulerID: "scheduler-1"}, Script: "function main() {}"}); err != nil {
		t.Fatalf("upsert loader: %v", err)
	}
	startedAt := time.UnixMilli(1_720_000_000_000).UTC()
	for _, run := range []domain.LoaderRunSummary{
		{ID: "invoke-newest", LoaderID: "loader-a", Status: domain.LoaderRunStatusSucceeded, StartedAt: startedAt.Add(3 * time.Second)},
		{ID: "run-failed", LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusFailed, StartedAt: startedAt.Add(2 * time.Second)},
		{ID: "run-success", LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusSucceeded, StartedAt: startedAt.Add(time.Second)},
		{ID: "run-other", LoaderID: "loader-a", TriggerID: "trigger-b", Status: domain.LoaderRunStatusSucceeded, StartedAt: startedAt},
	} {
		if err := store.CreateLoaderRun(ctx, run); err != nil {
			t.Fatalf("create run %s: %v", run.ID, err)
		}
	}
	filtered, err := store.ListLoaderRunsPage(ctx, loaders.LoaderRunPageFilter{
		LoaderIDs: []string{"loader-a"}, RequireTrigger: true, TriggerID: "trigger-a", Status: domain.LoaderRunStatusSucceeded, Limit: 1,
	})
	if err != nil || len(filtered) != 1 || filtered[0].ID != "run-success" {
		t.Fatalf("filtered runs=%#v err=%v", filtered, err)
	}
	for index, sandboxID := range []string{"sandbox-b", "sandbox-a", "sandbox-a"} {
		if err := store.AddLoaderEvent(ctx, domain.LoaderEvent{LoaderID: "loader-a", ID: fmt.Sprintf("event-%d", index), RunID: "run-success", TriggerID: "trigger-a", Type: "loader.test", LinkedSandboxID: sandboxID, CreatedAt: startedAt}); err != nil {
			t.Fatalf("add event: %v", err)
		}
	}
	sandboxes, err := store.ListLoaderRunSandboxIDs(ctx, []loaders.LoaderRunKey{{LoaderID: "loader-a", RunID: "run-success"}, {LoaderID: "loader-a", RunID: "run-success"}})
	if err != nil || !reflect.DeepEqual(sandboxes[loaders.LoaderRunKey{LoaderID: "loader-a", RunID: "run-success"}], []string{"sandbox-a", "sandbox-b"}) {
		t.Fatalf("sandbox ids=%#v err=%v", sandboxes, err)
	}
}
