package agentcompose

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/samber/do/v2"

	appshell "agent-compose/internal/agentcompose/app"
	"agent-compose/internal/capproxy"
	appconfig "agent-compose/internal/config"
	"agent-compose/internal/imagecache"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type Service struct {
	config     *appconfig.Config
	store      *Store
	configDB   *ConfigStore
	driver     Driver
	runtimes   RuntimeProvider
	executor   *Executor
	loaders    *LoaderManager
	images     ImageBackend
	ociImages  ImageBackend
	autoImages ImageBackend
	llm        *LLMClient
	cap        capabilityIntegration
	bus        *LoaderBus
	streams    *SessionStreamBroker
	dashboard  *DashboardOverviewHub
	events     *EventDispatcher
	sessions   *SessionRPCBridge
	startedAt  time.Time
	background appshell.BackgroundStarter
	agentcomposev1connect.UnimplementedSessionServiceHandler
	agentcomposev1connect.UnimplementedKernelServiceHandler
	agentcomposev1connect.UnimplementedAgentServiceHandler
	agentcomposev1connect.UnimplementedAgentDefinitionServiceHandler
	agentcomposev1connect.UnimplementedLLMServiceHandler
	agentcomposev1connect.UnimplementedConfigServiceHandler
	agentcomposev1connect.UnimplementedLoaderServiceHandler
	agentcomposev1connect.UnimplementedDashboardServiceHandler
	agentcomposev1connect.UnimplementedCapabilityServiceHandler
	agentcomposev2connect.UnimplementedProjectServiceHandler
	agentcomposev2connect.UnimplementedRunServiceHandler
	agentcomposev2connect.UnimplementedExecServiceHandler
	agentcomposev2connect.UnimplementedImageServiceHandler
}

func NewService(di do.Injector) (*Service, error) {
	state, err := appshell.BuildState[
		*appconfig.Config,
		*Store,
		*ConfigStore,
		Driver,
		RuntimeProvider,
		*Executor,
		*LoaderManager,
		ImageBackend,
		*LLMClient,
		capabilityIntegration,
		*LoaderBus,
		*SessionStreamBroker,
		*DashboardOverviewHub,
		*EventDispatcher,
		*SessionRPCBridge,
	](di, appshell.StateOptions[*appconfig.Config, *DashboardOverviewHub, ImageBackend, *EventDispatcher]{
		Dashboard: defaultDashboardOverviewHub,
		Images:    configureServiceImages,
		Events: func(di do.Injector) *EventDispatcher {
			return NewEventDispatcher(do.MustInvoke[context.Context](di), do.MustInvoke[*ConfigStore](di), do.MustInvoke[*LoaderBus](di))
		},
	})
	if err != nil {
		return nil, err
	}
	return &Service{
		config:     state.Config,
		store:      state.Store,
		configDB:   state.ConfigDB,
		driver:     state.Driver,
		runtimes:   state.Runtimes,
		executor:   state.Executor,
		loaders:    state.Loaders,
		images:     state.Images,
		ociImages:  state.OCIImages,
		autoImages: state.AutoImages,
		llm:        state.LLM,
		cap:        state.Cap,
		bus:        state.Bus,
		streams:    state.Streams,
		dashboard:  state.Dashboard,
		events:     state.Events,
		sessions:   state.Sessions,
		startedAt:  state.StartedAt,
	}, nil
}

func defaultDashboardOverviewHub(di do.Injector, dashboard *DashboardOverviewHub) *DashboardOverviewHub {
	if dashboard != nil {
		return dashboard
	}
	rootCtx, _ := do.Invoke[context.Context](di)
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	return newDashboardOverviewHub(rootCtx, newDashboardOverviewAggregator(do.MustInvoke[*Store](di), do.MustInvoke[*ConfigStore](di)), 250*time.Millisecond)
}

func configureServiceImages(config *appconfig.Config) (appshell.ImageSet[ImageBackend], error) {
	imageCacheRoot := strings.TrimSpace(config.ImageCacheRoot)
	if imageCacheRoot == "" {
		imageCacheRoot = filepath.Join(config.DataRoot, "images")
		config.ImageCacheRoot = imageCacheRoot
	}
	dockerImages := NewDockerImageBackend()
	ociCache, err := imagecache.New(imagecache.Config{
		Root:               imageCacheRoot,
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return appshell.ImageSet[ImageBackend]{}, err
	}
	config.ImageCacheRoot = ociCache.Root()
	ociImages := NewOCIImageBackend(ociCache)
	return appshell.ImageSet[ImageBackend]{
		Images:     dockerImages,
		OCIImages:  ociImages,
		AutoImages: NewAutoImageBackend(config.ImageStoreMode, dockerImages, ociImages),
	}, nil
}

func Setup(di do.Injector) {
	bootstrapSetup(di)
}

func Register(di do.Injector) {
	bootstrapRegister(di)
}

func StartBackground(di do.Injector) error {
	return bootstrapStartBackground(di)
}

func (s *Service) StartBackground(ctx context.Context, capProxy *capproxy.Server) error {
	return s.background.Start(ctx, appshell.BackgroundComponents{
		ReconcilePersistedSessions: s.reconcilePersistedSessions,
		StartLoaders:               s.loaders.Start,
		StartEvents:                s.events.Start,
		CapabilityProxy:            capProxy,
	})
}
