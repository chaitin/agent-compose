package dashboard

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	domain "agent-compose/pkg/model"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const OverviewPageSize = 20

type SessionStore interface {
	ListSessions(context.Context, domain.SessionListOptions) (domain.SessionListResult, error)
}

type LoaderRunStore interface {
	ListRecentLoaderRuns(context.Context, int) ([]domain.LoaderRunSummary, error)
}

type Aggregator struct {
	store    SessionStore
	configDB LoaderRunStore
	clock    func() time.Time
}

func NewAggregator(store SessionStore, configDB LoaderRunStore) *Aggregator {
	return &Aggregator{
		store:    store,
		configDB: configDB,
		clock:    func() time.Time { return time.Now().UTC() },
	}
}

func (a *Aggregator) SetClock(clock func() time.Time) {
	if a == nil {
		return
	}
	a.clock = clock
}

func (a *Aggregator) Build(ctx context.Context) (*agentcomposev1.DashboardOverview, error) {
	sessions, err := a.store.ListSessions(ctx, domain.SessionListOptions{Limit: OverviewPageSize})
	if err != nil {
		return nil, err
	}
	runs, err := a.configDB.ListRecentLoaderRuns(ctx, OverviewPageSize)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC
	if a.clock != nil {
		now = a.clock
	}
	overview := &agentcomposev1.DashboardOverview{
		Runs:      &agentcomposev1.RunOverview{},
		UpdatedAt: now().Format(time.RFC3339Nano),
	}
	overview.Runs.RecentCount = uint32(len(sessions.Sessions) + len(runs))
	for _, session := range sessions.Sessions {
		status := ""
		if session != nil {
			status = session.Summary.VMStatus
		}
		if IsRunningStatus(status) {
			overview.Runs.RunningCount++
		}
		if IsAttentionStatus(status) {
			overview.Runs.AttentionCount++
		}
	}
	for _, run := range runs {
		if IsRunningStatus(run.Status) {
			overview.Runs.RunningCount++
		}
		if IsAttentionStatus(run.Status) {
			overview.Runs.AttentionCount++
		}
	}
	return overview, nil
}

type Hub struct {
	ctx        context.Context
	cancel     context.CancelFunc
	aggregator *Aggregator
	debounce   time.Duration
	notifyCh   chan string

	mu          sync.RWMutex
	current     *agentcomposev1.DashboardOverview
	subscribers map[chan Event]struct{}
}

type Event struct {
	Overview *agentcomposev1.DashboardOverview
	Reason   string
}

func NewHub(ctx context.Context, aggregator *Aggregator, debounce time.Duration) *Hub {
	if ctx == nil {
		ctx = context.Background()
	}
	if debounce <= 0 {
		debounce = 250 * time.Millisecond
	}
	hubCtx, cancel := context.WithCancel(ctx)
	hub := &Hub{
		ctx:         hubCtx,
		cancel:      cancel,
		aggregator:  aggregator,
		debounce:    debounce,
		notifyCh:    make(chan string, 1),
		subscribers: make(map[chan Event]struct{}),
	}
	go hub.run()
	return hub
}

func (h *Hub) SetDebounce(debounce time.Duration) {
	if h == nil || debounce <= 0 {
		return
	}
	h.mu.Lock()
	h.debounce = debounce
	h.mu.Unlock()
}

func (h *Hub) Current(ctx context.Context) (*agentcomposev1.DashboardOverview, error) {
	h.mu.RLock()
	current := h.current
	h.mu.RUnlock()
	if current != nil {
		return CloneOverview(current), nil
	}
	overview, err := h.aggregator.Build(ctx)
	if err != nil {
		return nil, err
	}
	h.setCurrent(overview)
	return CloneOverview(overview), nil
}

func (h *Hub) Notify(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "updated"
	}
	select {
	case h.notifyCh <- reason:
	default:
	}
}

func (h *Hub) Watch(ctx context.Context) (<-chan Event, func()) {
	ch := make(chan Event, 8)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		if _, ok := h.subscribers[ch]; ok {
			delete(h.subscribers, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ch, cancel
}

func (h *Hub) run() {
	for {
		select {
		case <-h.ctx.Done():
			h.closeSubscribers()
			return
		case reason := <-h.notifyCh:
			h.mu.RLock()
			debounce := h.debounce
			h.mu.RUnlock()
			timer := time.NewTimer(debounce)
			latestReason := reason
		collect:
			for {
				select {
				case <-h.ctx.Done():
					timer.Stop()
					h.closeSubscribers()
					return
				case latestReason = <-h.notifyCh:
				case <-timer.C:
					break collect
				}
			}
			overview, err := h.aggregator.Build(h.ctx)
			if err != nil {
				slog.Warn("failed to build dashboard overview", "reason", latestReason, "error", err)
				continue
			}
			h.setCurrent(overview)
			h.broadcast(Event{Overview: overview, Reason: latestReason})
		}
	}
}

func (h *Hub) setCurrent(overview *agentcomposev1.DashboardOverview) {
	h.mu.Lock()
	h.current = CloneOverview(overview)
	h.mu.Unlock()
}

func (h *Hub) broadcast(event Event) {
	event.Overview = CloneOverview(event.Overview)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func (h *Hub) closeSubscribers() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		delete(h.subscribers, ch)
		close(ch)
	}
}

func IsRunningStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case domain.VMStatusPending, domain.VMStatusRunning:
		return true
	default:
		return false
	}
}

func IsAttentionStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case domain.VMStatusFailed, "SKIPPED", "CANCELED", "CANCELLED":
		return true
	default:
		return false
	}
}

func CloneOverview(item *agentcomposev1.DashboardOverview) *agentcomposev1.DashboardOverview {
	if item == nil {
		return nil
	}
	clone := &agentcomposev1.DashboardOverview{UpdatedAt: item.GetUpdatedAt()}
	if item.GetRuns() != nil {
		clone.Runs = &agentcomposev1.RunOverview{
			RunningCount:   item.GetRuns().GetRunningCount(),
			RecentCount:    item.GetRuns().GetRecentCount(),
			AttentionCount: item.GetRuns().GetAttentionCount(),
		}
	}
	return clone
}
