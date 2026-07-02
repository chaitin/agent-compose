package agentcompose

import (
	"context"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	executorpkg "agent-compose/pkg/executor"
	imagespkg "agent-compose/pkg/images"
	loaderspkg "agent-compose/pkg/loaders"
	projectspkg "agent-compose/pkg/projects"
	runtimespkg "agent-compose/pkg/runtimes"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"
)

func TestServiceProjectServiceCachesHandler(t *testing.T) {
	service := &Service{}
	first := service.projectService()
	if first == nil {
		t.Fatalf("projectService returned nil")
	}
	second := service.projectService()
	if second != first {
		t.Fatalf("projectService rebuilt handler: first=%p second=%p", first, second)
	}

	prebuilt := newProjectServiceFromDeps(nil)
	service = &Service{projectHandlers: prebuilt}
	if got := service.projectService(); got != prebuilt {
		t.Fatalf("projectService ignored prebuilt handler: got=%p want=%p", got, prebuilt)
	}
}

func TestServiceForwarderInitializationConcurrent(t *testing.T) {
	service := &Service{}
	const workers = 32
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = service.sessionsService()
			_ = service.agentDefinitionService()
			_ = service.dashboardService()
			_ = service.capabilityService()
			_ = service.workspaceService()
			_ = service.settingsService()
			_ = service.loaderService()
			_ = service.projectService()
			_ = service.imageService()
		}()
	}
	wg.Wait()

	if service.sessionHandlers == nil || service.agentHandlers == nil || service.dashboardHandlers == nil ||
		service.capabilityHandlers == nil || service.workspaceHandlers == nil || service.settingsHandlers == nil ||
		service.loaderHandlers == nil || service.projectHandlers == nil || service.imageHandlers == nil {
		t.Fatalf("expected all lazy forwarders to be initialized")
	}

	if got := service.sessionsService(); got != service.sessionHandlers {
		t.Fatalf("sessionsService rebuilt handler: got=%p want=%p", got, service.sessionHandlers)
	}
	if got := service.agentDefinitionService(); got != service.agentHandlers {
		t.Fatalf("agentDefinitionService rebuilt handler: got=%p want=%p", got, service.agentHandlers)
	}
	if got := service.settingsService(); got != service.settingsHandlers {
		t.Fatalf("settingsService rebuilt handler: got=%p want=%p", got, service.settingsHandlers)
	}
	if got := service.projectService(); got != service.projectHandlers {
		t.Fatalf("projectService rebuilt handler: got=%p want=%p", got, service.projectHandlers)
	}
	if got := service.imageService(); got != service.imageHandlers {
		t.Fatalf("imageService rebuilt handler: got=%p want=%p", got, service.imageHandlers)
	}
}

func TestRegisterSharesImageBackendsAcrossServiceGraph(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("SESSION_ROOT", filepath.Join(root, "sessions"))
	t.Setenv("RUNTIME_DRIVER", driverpkg.RuntimeDriverDocker)
	t.Setenv("DOCKER_IMAGE", "guest:latest")
	t.Setenv("SESSION_START_TIMEOUT", "1s")
	t.Setenv("SESSION_STOP_TIMEOUT", "1s")
	t.Setenv("LLM_API_ENDPOINT", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	di := do.New()
	appconfig.Setup(di)
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, slog.Default())
	do.ProvideValue(di, echo.New())
	Register(di)

	backends := do.MustInvoke[*imageBackends](di)
	service := do.MustInvoke[*Service](di)
	if service.images != backends.docker {
		t.Fatalf("Service docker backend = %p, want shared %p", service.images, backends.docker)
	}
	if service.ociImages != backends.oci {
		t.Fatalf("Service OCI backend = %p, want shared %p", service.ociImages, backends.oci)
	}
	if service.autoImages != backends.auto {
		t.Fatalf("Service auto backend = %p, want shared %p", service.autoImages, backends.auto)
	}
	if service.imageHandlers.DockerBackend() != backends.docker {
		t.Fatalf("ImageService docker backend = %p, want shared %p", service.imageHandlers.DockerBackend(), backends.docker)
	}
	if service.imageHandlers.OCIBackend() != backends.oci {
		t.Fatalf("ImageService OCI backend = %p, want shared %p", service.imageHandlers.OCIBackend(), backends.oci)
	}
	if service.imageHandlers.AutoBackend() != backends.auto {
		t.Fatalf("ImageService auto backend = %p, want shared %p", service.imageHandlers.AutoBackend(), backends.auto)
	}
	auto, ok := service.autoImages.(*imagespkg.AutoImageBackend)
	if !ok {
		t.Fatalf("Service auto backend = %T, want *AutoImageBackend", service.autoImages)
	}
	if auto.DockerBackend() != backends.docker {
		t.Fatalf("auto docker backend = %p, want shared %p", auto.DockerBackend(), backends.docker)
	}
	if auto.OCIBackend() != backends.oci {
		t.Fatalf("auto OCI backend = %p, want shared %p", auto.OCIBackend(), backends.oci)
	}

	assertBackendField(t, do.MustInvoke[*loaderspkg.LoaderManager](di), "images", backends.docker)
	assertBackendField(t, do.MustInvoke[*projectspkg.Service](di), "images", backends.docker)

	publisher := do.MustInvoke[executorpkg.StreamPublisher](di)
	if publisher == nil || publisher != service.streams {
		t.Fatalf("executor stream publisher = %T/%p, want service stream broker", publisher, publisher)
	}
	if preparer := do.MustInvoke[executorpkg.LLMFacadeEnvPreparer](di); preparer == nil {
		t.Fatalf("executor LLM facade preparer is nil")
	}
	if preparer := do.MustInvoke[runtimespkg.SessionRuntimeEnvPreparer](di); preparer == nil {
		t.Fatalf("session runtime env preparer is nil")
	}
}

func assertBackendField(t *testing.T, owner any, fieldName string, want imagespkg.ImageBackend) {
	t.Helper()
	field := reflect.ValueOf(owner).Elem().FieldByName(fieldName)
	if !field.IsValid() {
		t.Fatalf("%T has no %s field", owner, fieldName)
	}
	if got, want := interfacePointer(field), backendPointer(want); got != want {
		t.Fatalf("%T.%s backend = %#x, want shared %#x", owner, fieldName, got, want)
	}
}

func interfacePointer(value reflect.Value) uintptr {
	if value.Kind() != reflect.Interface || value.IsNil() {
		return 0
	}
	elem := value.Elem()
	if elem.Kind() != reflect.Ptr {
		return 0
	}
	return elem.Pointer()
}

func backendPointer(backend imagespkg.ImageBackend) uintptr {
	value := reflect.ValueOf(backend)
	if value.Kind() != reflect.Ptr {
		return 0
	}
	return value.Pointer()
}
