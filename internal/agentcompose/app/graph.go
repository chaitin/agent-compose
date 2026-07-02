package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"
)

type Providers[
	Store any,
	ConfigStore any,
	RuntimeProvider any,
	Driver any,
	Executor any,
	LLMClient any,
	CapabilityProvider any,
	CapProxy any,
	LoaderBus any,
	SessionStreamBroker any,
	DashboardOverviewAggregator any,
	DashboardOverviewHub any,
	LoaderEngine any,
	SessionRPCBridge any,
	LoaderManager any,
	Service any,
] struct {
	Store                       do.Provider[Store]
	ConfigStore                 do.Provider[ConfigStore]
	RuntimeProvider             do.Provider[RuntimeProvider]
	Driver                      do.Provider[Driver]
	Executor                    do.Provider[Executor]
	LLMClient                   do.Provider[LLMClient]
	CapabilityProvider          do.Provider[CapabilityProvider]
	CapProxy                    do.Provider[CapProxy]
	LoaderBus                   do.Provider[LoaderBus]
	SessionStreamBroker         do.Provider[SessionStreamBroker]
	DashboardOverviewAggregator do.Provider[DashboardOverviewAggregator]
	DashboardOverviewHub        do.Provider[DashboardOverviewHub]
	LoaderEngine                do.Provider[LoaderEngine]
	SessionRPCBridge            do.Provider[SessionRPCBridge]
	LoaderManager               do.Provider[LoaderManager]
	Service                     do.Provider[Service]
	RegisterRoutes              func(*echo.Echo, Service)
}

func Register[
	Store any,
	ConfigStore any,
	RuntimeProvider any,
	Driver any,
	Executor any,
	LLMClient any,
	CapabilityProvider any,
	CapProxy any,
	LoaderBus any,
	SessionStreamBroker any,
	DashboardOverviewAggregator any,
	DashboardOverviewHub any,
	LoaderEngine any,
	SessionRPCBridge any,
	LoaderManager any,
	Service any,
](di do.Injector, providers Providers[
	Store,
	ConfigStore,
	RuntimeProvider,
	Driver,
	Executor,
	LLMClient,
	CapabilityProvider,
	CapProxy,
	LoaderBus,
	SessionStreamBroker,
	DashboardOverviewAggregator,
	DashboardOverviewHub,
	LoaderEngine,
	SessionRPCBridge,
	LoaderManager,
	Service,
]) {
	do.Provide(di, providers.Store)
	do.Provide(di, providers.ConfigStore)
	do.Provide(di, providers.RuntimeProvider)
	do.Provide(di, providers.Driver)
	do.Provide(di, providers.Executor)
	do.Provide(di, providers.LLMClient)
	do.Provide(di, providers.CapabilityProvider)
	do.Provide(di, providers.CapProxy)
	do.Provide(di, providers.LoaderBus)
	do.Provide(di, providers.SessionStreamBroker)
	do.Provide(di, providers.DashboardOverviewAggregator)
	do.Provide(di, providers.DashboardOverviewHub)
	do.Provide(di, providers.LoaderEngine)
	do.Provide(di, providers.SessionRPCBridge)
	do.Provide(di, providers.LoaderManager)
	do.Provide(di, providers.Service)

	app := do.MustInvoke[*echo.Echo](di)
	service := do.MustInvoke[Service](di)
	providers.RegisterRoutes(app, service)
}

type ImageSet[ImageBackend any] struct {
	Images     ImageBackend
	OCIImages  ImageBackend
	AutoImages ImageBackend
}

type State[
	Config any,
	Store any,
	ConfigStore any,
	Driver any,
	RuntimeProvider any,
	Executor any,
	LoaderManager any,
	ImageBackend any,
	LLMClient any,
	CapabilityProvider any,
	LoaderBus any,
	SessionStreamBroker any,
	DashboardOverviewHub any,
	EventDispatcher any,
	SessionRPCBridge any,
] struct {
	Config     Config
	Store      Store
	ConfigDB   ConfigStore
	Driver     Driver
	Runtimes   RuntimeProvider
	Executor   Executor
	Loaders    LoaderManager
	Images     ImageBackend
	OCIImages  ImageBackend
	AutoImages ImageBackend
	LLM        LLMClient
	Cap        CapabilityProvider
	Bus        LoaderBus
	Streams    SessionStreamBroker
	Dashboard  DashboardOverviewHub
	Events     EventDispatcher
	Sessions   SessionRPCBridge
	StartedAt  time.Time
}

type StateOptions[
	Config any,
	DashboardOverviewHub any,
	ImageBackend any,
	EventDispatcher any,
] struct {
	Dashboard func(do.Injector, DashboardOverviewHub) DashboardOverviewHub
	Images    func(Config) (ImageSet[ImageBackend], error)
	Events    func(do.Injector) EventDispatcher
	Now       func() time.Time
}

