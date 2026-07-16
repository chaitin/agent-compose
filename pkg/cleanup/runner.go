package cleanup

import (
	"context"
	"log/slog"
	"time"
)

type Result struct {
	Matched int
	Removed int
	Skipped int
	Failed  int
}

type Cleaner interface {
	Name() string
	Clean(context.Context, time.Time) (Result, error)
}

type Policy struct {
	TTL     time.Duration
	Cleaner Cleaner
}

type Runner struct {
	Interval time.Duration
	Policies []Policy
	Now      func() time.Time
	Logger   *slog.Logger
}

func (r *Runner) Enabled() bool {
	if r == nil || r.Interval <= 0 {
		return false
	}
	for _, policy := range r.Policies {
		if policy.TTL > 0 && policy.Cleaner != nil {
			return true
		}
	}
	return false
}

func (r *Runner) Start(ctx context.Context) {
	if !r.Enabled() {
		return
	}
	go r.run(ctx)
}

func (r *Runner) run(ctx context.Context) {
	r.runOnce(ctx)
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

func (r *Runner) runOnce(ctx context.Context) {
	now := r.now()
	for _, policy := range r.Policies {
		if policy.TTL <= 0 || policy.Cleaner == nil || ctx.Err() != nil {
			continue
		}
		cutoff := now.Add(-policy.TTL)
		result, err := policy.Cleaner.Clean(ctx, cutoff)
		logger := r.Logger
		if logger == nil {
			logger = slog.Default()
		}
		attrs := []any{
			"cleaner", policy.Cleaner.Name(), "cutoff", cutoff,
			"matched", result.Matched, "removed", result.Removed,
			"skipped", result.Skipped, "failed", result.Failed,
		}
		if err != nil {
			attrs = append(attrs, "error", err)
			logger.Warn("automatic cleanup completed with errors", attrs...)
			continue
		}
		logger.Info("automatic cleanup completed", attrs...)
	}
}

func (r *Runner) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
