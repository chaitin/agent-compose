package agentcompose

import (
	"context"

	"agent-compose/pkg/images"
)

type driverImageEnsureRequest = images.DriverImageEnsureRequest

func (s *Service) ensureDriverImage(ctx context.Context, req driverImageEnsureRequest) error {
	if s == nil {
		return images.EnsureDriverImage(ctx, nil, nil, images.DriverImageEnsureRequest(req))
	}
	return images.EnsureDriverImage(ctx, s.config, s.images, images.DriverImageEnsureRequest(req))
}
