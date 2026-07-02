package agentcompose

import (
	"context"

	"github.com/samber/do/v2"

	"agent-compose/pkg/bus"
	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/executor"
	llmpkg "agent-compose/pkg/llm"
	"agent-compose/pkg/loaders"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/runtimes"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage"
)

func newLoaderEngine(di do.Injector) (loaders.LoaderEngine, error) {
	return loaders.NewLoaderEngine(di)
}

func newLoaderManager(di do.Injector) (*loaders.LoaderManager, error) {
	imageBackends, err := imageBackendsFromDI(di)
	if err != nil {
		return nil, err
	}
	manager, err := loaders.NewManager(loaders.ManagerDeps{
		Config:             do.MustInvoke[*appconfig.Config](di),
		RootCtx:            do.MustInvoke[context.Context](di),
		Store:              do.MustInvoke[*storage.Store](di),
		ConfigDB:           do.MustInvoke[*storage.ConfigStore](di),
		Driver:             do.MustInvoke[runtimes.Driver](di),
		Executor:           loaders.NewExecutor(do.MustInvoke[*appconfig.Config](di), do.MustInvoke[*storage.Store](di), do.MustInvoke[*storage.ConfigStore](di), do.MustInvoke[runtimes.RuntimeProvider](di), do.MustInvoke[*sessions.SessionStreamBroker](di)),
		Images:             imageBackends.docker,
		LLM:                do.MustInvoke[*llmpkg.LLMClient](di),
		CapabilityProvider: do.MustInvoke[capabilities.Integration](di),
		Bus:                do.MustInvoke[*bus.LoaderBus](di),
		Streams:            do.MustInvoke[*sessions.SessionStreamBroker](di),
		Engine:             do.MustInvoke[loaders.LoaderEngine](di),
		Sessions:           do.MustInvoke[*sessions.SessionRPCBridge](di),
		Dashboard:          mustDashboardHub(di),
	})
	if err != nil {
		return nil, err
	}
	return manager, nil
}

func mustDashboardHub(di do.Injector) *dashboard.DashboardOverviewHub {
	dashboard, _ := do.Invoke[*dashboard.DashboardOverviewHub](di)
	return dashboard
}

func newProjectService(di do.Injector) (*projects.Service, error) {
	imageBackends, err := imageBackendsFromDI(di)
	if err != nil {
		return nil, err
	}
	return newProjectServiceFromDeps(&Service{
		config:    do.MustInvoke[*appconfig.Config](di),
		store:     do.MustInvoke[*storage.Store](di),
		configDB:  do.MustInvoke[*storage.ConfigStore](di),
		driver:    do.MustInvoke[runtimes.Driver](di),
		executor:  do.MustInvoke[*executor.Executor](di),
		images:    imageBackends.docker,
		loaders:   do.MustInvoke[*loaders.LoaderManager](di),
		cap:       do.MustInvoke[capabilities.Integration](di),
		bus:       do.MustInvoke[*bus.LoaderBus](di),
		streams:   do.MustInvoke[*sessions.SessionStreamBroker](di),
		dashboard: mustDashboardHub(di),
	}), nil
}

func newProjectServiceFromDeps(s *Service) *projects.Service {
	deps := projectServiceDeps(s)
	return projects.NewService(deps)
}

func projectServiceDeps(s *Service) projects.ServiceDeps {
	if s == nil {
		return projects.ServiceDeps{}
	}
	return projects.ServiceDeps{
		Config:    s.config,
		Store:     s.store,
		ConfigDB:  s.configDB,
		Driver:    s.driver,
		Executor:  s.executor,
		Images:    s.images,
		Loaders:   s.loaders,
		Cap:       s.cap,
		Bus:       s.bus,
		Streams:   s.streams,
		Dashboard: s.dashboard,
	}
}
