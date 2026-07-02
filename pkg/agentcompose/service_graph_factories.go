package agentcompose

import (
	"context"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/loaders"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/storage"
)

func newLoaderEngine(di do.Injector) (LoaderEngine, error) {
	return loaders.NewLoaderEngine(di)
}

func newLoaderManager(di do.Injector) (*LoaderManager, error) {
	imageBackends, err := imageBackendsFromDI(di)
	if err != nil {
		return nil, err
	}
	manager, err := loaders.NewManager(loaders.ManagerDeps{
		Config:             do.MustInvoke[*appconfig.Config](di),
		RootCtx:            do.MustInvoke[context.Context](di),
		Store:              do.MustInvoke[*Store](di),
		ConfigDB:           do.MustInvoke[*ConfigStore](di),
		Driver:             do.MustInvoke[Driver](di),
		Executor:           loaders.NewExecutor(do.MustInvoke[*appconfig.Config](di), do.MustInvoke[*Store](di), do.MustInvoke[*ConfigStore](di), do.MustInvoke[RuntimeProvider](di), do.MustInvoke[*SessionStreamBroker](di).componentBroker()),
		Images:             imageBackends.docker,
		LLM:                do.MustInvoke[*LLMClient](di).componentClient(),
		CapabilityProvider: do.MustInvoke[capabilityIntegration](di),
		Bus:                do.MustInvoke[*LoaderBus](di),
		Streams:            do.MustInvoke[*SessionStreamBroker](di).componentBroker(),
		Engine:             do.MustInvoke[LoaderEngine](di),
		Sessions:           do.MustInvoke[*SessionRPCBridge](di).componentBridge(),
		Dashboard:          mustDashboardHub(di),
	})
	if err != nil {
		return nil, err
	}
	return manager, nil
}

func mustDashboardHub(di do.Injector) *DashboardOverviewHub {
	dashboard, _ := do.Invoke[*DashboardOverviewHub](di)
	return dashboard
}

func newProjectService(di do.Injector) (*ProjectService, error) {
	imageBackends, err := imageBackendsFromDI(di)
	if err != nil {
		return nil, err
	}
	return newProjectServiceFromDeps(&Service{
		config:    do.MustInvoke[*appconfig.Config](di),
		store:     do.MustInvoke[*Store](di),
		configDB:  do.MustInvoke[*ConfigStore](di),
		driver:    do.MustInvoke[Driver](di),
		executor:  do.MustInvoke[*Executor](di),
		images:    imageBackends.docker,
		loaders:   do.MustInvoke[*LoaderManager](di),
		cap:       do.MustInvoke[capabilityIntegration](di),
		bus:       do.MustInvoke[*LoaderBus](di),
		streams:   do.MustInvoke[*SessionStreamBroker](di),
		dashboard: mustDashboardHub(di),
	}), nil
}

func newProjectServiceFromDeps(s *Service) *ProjectService {
	deps := projectServiceDeps(s)
	return projects.NewService(deps)
}

func projectServiceDeps(s *Service) projects.ServiceDeps {
	if s == nil {
		return projects.ServiceDeps{}
	}
	var executorComponent *projects.Executor
	if s.executor != nil {
		executorComponent = s.executor.componentExecutor()
	}
	var streams *projects.SessionStreamBroker
	if s.streams != nil {
		streams = s.streams.componentBroker()
	}
	return projects.ServiceDeps{
		Config:    s.config,
		Store:     s.store,
		ConfigDB:  s.configDB,
		Driver:    s.driver,
		Executor:  executorComponent,
		Images:    s.images,
		Loaders:   s.loaders,
		Cap:       s.cap,
		Bus:       s.bus,
		Streams:   streams,
		Dashboard: s.dashboard,
	}
}

func newStore(di do.Injector) (*Store, error) {
	return storage.NewStore(di)
}

func newConfigStore(di do.Injector) (*ConfigStore, error) {
	return storage.NewConfigStore(di)
}
