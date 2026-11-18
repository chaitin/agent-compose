package configstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestRunHistoryAcceptsNormalPathsUnderPrefixedRoot(t *testing.T) {
	f := newRunHistoryFixture(t, nil)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), ".history-removing-storage")
	run, err := f.store.GetProjectRun(ctx, f.standalone.RunID)
	if err != nil {
		t.Fatal(err)
	}
	run.LogsPath = filepath.Join(root, "sandboxes", identity.NewID(identity.ResourceSandbox, t.Name()), "state", "cells", "cell", "output.txt")
	run.ArtifactsDir = filepath.Dir(run.LogsPath)
	stored, err := f.store.UpdateProjectRunWithEvents(ctx, run, nil)
	if err != nil || stored.LogsPath != run.LogsPath || stored.ArtifactsDir != run.ArtifactsDir {
		t.Fatalf("normal sandbox paths rejected or changed: %+v, %v", stored, err)
	}
	scheduler, err := f.store.GetSchedulerRun(ctx, f.scheduler.SchedulerID, f.scheduler.RunID)
	if err != nil {
		t.Fatal(err)
	}
	scheduler.ArtifactsDir = filepath.Join(root, "schedulers", scheduler.SchedulerID, "runs", scheduler.ID)
	if err := f.store.UpdateSchedulerRun(ctx, scheduler); err != nil {
		t.Fatalf("normal scheduler path rejected: %v", err)
	}
	got, err := f.store.GetSchedulerRun(ctx, scheduler.SchedulerID, scheduler.ID)
	if err != nil || got.ArtifactsDir != scheduler.ArtifactsDir {
		t.Fatalf("normal scheduler path not persisted: %+v, %v", got, err)
	}
}

func TestRunHistoryWritersRejectReservedPaths(t *testing.T) {
	f := newRunHistoryFixture(t, nil)
	ctx := context.Background()
	original, err := f.store.GetProjectRun(ctx, f.standalone.RunID)
	if err != nil {
		t.Fatal(err)
	}
	writers := []struct {
		name  string
		write func(domain.ProjectRunRecord) error
	}{
		{"create", func(r domain.ProjectRunRecord) error { _, err := f.store.CreateProjectRun(ctx, r); return err }},
		{"create_with_events", func(r domain.ProjectRunRecord) error {
			_, err := f.store.CreateProjectRunWithEvents(ctx, r, nil)
			return err
		}},
		{"update", func(r domain.ProjectRunRecord) error { _, err := f.store.UpdateProjectRun(ctx, r); return err }},
		{"update_with_events", func(r domain.ProjectRunRecord) error {
			_, err := f.store.UpdateProjectRunWithEvents(ctx, r, nil)
			return err
		}},
		{"finalize_completion", func(r domain.ProjectRunRecord) error {
			_, err := f.store.FinalizeProjectRunCompletion(ctx, r, domain.ProjectRunEventRecord{})
			return err
		}},
		{"abandon_completion", func(r domain.ProjectRunRecord) error {
			_, err := f.store.AbandonProjectRunCompletion(ctx, r, domain.ProjectRunEventRecord{}, "cleanup failed")
			return err
		}},
	}
	for _, writer := range writers {
		for _, field := range []string{"logs_path", "artifacts_dir"} {
			t.Run(writer.name+"/"+field, func(t *testing.T) {
				run := original
				path := filepath.Join(t.TempDir(), "schedulers", f.scheduler.SchedulerID, "runs", ".history-removing-"+f.scheduler.RunID, "output.txt")
				if field == "logs_path" {
					run.LogsPath = path
				} else {
					run.ArtifactsDir = path
				}
				if err := writer.write(run); !errors.Is(err, domain.ErrInvalidArgument) {
					t.Fatalf("writer accepted reserved path: %v", err)
				}
			})
		}
	}
	scheduler, err := f.store.GetSchedulerRun(ctx, f.scheduler.SchedulerID, f.scheduler.RunID)
	if err != nil {
		t.Fatal(err)
	}
	scheduler.ArtifactsDir = filepath.Join(t.TempDir(), "schedulers", scheduler.SchedulerID, "runs", ".history-removing-"+scheduler.ID)
	for _, writer := range []func(context.Context, domain.SchedulerRunSummary) error{f.store.CreateSchedulerRun, f.store.UpdateSchedulerRun} {
		if err := writer(ctx, scheduler); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("scheduler writer accepted reserved path: %v", err)
		}
	}
	stored, err := f.store.GetProjectRun(ctx, original.RunID)
	if err != nil || stored.LogsPath != original.LogsPath || stored.ArtifactsDir != original.ArtifactsDir {
		t.Fatalf("rejected writes changed history: %+v, %v", stored, err)
	}
}

func TestRunHistoryWritersRejectPathsInsideAnotherSchedulerRun(t *testing.T) {
	f := newRunHistoryFixture(t, nil)
	ctx := context.Background()
	target := filepath.Join(t.TempDir(), "schedulers", f.scheduler.SchedulerID, "runs", f.scheduler.RunID)
	run, err := f.store.GetProjectRun(ctx, f.standalone.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{target, filepath.Join(target, "private.json")} {
		run.LogsPath = path
		if _, err := f.store.UpdateProjectRun(ctx, run); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("project run may not record a path in a scheduler run: %q, %v", path, err)
		}
	}
	other := domain.SchedulerRunSummary{ID: identity.NewID(identity.ResourceRun, "other", f.scheduler.ProjectID), SchedulerID: f.scheduler.SchedulerID, Status: domain.SchedulerRunStatusSucceeded}
	other.ArtifactsDir = target
	if err := f.store.CreateSchedulerRun(ctx, other); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("scheduler run may not record another run's directory: %v", err)
	}
	other.ArtifactsDir = filepath.Join(filepath.Dir(target), other.ID)
	if err := f.store.CreateSchedulerRun(ctx, other); err != nil {
		t.Fatalf("scheduler run rejected its own directory: %v", err)
	}
}

func TestRunHistoryWritersRejectReservedPathForAnyRunID(t *testing.T) {
	f := newRunHistoryFixture(t, nil)
	ctx := context.Background()
	run, err := f.store.GetProjectRun(ctx, f.standalone.RunID)
	if err != nil {
		t.Fatal(err)
	}
	run.LogsPath = filepath.Join(t.TempDir(), "schedulers", f.scheduler.SchedulerID, "runs", ".history-removing-3f2b8c1e-6a4d-4e8b-9c2a-1d5e7f9a0b3c", "out.txt")
	if _, err := f.store.UpdateProjectRun(ctx, run); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("reserved cleanup name accepted: %v", err)
	}
}
