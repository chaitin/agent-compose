package runhistory

import (
	"context"
	"fmt"

	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type Kind string

const (
	Run          Kind = "run"
	SchedulerRun Kind = "scheduler_run"
)

type Identity struct {
	ProjectID   string
	Kind        Kind
	RunID       string
	SchedulerID string
}

func (id Identity) Validate() error {
	if !canonicalID(id.ProjectID) || !canonicalID(id.RunID) {
		return domain.ClassifyError(domain.ErrInvalidArgument, "project and run ids must be complete resource ids", nil)
	}
	switch id.Kind {
	case Run:
		if id.SchedulerID != "" {
			return domain.ClassifyError(domain.ErrInvalidArgument, "a standalone run must not specify a scheduler id", nil)
		}
	case SchedulerRun:
		if !canonicalID(id.SchedulerID) {
			return domain.ClassifyError(domain.ErrInvalidArgument, "scheduler id must be a complete resource id", nil)
		}
	default:
		return domain.ClassifyError(domain.ErrInvalidArgument, "an explicit run history kind is required", nil)
	}
	return nil
}

func canonicalID(value string) bool {
	hash, err := identity.Hash(value)
	return err == nil && hash == value
}

type Scope struct {
	Runs           []domain.ProjectRunRecord
	SandboxIDs     []string
	SchedulerRun   *domain.SchedulerRunSummary
	ProtectedPaths []string
}

// Store holds a database write transaction from ownership/terminal-state checks
// through artifact removal and deletion of the exact history rows. A failed
// callback or commit retains the records and paths needed for a retry.
type Store interface {
	RemoveRunHistory(context.Context, Identity, func(Scope) error) error
}

type ArtifactRemover interface {
	Remove(context.Context, Scope) error
}

type SchedulerGuard interface {
	WithIdleScheduler(context.Context, string, func(context.Context) error) error
}

type Remover struct {
	Store      Store
	Artifacts  ArtifactRemover
	Schedulers SchedulerGuard
}

func (r *Remover) RemoveRunHistory(ctx context.Context, id Identity) error {
	if err := id.Validate(); err != nil {
		return apierror.NotApplied(err)
	}
	if r == nil || r.Store == nil || r.Artifacts == nil {
		return apierror.NotApplied(domain.ClassifyError(domain.ErrFailedPrecondition, "run history remover is unavailable", nil))
	}
	remove := func(ctx context.Context) error { return r.remove(ctx, id) }
	if id.Kind == SchedulerRun {
		if r.Schedulers == nil {
			return apierror.NotApplied(domain.ClassifyError(domain.ErrFailedPrecondition, "scheduler lifecycle guard is unavailable", nil))
		}
		return r.Schedulers.WithIdleScheduler(ctx, id.SchedulerID, remove)
	}
	return remove(ctx)
}

func (r *Remover) remove(ctx context.Context, id Identity) error {
	return r.Store.RemoveRunHistory(ctx, id, func(scope Scope) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.Artifacts.Remove(ctx, scope); err != nil {
			return fmt.Errorf("remove run history artifacts (retry with the same identity): %w", err)
		}
		return nil
	})
}
