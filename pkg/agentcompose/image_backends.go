package agentcompose

import (
	"path/filepath"
	"strings"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/imagecache"
	"agent-compose/pkg/images"
)

type imageBackends struct {
	docker ImageBackend
	oci    ImageBackend
	auto   ImageBackend
}

func newImageBackends(di do.Injector) (*imageBackends, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	imageCacheRoot := strings.TrimSpace(config.ImageCacheRoot)
	if imageCacheRoot == "" {
		imageCacheRoot = filepath.Join(config.DataRoot, "images")
		config.ImageCacheRoot = imageCacheRoot
	}
	dockerImages := images.NewDockerImageBackend()
	ociCache, err := imagecache.New(imagecache.Config{
		Root:               imageCacheRoot,
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return nil, err
	}
	config.ImageCacheRoot = ociCache.Root()
	ociImages := images.NewOCIImageBackend(ociCache)
	return &imageBackends{
		docker: dockerImages,
		oci:    ociImages,
		auto:   images.NewAutoImageBackend(config.ImageStoreMode, dockerImages, ociImages),
	}, nil
}

func imageBackendsFromDI(di do.Injector) (*imageBackends, error) {
	backends, err := do.Invoke[*imageBackends](di)
	if err == nil && backends != nil {
		return backends, nil
	}
	backends, err = newImageBackends(di)
	if err != nil {
		return nil, err
	}
	do.ProvideValue(di, backends)
	return backends, nil
}
