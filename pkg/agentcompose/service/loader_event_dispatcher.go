package agentcompose

import (
	"context"
	"log/slog"

	"agent-compose/pkg/events/webhooks"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

type LoaderEventDispatcher struct {
	manager *LoaderManager
	core    *loaders.EventDispatcher
}

func NewLoaderEventDispatcher(manager *LoaderManager) *LoaderEventDispatcher {
	dispatcher := &LoaderEventDispatcher{manager: manager}
	dispatcher.core = loaders.NewEventDispatcher(loaders.EventDispatcherDependencies{
		RootCtx:      manager.rootCtx,
		Store:        manager.configDB,
		Targets:      dispatcher.collectTargets,
		IsBusy:       dispatcher.shouldRetryForBusyTargets,
		ReserveSlots: dispatcher.reserveQueueSlots,
		Run: func(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options loaders.RunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error) {
			return manager.runLoader(ctx, loader, trigger, payloadJSON, source, true, loaderRunOptions{retryWhenBusy: options.RetryWhenBusy, alreadyEntered: options.AlreadyEntered}, triggerEventAck...)
		},
		Prepare: func(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options loaders.RunOptions) (loaders.PreparedRun, error) {
			prepared, err := manager.prepareLoaderRun(ctx, loader, trigger, payloadJSON, source, loaderRunOptions{retryWhenBusy: options.RetryWhenBusy, alreadyEntered: options.AlreadyEntered})
			return prepared.toCore(), err
		},
		Execute: func(ctx context.Context, prepared loaders.PreparedRun) (domain.LoaderRunSummary, error) {
			return manager.executePreparedLoaderRun(ctx, preparedLoaderRunFromCore(prepared))
		},
		Abort: func(ctx context.Context, prepared loaders.PreparedRun, reason string) {
			manager.abortPreparedLoaderRun(ctx, preparedLoaderRunFromCore(prepared), reason)
		},
		RunTimeout: manager.loaderRunTimeout,
		EnterRun:   manager.enterRun,
		LeaveRun:   manager.leaveRun,
	})
	return dispatcher
}

func (d *LoaderEventDispatcher) Dispatch(event domain.LoaderTopicEvent) {
	d.core.Dispatch(event)
}

func (d *LoaderEventDispatcher) collectTargets(topic string) []loaders.EventTarget {
	return loaders.CollectEventTargets(d.manager.snapshotLoaders(), topic)
}

func (d *LoaderEventDispatcher) shouldRetryForBusyTargets(targets []loaders.EventTarget) bool {
	m := d.manager
	m.mu.RLock()
	defer m.mu.RUnlock()
	return loaders.AnyTargetBusy(targets, m.running)
}

func (d *LoaderEventDispatcher) reserveQueueSlots(event domain.LoaderTopicEvent, count int) ([]*webhooks.Reservation, bool) {
	m := d.manager
	if count <= 0 {
		return nil, true
	}
	if event.Source != domain.TopicEventSourceWebhook {
		return webhooks.NoopReservations(count), true
	}
	if m.eventQueue == nil {
		queue, err := webhooks.NewRunQueueFromConfig(m.config)
		if err != nil {
			slog.Warn("failed to initialize webhook queue config", "error", err)
			queue = webhooks.NewRunQueue(0)
		}
		m.eventQueue = queue
	}
	reservations := make([]*webhooks.Reservation, 0, count)
	for i := 0; i < count; i++ {
		reservation, ok := m.eventQueue.Reserve(event)
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
