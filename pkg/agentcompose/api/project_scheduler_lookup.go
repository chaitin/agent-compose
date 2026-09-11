package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type projectSchedulerByIDStore interface {
	GetProjectSchedulerByID(context.Context, string) (model.ProjectSchedulerRecord, error)
}

func (h *ProjectHandler) resolveGetScheduler(ctx context.Context, request *agentcomposev2.GetSchedulerRequest) (model.ProjectSchedulerRecord, error) {
	schedulerID := strings.TrimSpace(request.GetSchedulerId())
	if schedulerID == "" {
		_, scheduler, err := h.resolveProjectScheduler(ctx, request.GetProject(), request.GetAgentName())
		return scheduler, err
	}
	if request.GetProject() != nil || strings.TrimSpace(request.GetAgentName()) != "" {
		return model.ProjectSchedulerRecord{}, model.ClassifyError(model.ErrAmbiguous, "provide scheduler_id or project and agent_name, not both", nil)
	}
	store, ok := h.store.(projectSchedulerByIDStore)
	if !ok {
		return model.ProjectSchedulerRecord{}, fmt.Errorf("scheduler ID lookup store is required")
	}
	return store.GetProjectSchedulerByID(ctx, schedulerID)
}
