package api

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// projectApplyTrustedHeadersStore reads the identity that cron and event
// scheduler runs of a Project act as.
type projectApplyTrustedHeadersStore interface {
	ProjectApplyTrustedHeaders(context.Context, string) ([]domain.TrustedHeader, error)
}

func (h *ProjectHandler) schedulerRunTrustedHeaders(ctx context.Context, projectID string) ([]*agentcomposev2.TrustedHeader, error) {
	store, ok := h.store.(projectApplyTrustedHeadersStore)
	if !ok || projectID == "" {
		return nil, nil
	}
	headers, err := store.ProjectApplyTrustedHeaders(ctx, projectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load scheduler run trusted headers: %w", err))
	}
	out := make([]*agentcomposev2.TrustedHeader, 0, len(headers))
	for _, header := range headers {
		out = append(out, &agentcomposev2.TrustedHeader{Name: header.Name, Value: header.Value})
	}
	return out, nil
}
