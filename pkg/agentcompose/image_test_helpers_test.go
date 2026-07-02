package agentcompose

import (
	"agent-compose/pkg/images"
	"context"
	"errors"
)

type fakeImageBackend struct {
	listImages   func(context.Context, images.ImageListRequest) (images.ImageListResult, error)
	pullImage    func(context.Context, images.ImagePullRequest) (images.ImagePullResult, error)
	inspectImage func(context.Context, images.ImageInspectRequest) (images.ImageInspectResult, error)
	removeImage  func(context.Context, images.ImageRemoveRequest) (images.ImageRemoveResult, error)
}

func (b *fakeImageBackend) ListImages(ctx context.Context, req images.ImageListRequest) (images.ImageListResult, error) {
	if b.listImages == nil {
		return images.ImageListResult{}, errors.New("ListImages fake is not configured")
	}
	return b.listImages(ctx, req)
}

func (b *fakeImageBackend) PullImage(ctx context.Context, req images.ImagePullRequest) (images.ImagePullResult, error) {
	if b.pullImage == nil {
		return images.ImagePullResult{}, errors.New("PullImage fake is not configured")
	}
	return b.pullImage(ctx, req)
}

func (b *fakeImageBackend) InspectImage(ctx context.Context, req images.ImageInspectRequest) (images.ImageInspectResult, error) {
	if b.inspectImage == nil {
		return images.ImageInspectResult{}, errors.New("InspectImage fake is not configured")
	}
	return b.inspectImage(ctx, req)
}

func (b *fakeImageBackend) RemoveImage(ctx context.Context, req images.ImageRemoveRequest) (images.ImageRemoveResult, error) {
	if b.removeImage == nil {
		return images.ImageRemoveResult{}, errors.New("RemoveImage fake is not configured")
	}
	return b.removeImage(ctx, req)
}
