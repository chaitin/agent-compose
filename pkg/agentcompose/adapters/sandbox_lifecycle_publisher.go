package adapters

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

// publishSandboxLifecycle raises a sandbox lifecycle topic on the scheduler
// bus, attributed to the Project it is delivered within.
func (b *SandboxRPCBridge) publishSandboxLifecycle(ctx context.Context, topic string, sandbox *domain.Sandbox, source string) {
	if b == nil || b.bus == nil {
		return
	}
	b.bus.Publish(domain.SchedulerTopicEvent{
		Topic:              topic,
		PublisherProjectID: b.sandboxLifecyclePublisherProject(ctx, sandbox),
		Payload:            schedulers.SessionTopicPayload(sandbox, source),
		CreatedAt:          time.Now().UTC(),
	})
}

// sandboxLifecyclePublisherProject decides which Project a sandbox lifecycle
// topic belongs to.
//
// A scheduler script acting through the bridge speaks for its own Project,
// which its run context carries; that always wins, so a script cannot make its
// action count as another Project's. An operator action through the Sandbox
// API carries no Project, so the topic goes to the Project that owns the
// sandbox. Ownership comes only from facts the daemon wrote: the scheduler in
// a script sandbox's trigger source, or the project tag the daemon writes on a
// project-run sandbox. A script sandbox's tags are never consulted, because a
// script chooses them when it creates the sandbox.
func (b *SandboxRPCBridge) sandboxLifecyclePublisherProject(ctx context.Context, sandbox *domain.Sandbox) string {
	if projectID := events.PublisherProject(ctx); projectID != "" {
		return projectID
	}
	if sandbox == nil {
		return ""
	}
	if schedulerID, ok := strings.CutPrefix(strings.TrimSpace(sandbox.Summary.TriggerSource), domain.SandboxTypeScript+":"); ok {
		return b.schedulerProject(ctx, schedulerID)
	}
	return capabilities.GuideScopeFromSandbox(sandbox).ProjectID
}

// schedulerProject returns the Project that owns schedulerID, or empty when
// the scheduler is gone or belongs to no Project.
func (b *SandboxRPCBridge) schedulerProject(ctx context.Context, schedulerID string) string {
	schedulerID = strings.TrimSpace(schedulerID)
	if schedulerID == "" || b.configDB == nil {
		return ""
	}
	scheduler, err := b.configDB.GetScheduler(ctx, schedulerID)
	if err != nil {
		// Either way the topic is attributed to no Project, which keeps it
		// from reaching another Project's subscribers. A deleted scheduler is
		// expected; any other failure is worth a warning.
		if !errors.Is(err, domain.ErrNotFound) {
			slog.Warn("failed to resolve sandbox owner project for lifecycle event", "scheduler_id", schedulerID, "error", err)
		}
		return ""
	}
	return strings.TrimSpace(scheduler.Summary.ProjectID)
}
