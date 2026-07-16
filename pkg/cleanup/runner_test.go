package cleanup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestRunnerRunOnceUsesIndependentPolicyCutoffs(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	first := &recordingCleaner{name: "workspace"}
	second := &recordingCleaner{name: "image", err: errors.New("failed")}
	runner := &Runner{
		Interval: time.Hour,
		Now:      func() time.Time { return now },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Policies: []Policy{{TTL: 24 * time.Hour, Cleaner: first}, {TTL: 48 * time.Hour, Cleaner: second}, {TTL: 0, Cleaner: &recordingCleaner{name: "disabled"}}},
	}
	runner.runOnce(context.Background())
	if len(first.cutoffs) != 1 || !first.cutoffs[0].Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("workspace cutoffs = %v", first.cutoffs)
	}
	if len(second.cutoffs) != 1 || !second.cutoffs[0].Equal(now.Add(-48*time.Hour)) {
		t.Fatalf("image cutoffs = %v", second.cutoffs)
	}
}

func TestRunnerDisabledWithoutPositivePolicy(t *testing.T) {
	if (&Runner{Interval: time.Hour, Policies: []Policy{{TTL: 0, Cleaner: &recordingCleaner{}}}}).Enabled() {
		t.Fatal("runner with zero TTL is enabled")
	}
	if (&Runner{Interval: 0, Policies: []Policy{{TTL: time.Hour, Cleaner: &recordingCleaner{}}}}).Enabled() {
		t.Fatal("runner with zero interval is enabled")
	}
}

type recordingCleaner struct {
	name    string
	err     error
	cutoffs []time.Time
}

func (c *recordingCleaner) Name() string { return c.name }
func (c *recordingCleaner) Clean(_ context.Context, cutoff time.Time) (Result, error) {
	c.cutoffs = append(c.cutoffs, cutoff)
	return Result{Matched: 1}, c.err
}
