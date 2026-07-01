package agentcompose

import (
	"context"
	"errors"
)

type fakeImageBackend struct {
	listImages   func(context.Context, ImageListRequest) (ImageListResult, error)
	pullImage    func(context.Context, ImagePullRequest) (ImagePullResult, error)
	inspectImage func(context.Context, ImageInspectRequest) (ImageInspectResult, error)
	removeImage  func(context.Context, ImageRemoveRequest) (ImageRemoveResult, error)
}

func (b *fakeImageBackend) ListImages(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
	if b.listImages == nil {
		return ImageListResult{}, errors.New("ListImages fake is not configured")
	}
	return b.listImages(ctx, req)
}

func (b *fakeImageBackend) PullImage(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
	if b.pullImage == nil {
		return ImagePullResult{}, errors.New("PullImage fake is not configured")
	}
	return b.pullImage(ctx, req)
}

func (b *fakeImageBackend) InspectImage(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
	if b.inspectImage == nil {
		return ImageInspectResult{}, errors.New("InspectImage fake is not configured")
	}
	return b.inspectImage(ctx, req)
}

func (b *fakeImageBackend) RemoveImage(ctx context.Context, req ImageRemoveRequest) (ImageRemoveResult, error) {
	if b.removeImage == nil {
		return ImageRemoveResult{}, errors.New("RemoveImage fake is not configured")
	}
	return b.removeImage(ctx, req)
}
