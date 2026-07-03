package images

import (
	"context"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type Backend interface {
	ListImages(context.Context, ListRequest) (ListResult, error)
	PullImage(context.Context, PullRequest) (PullResult, error)
	InspectImage(context.Context, InspectRequest) (InspectResult, error)
	RemoveImage(context.Context, RemoveRequest) (RemoveResult, error)
}

type ListRequest struct {
	Query string
	All   bool
}

type ListResult struct {
	Images      []*agentcomposev2.Image
	StoreStatus *agentcomposev2.ImageStoreStatus
}

type PullRequest struct {
	ImageRef string
	Platform *agentcomposev2.ImagePlatform
}

type PullResult struct {
	Image       *agentcomposev2.Image
	ResolvedRef string
	Progress    []*agentcomposev2.ImagePullProgress
	Warnings    []string
}

type InspectRequest struct {
	ImageRef string
}

type InspectResult struct {
	Image       *agentcomposev2.Image
	StoreStatus *agentcomposev2.ImageStoreStatus
}

type RemoveRequest struct {
	ImageRef      string
	Force         bool
	PruneChildren bool
}

type RemoveResult struct {
	ImageRef     string
	UntaggedRefs []string
	DeletedIDs   []string
	Warnings     []string
}
