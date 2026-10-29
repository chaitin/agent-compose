package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/samber/do/v2"

	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/dashboard"
	"github.com/chaitin/agent-compose/pkg/events/webhooks"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func NewSchedulerController(di do.Injector) (*schedulers.Controller, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	queue, err := webhooks.NewRunQueueFromConfig(config)
	if err != nil {
		return nil, err
	}
	var controller *schedulers.Controller
	configDB := do.MustInvoke[*configstore.ConfigStore](di)
	bus := do.MustInvoke[*schedulers.Bus](di)
	var notifier schedulers.ControllerNotifier
	if hub, err := do.Invoke[*dashboard.Hub](di); err == nil {
		notifier = hub
	}
	// The default keeps events within the publisher's Project. A deployment
	// that relied on daemon-wide delivery sees its cross-Project events
	// acknowledged as having no subscriber, so the effective scope is logged
	// where an operator will look first.
	slog.Info("scheduler event delivery scope", "scope", config.EventDeliveryScope, "env", "EVENT_DELIVERY_SCOPE")
	controller = schedulers.NewController(schedulers.ControllerDependencies{
		RootCtx: do.MustInvoke[context.Context](di),
		RunTimeout: func(override time.Duration) time.Duration {
			if override > 0 {
				return override
			}
			if config.SchedulerRunTimeout > 0 {
				return config.SchedulerRunTimeout
			}
			return 20 * time.Minute
		},
		Store:              configDB,
		EventDeliveryScope: config.EventDeliveryScope,
		Engine:             do.MustInvoke[schedulers.SchedulerEngine](di),
		Publisher:          bus,
		Notifier:           notifier,
		Artifacts:          schedulers.FSArtifacts{DataRoot: config.DataRoot},
		// Resolved here, not inside HostFactory, so startup recovery can stop
		// sandboxes before any run builds a host.
		InterruptedSandboxes: do.MustInvoke[*adapters.SchedulerSandboxRunner](di),
		ReserveSlots: func(event domain.SchedulerTopicEvent, count int) ([]*webhooks.Reservation, bool) {
			return reserveSchedulerEventQueueSlots(config, &queue, event, count)
		},
		HostFactory: func(scheduler domain.Scheduler, execution schedulers.RuntimeExecutionContext, triggerEvent schedulers.TriggerEventMetadata) schedulers.RunHost {
			events := schedulers.HostEventRecorder(nil)
			if execution.Kind == schedulers.ExecutionKindTrigger {
				events = adapters.SchedulerHostEvents{Controller: controller}
			}
			return schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
				Store:                   configDB,
				Events:                  events,
				Sessions:                do.MustInvoke[*adapters.SchedulerSandboxRunner](di),
				AgentDefinitions:        do.MustInvoke[*adapters.SchedulerSandboxRunner](di),
				AgentExecutor:           adapters.SchedulerHostAgentExecutor{Executor: do.MustInvoke[*adapters.AgentExecutor](di)},
				CommandExecutor:         adapters.SchedulerHostCommandExecutor{Executor: do.MustInvoke[*adapters.SchedulerCommandExecutor](di)},
				ProjectAgentRunner:      schedulerProjectAgentRunner{controller: do.MustInvoke[*runs.Controller](di)},
				LLM:                     adapters.SchedulerHostLLMRunner{Client: do.MustInvoke[*adapters.LLMClient](di)},
				SandboxRPC:              do.MustInvoke[*adapters.SandboxRPCBridge](di),
				Publisher:               controller,
				CommandRequiresCleanup:  schedulers.CommandRequestRequiresCleanup,
				LinkedSandboxIDFromJSON: adapters.SchedulerSandboxRPCLinkedSandboxID,
			}, scheduler, execution, triggerEvent)
		},
	})
	return controller, nil
}

func reserveSchedulerEventQueueSlots(config *appconfig.Config, queue **webhooks.RunQueue, event domain.SchedulerTopicEvent, count int) ([]*webhooks.Reservation, bool) {
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

type schedulerProjectAgentRunner struct {
	controller *runs.Controller
}

func (r schedulerProjectAgentRunner) RunProjectAgent(ctx context.Context, request schedulers.HostProjectAgentRequest) (domain.ProjectRunRecord, error, error) {
	cleanupPolicy := agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_UNSPECIFIED
	stickySchedulerID := ""
	stickyTriggerID := ""
	if schedulers.NormalizeSandboxPolicy(request.SandboxPolicy) == domain.SchedulerSandboxPolicySticky {
		cleanupPolicy = agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING
		stickySchedulerID = request.SchedulerID
		stickyTriggerID = request.TriggerID
	}
	run, execErr, err := r.controller.RunProjectAgent(ctx, runs.RunAgentRequest{
		ProjectID:                request.ProjectID,
		AgentName:                request.AgentName,
		Prompt:                   request.Prompt,
		Source:                   domain.ProjectRunSourceScheduler,
		SchedulerID:              request.ProjectSchedulerID,
		SchedulerRunID:           request.SchedulerRunID,
		TriggerID:                request.TriggerID,
		OutputSchemaJSON:         request.OutputSchemaJSON,
		ClientRequestID:          request.ClientRequestID,
		Volumes:                  request.Volumes,
		CleanupPolicy:            cleanupPolicy,
		StickyBindingSchedulerID: stickySchedulerID,
		StickyBindingTriggerID:   stickyTriggerID,
		StickyBindingConfigHash:  request.SandboxConfigHash,
	}, nil)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	return run, execErr, nil
}
