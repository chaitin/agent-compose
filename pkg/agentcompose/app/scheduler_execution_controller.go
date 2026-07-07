package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/samber/do/v2"

	"agent-compose/pkg/agentcompose/adapters"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/events/webhooks"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	"agent-compose/pkg/storage/configstore"
)

func NewSchedulerExecutionController(di do.Injector) (*loaders.Controller, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	queue, err := webhooks.NewRunQueueFromConfig(config)
	if err != nil {
		return nil, err
	}
	var controller *loaders.Controller
	configDB := do.MustInvoke[*configstore.ConfigStore](di)
	bus := do.MustInvoke[*loaders.Bus](di)
	var notifier loaders.ControllerNotifier
	if hub, err := do.Invoke[*dashboard.Hub](di); err == nil {
		notifier = hub
	}
	controller = loaders.NewController(loaders.ControllerDependencies{
		RootCtx: do.MustInvoke[context.Context](di),
		RunTimeout: func(override time.Duration) time.Duration {
			if override > 0 {
				return override
			}
			if config.LoaderRunTimeout > 0 {
				return config.LoaderRunTimeout
			}
			return 20 * time.Minute
		},
		Store:     configDB,
		Engine:    do.MustInvoke[loaders.LoaderEngine](di),
		Publisher: bus,
		Notifier:  notifier,
		Artifacts: loaders.FSArtifacts{DataRoot: config.DataRoot},
		ReserveSlots: func(event domain.LoaderTopicEvent, count int) ([]*webhooks.Reservation, bool) {
			return reserveLoaderEventQueueSlots(config, &queue, event, count)
		},
		HostFactory: func(loader domain.Loader, run *domain.LoaderRunSummary, triggerEvent loaders.TriggerEventMetadata) loaders.RunHost {
			return loaders.NewRuntimeHost(loaders.RunHostDependencies{
				Store:                   configDB,
				Events:                  adapters.LoaderHostEvents{Controller: controller},
				Sessions:                do.MustInvoke[*adapters.LoaderSessionRunner](di),
				AgentDefinitions:        do.MustInvoke[*adapters.LoaderSessionRunner](di),
				AgentExecutor:           adapters.LoaderHostAgentExecutor{Executor: do.MustInvoke[*adapters.AgentExecutor](di)},
				CommandExecutor:         adapters.LoaderHostCommandExecutor{Executor: do.MustInvoke[*adapters.LoaderCommandExecutor](di)},
				ProjectAgentRunner:      loaderProjectAgentRunner{controller: do.MustInvoke[*runs.Controller](di)},
				LLM:                     adapters.LoaderHostLLMRunner{Client: do.MustInvoke[*adapters.LLMClient](di)},
				SessionRPC:              do.MustInvoke[*adapters.SessionRPCBridge](di),
				Publisher:               controller,
				CommandRequiresCleanup:  loaders.CommandRequestRequiresCleanup,
				LinkedSessionIDFromJSON: adapters.LoaderSessionRPCLinkedSessionID,
			}, loader, run, triggerEvent)
		},
	})
	return controller, nil
}

func reserveLoaderEventQueueSlots(config *appconfig.Config, queue **webhooks.RunQueue, event domain.LoaderTopicEvent, count int) ([]*webhooks.Reservation, bool) {
	if count <= 0 {
		return nil, true
	}
	if event.Source != domain.TopicEventSourceWebhook {
		return webhooks.NoopReservations(count), true
	}
	if *queue == nil {
		next, err := webhooks.NewRunQueueFromConfig(config)
		if err != nil {
			slog.Warn("failed to initialize webhook queue config", "error", err)
			next = webhooks.NewRunQueue(0)
		}
		*queue = next
	}
	reservations := make([]*webhooks.Reservation, 0, count)
	for i := 0; i < count; i++ {
		reservation, ok := (*queue).Reserve(event)
		if !ok {
			for _, reserved := range reservations {
				reserved.Release()
			}
			return nil, false
		}
		reservations = append(reservations, reservation)
	}
	return reservations, true
}

type loaderProjectAgentRunner struct {
	controller *runs.Controller
}

func (r loaderProjectAgentRunner) RunProjectAgent(ctx context.Context, request loaders.HostProjectAgentRequest) (domain.ProjectRunRecord, error, error) {
	run, execErr, err := r.controller.RunProjectAgent(ctx, runs.RunAgentRequest{
		ProjectID:        request.ProjectID,
		AgentName:        request.AgentName,
		Prompt:           request.Prompt,
		Source:           domain.ProjectRunSourceScheduler,
		SchedulerID:      request.SchedulerID,
		TriggerID:        request.TriggerID,
		OutputSchemaJSON: request.OutputSchemaJSON,
		ClientRequestID:  request.ClientRequestID,
	}, nil)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	return run, execErr, nil
}
