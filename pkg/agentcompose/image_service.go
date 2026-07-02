package agentcompose

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"agent-compose/pkg/images"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func (s *Service) imageService() *images.Service {
	if s == nil {
		return nil
	}
	if s.imageHandlers != nil {
		return s.imageHandlers
	}
	s.imageHandlers = images.NewService(s.images, s.ociImages, s.autoImages)
	return s.imageHandlers
}

func (s *Service) ListImages(ctx context.Context, req *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error) {
	imageService := s.imageService()
	if imageService == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("image service is required"))
	}
	return imageService.ListImages(ctx, req)
}

func (s *Service) PullImage(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
	imageService := s.imageService()
	if imageService == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("image service is required"))
	}
	return imageService.PullImage(ctx, req)
}

func (s *Service) InspectImage(ctx context.Context, req *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error) {
	imageService := s.imageService()
	if imageService == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("image service is required"))
	}
	return imageService.InspectImage(ctx, req)
}

func (s *Service) RemoveImage(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
	imageService := s.imageService()
	if imageService == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("image service is required"))
	}
	return imageService.RemoveImage(ctx, req)
}
