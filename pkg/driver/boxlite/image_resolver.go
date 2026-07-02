package boxlitedriver

import (
	"context"
	"strings"
)

type boxliteImageLayoutResult struct {
	ImageID     string
	ResolvedRef string
	RootfsPath  string
}

type boxliteImageResolverOps struct {
	dockerAvailable   func(context.Context) bool
	dockerMaterialize func(context.Context, string) (boxliteImageLayoutResult, bool, error)
	ociMaterialize    func(context.Context, string) (boxliteImageLayoutResult, bool, error)
}

func resolveBoxliteImageLayout(ctx context.Context, imageRef string, ops boxliteImageResolverOps) (boxliteImageLayoutResult, bool, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return boxliteImageLayoutResult{}, false, nil
	}
	if ops.dockerAvailable != nil && ops.dockerAvailable(ctx) && ops.dockerMaterialize != nil {
		layout, ok, err := ops.dockerMaterialize(ctx, imageRef)
		if err != nil || ok {
			return layout, ok, err
		}
	}
	if ops.ociMaterialize == nil {
		return boxliteImageLayoutResult{}, false, nil
	}
	return ops.ociMaterialize(ctx, imageRef)
}
