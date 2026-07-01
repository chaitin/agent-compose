package agentcompose

import (
	"agent-compose/pkg/imagecache"
	"agent-compose/pkg/images"
)

type ImageBackend = images.ImageBackend
type ImageListRequest = images.ImageListRequest
type ImageListResult = images.ImageListResult
type ImagePullRequest = images.ImagePullRequest
type ImagePullResult = images.ImagePullResult
type ImageInspectRequest = images.ImageInspectRequest
type ImageInspectResult = images.ImageInspectResult
type ImageRemoveRequest = images.ImageRemoveRequest
type ImageRemoveResult = images.ImageRemoveResult
type DockerImageBackend = images.DockerImageBackend
type OCIImageBackend = images.OCIImageBackend
type AutoImageBackend = images.AutoImageBackend
type DockerPingFunc = images.DockerPingFunc
type imageBackendOpError = images.BackendOpError

func NewDockerImageBackend() *DockerImageBackend {
	return images.NewDockerImageBackend()
}

func NewOCIImageBackend(cache *imagecache.Cache) *OCIImageBackend {
	return images.NewOCIImageBackend(cache)
}

func NewAutoImageBackend(mode string, dockerBackend, ociBackend ImageBackend) *AutoImageBackend {
	return images.NewAutoImageBackend(mode, dockerBackend, ociBackend)
}
