package agentcompose

import (
	"agent-compose/pkg/agentcompose/images"
	"context"
)

type driverImageEnsureRequest = images.EnsureRequest

func (s *Service) ensureProjectAgentImages(ctx context.Context, projectName string, agents []ProjectAgentRecord) error {
	if s == nil {
		return images.EnsureProjectAgentImages(ctx, nil, nil, projectName, agents)
	}
	return images.EnsureProjectAgentImages(ctx, s.config, s.images, projectName, agents)
}

func (s *Service) ensureDriverImage(ctx context.Context, req driverImageEnsureRequest) error {
	if s == nil {
		return images.EnsureDriverImage(ctx, nil, nil, req)
	}
	return images.EnsureDriverImage(ctx, s.config, s.images, req)
}
