package drivermount

import (
	. "agent-compose/pkg/driver/types"
	drivertypes "agent-compose/pkg/driver/types"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/imagecache"
)

const runtimeSmokeOCIImageSkipMessage = "set SMOKE_OCI_IMAGE_REF to run go-containerregistry OCI image smoke coverage for BoxLite/Microsandbox consumption"

func TestSmokeGoContainerRegistryOCIImagePullsToCache(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	config := &appconfig.Config{
		DataRoot:      t.TempDir(),
		ImageRegistry: FirstNonEmpty(os.Getenv("IMAGE_REGISTRY"), "docker.io"),
	}
	prepareRuntimeSmokeGoContainerRegistryOCIImage(t, ctx, config)
}

func TestRuntimeSmokeOCIImageDisablesDockerEnvironment(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://docker.example:2375")
	t.Setenv("DOCKER_CONTEXT", "remote")
	t.Setenv("DOCKER_CONFIG", "/tmp/docker-config")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", "/tmp/docker-certs")

	config := &appconfig.Config{DataRoot: t.TempDir()}
	disableRuntimeSmokeDockerDaemon(t, config)

	host := os.Getenv("DOCKER_HOST")
	if !strings.HasPrefix(host, "unix://") || !strings.Contains(host, "docker-unavailable.sock") {
		t.Fatalf("DOCKER_HOST = %q, want unavailable unix socket", host)
	}
	for _, key := range []string{"DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if _, ok := os.LookupEnv(key); ok {
			t.Fatalf("%s remained set", key)
		}
	}
}

func prepareRuntimeSmokeGoContainerRegistryOCIImage(t *testing.T, ctx context.Context, config *appconfig.Config) string {
	t.Helper()
	imageRef := strings.TrimSpace(os.Getenv("SMOKE_OCI_IMAGE_REF"))
	if imageRef == "" {
		t.Skip(runtimeSmokeOCIImageSkipMessage)
	}
	if config == nil {
		t.Fatal("runtime smoke config is required")
	}
	prepareRuntimeSmokeOCIImageConfig(t, config)
	disableRuntimeSmokeDockerDaemon(t, config)

	cache, err := imagecache.New(imagecache.Config{
		Root:               config.ImageCacheRoot,
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		t.Fatalf("create OCI image cache: %v", err)
	}
	result, err := cache.Pull(ctx, imagecache.PullRequest{Reference: imageRef})
	if err != nil {
		t.Fatalf("pull SMOKE_OCI_IMAGE_REF=%q with go-containerregistry OCI cache: %v", imageRef, err)
	}
	resolvedRef := strings.TrimSpace(result.ResolvedRef)
	if resolvedRef == "" {
		resolvedRef = imageRef
	}
	t.Logf("pulled SMOKE_OCI_IMAGE_REF=%q resolved=%q into OCI cache %s", imageRef, resolvedRef, config.ImageCacheRoot)
	return resolvedRef
}

func prepareRuntimeSmokeOCIImageConfig(t *testing.T, config *appconfig.Config) {
	t.Helper()
	if strings.TrimSpace(config.DataRoot) == "" {
		config.DataRoot = t.TempDir()
	}
	if strings.TrimSpace(config.ImageCacheRoot) == "" {
		config.ImageCacheRoot = drivertypes.ImageCacheRootForDriver(config)
	}
	if len(config.ImageInsecureRegistries) == 0 {
		config.ImageInsecureRegistries = splitRuntimeSmokeList(os.Getenv("IMAGE_INSECURE_REGISTRIES"))
	}
}

func disableRuntimeSmokeDockerDaemon(t *testing.T, config *appconfig.Config) {
	t.Helper()
	root := ""
	if config != nil {
		root = strings.TrimSpace(config.DataRoot)
	}
	if root == "" {
		root = t.TempDir()
	}
	setRuntimeSmokeEnv(t, "DOCKER_HOST", "unix://"+filepath.Join(root, "docker-unavailable.sock"))
	for _, key := range []string{"DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		unsetRuntimeSmokeEnv(t, key)
	}
}

func splitRuntimeSmokeList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	})
	items := make([]string, 0, len(fields))
	for _, field := range fields {
		if item := strings.TrimSpace(field); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func setRuntimeSmokeEnv(t *testing.T, key string, value string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

func unsetRuntimeSmokeEnv(t *testing.T, key string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
}