func BuildState[
	Config any,
	Store any,
	ConfigStore any,
	Driver any,
	RuntimeProvider any,
	Executor any,
	LoaderManager any,
	ImageBackend any,
	LLMClient any,
	CapabilityProvider any,
	LoaderBus any,
	SessionStreamBroker any,
	DashboardOverviewHub any,
	EventDispatcher any,
	SessionRPCBridge any,
](di do.Injector, options StateOptions[Config, DashboardOverviewHub, ImageBackend, EventDispatcher]) (State[
	Config,
	Store,
	ConfigStore,
	Driver,
	RuntimeProvider,
	Executor,
	LoaderManager,
	ImageBackend,
	LLMClient,
	CapabilityProvider,
	LoaderBus,
	SessionStreamBroker,
	DashboardOverviewHub,
	EventDispatcher,
	SessionRPCBridge,
], error) {
	config := do.MustInvoke[Config](di)
	dashboard, _ := do.Invoke[DashboardOverviewHub](di)
	if options.Dashboard != nil {
		dashboard = options.Dashboard(di, dashboard)
	}
	images, err := options.Images(config)
	if err != nil {
		return State[
			Config,
			Store,
			ConfigStore,
			Driver,
			RuntimeProvider,
			Executor,
			LoaderManager,
			ImageBackend,
			LLMClient,
			CapabilityProvider,
			LoaderBus,
			SessionStreamBroker,
			DashboardOverviewHub,
			EventDispatcher,
			SessionRPCBridge,
		]{}, err
	}
	now := func() time.Time {
		return time.Now().UTC()
	}
	if options.Now != nil {
		now = options.Now
	}
	return State[
		Config,
		Store,
		ConfigStore,
		Driver,
		RuntimeProvider,
		Executor,
		LoaderManager,
		ImageBackend,
		LLMClient,
		CapabilityProvider,
		LoaderBus,
		SessionStreamBroker,
		DashboardOverviewHub,
		EventDispatcher,
		SessionRPCBridge,
	]{
		Config:     config,
		Store:      do.MustInvoke[Store](di),
		ConfigDB:   do.MustInvoke[ConfigStore](di),
		Driver:     do.MustInvoke[Driver](di),
		Runtimes:   do.MustInvoke[RuntimeProvider](di),
		Executor:   do.MustInvoke[Executor](di),
		Loaders:    do.MustInvoke[LoaderManager](di),
		Images:     images.Images,
		OCIImages:  images.OCIImages,
		AutoImages: images.AutoImages,
		LLM:        do.MustInvoke[LLMClient](di),
		Cap:        do.MustInvoke[CapabilityProvider](di),
		Bus:        do.MustInvoke[LoaderBus](di),
		Streams:    do.MustInvoke[SessionStreamBroker](di),
		Dashboard:  dashboard,
		Events:     options.Events(di),
		Sessions:   do.MustInvoke[SessionRPCBridge](di),
		StartedAt:  now(),
	}, nil
}

type CapabilityProxy interface {
	Configured() bool
	Serve(context.Context) error
}

type BackgroundStarter struct {
	once sync.Once
	err  error
}

type BackgroundComponents struct {
	ReconcilePersistedSessions func(context.Context) error
	StartLoaders               func()
	StartEvents                func()
	CapabilityProxy            CapabilityProxy
}

func (s *BackgroundStarter) Start(ctx context.Context, components BackgroundComponents) error {
	s.once.Do(func() {
		reconcileCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if components.ReconcilePersistedSessions != nil {
			if err := components.ReconcilePersistedSessions(reconcileCtx); err != nil {
				slog.Warn("failed to reconcile persisted session state on startup", "error", err)
			}
		}
		if components.StartLoaders != nil {
			components.StartLoaders()
		}
		if components.StartEvents != nil {
			components.StartEvents()
		}
		s.err = StartCapabilityProxy(ctx, components.CapabilityProxy)
	})
	return s.err
}

func StartCapabilityProxy(ctx context.Context, capProxy CapabilityProxy) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if capProxy != nil && capProxy.Configured() {
		go func() {
			if err := capProxy.Serve(ctx); err != nil {
				slog.Error("agent compose capability grpc proxy stopped", "error", err)
			}
		}()
	}
	return nil
}

type BackgroundOptions[Service any, CapProxy any] struct {
	Start func(Service, context.Context, CapProxy) error
}

func StartBackground[Service any, CapProxy any](di do.Injector, options BackgroundOptions[Service, CapProxy]) error {
	service := do.MustInvoke[Service](di)
	return options.Start(service, do.MustInvoke[context.Context](di), do.MustInvoke[CapProxy](di))
}
