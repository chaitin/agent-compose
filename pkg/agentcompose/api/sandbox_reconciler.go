package api

import (
	"context"

	domain "agent-compose/pkg/model"
)

type SessionRuntimeReconciler interface {
	ReconcileRuntimeState(context.Context, *domain.Sandbox) (*domain.Sandbox, error)
}
