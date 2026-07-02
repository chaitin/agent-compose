package loader

import "context"

type fakeImageBackend struct{}

func (fakeImageBackend) ListImages(context.Context, ImageListRequest) (ImageListResult, error) {
	return ImageListResult{}, nil
}

func (fakeImageBackend) PullImage(context.Context, ImagePullRequest) (ImagePullResult, error) {
	return ImagePullResult{}, nil
}

func (fakeImageBackend) InspectImage(context.Context, ImageInspectRequest) (ImageInspectResult, error) {
	return ImageInspectResult{}, nil
}

func (fakeImageBackend) RemoveImage(context.Context, ImageRemoveRequest) (ImageRemoveResult, error) {
	return ImageRemoveResult{}, nil
}
