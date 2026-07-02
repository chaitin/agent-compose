package image

import (
	"context"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ImageBackend interface {
	ListImages(context.Context, ImageListRequest) (ImageListResult, error)
	PullImage(context.Context, ImagePullRequest) (ImagePullResult, error)
	InspectImage(context.Context, ImageInspectRequest) (ImageInspectResult, error)
	RemoveImage(context.Context, ImageRemoveRequest) (ImageRemoveResult, error)
}

type ImageListRequest struct {
	Query string
	All   bool
}

type ImageListResult struct {
	Images      []*agentcomposev2.Image
	StoreStatus *agentcomposev2.ImageStoreStatus
}

type ImagePullRequest struct {
	ImageRef string
	Platform *agentcomposev2.ImagePlatform
}

type ImagePullResult struct {
	Image       *agentcomposev2.Image
	ResolvedRef string
	Progress    []*agentcomposev2.ImagePullProgress
	Warnings    []string
}

type ImageInspectRequest struct {
	ImageRef string
}

type ImageInspectResult struct {
	Image       *agentcomposev2.Image
	StoreStatus *agentcomposev2.ImageStoreStatus
}

type ImageRemoveRequest struct {
	ImageRef      string
	Force         bool
	PruneChildren bool
}

type ImageRemoveResult struct {
	ImageRef     string
	UntaggedRefs []string
	DeletedIDs   []string
	Warnings     []string
}
