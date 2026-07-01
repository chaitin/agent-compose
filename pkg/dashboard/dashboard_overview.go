package dashboard

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const dashboardOverviewPageSize = 20

type DashboardOverviewAggregator struct {
	store    *storage.Store
	configDB *storage.ConfigStore
	clock    func() time.Time
}

func NewDashboardOverviewAggregator(di do.Injector) (*DashboardOverviewAggregator, error) {
	return NewAggregator(do.MustInvoke[*storage.Store](di), do.MustInvoke[*storage.ConfigStore](di)), nil
}

func NewAggregator(store *storage.Store, configDB *storage.ConfigStore) *DashboardOverviewAggregator {
	return &DashboardOverviewAggregator{
		store:    store,
		configDB: configDB,
		clock:    func() time.Time { return time.Now().UTC() },
	}
}

func (a *DashboardOverviewAggregator) Build(ctx context.Context) (*agentcomposev1.DashboardOverview, error) {
	sessions, err := a.store.ListSessions(ctx, model.SessionListOptions{Limit: dashboardOverviewPageSize})
	if err != nil {
		return nil, err
	}
	runs, err := a.configDB.ListRecentLoaderRuns(ctx, dashboardOverviewPageSize)
	if err != nil {
		return nil, err
	}

	overview := &agentcomposev1.DashboardOverview{
		Runs:      &agentcomposev1.RunOverview{},
		UpdatedAt: a.clock().Format(time.RFC3339Nano),
	}
	overview.Runs.RecentCount = uint32(len(sessions.Sessions) + len(runs))
	for _, session := range sessions.Sessions {
		status := ""
		if session != nil {
			status = session.Summary.VMStatus
		}
		if isDashboardRunningStatus(status) {
			overview.Runs.RunningCount++
		}
		if isDashboardAttentionStatus(status) {
			overview.Runs.AttentionCount++
		}
	}
	for _, run := range runs {
		if isDashboardRunningStatus(run.Status) {
			overview.Runs.RunningCount++
		}
		if isDashboardAttentionStatus(run.Status) {
			overview.Runs.AttentionCount++
		}
	}
	return overview, nil
}

type DashboardOverviewHub struct {
	ctx        context.Context
	cancel     context.CancelFunc
	aggregator *DashboardOverviewAggregator
	debounce   time.Duration
	notifyCh   chan string

	mu          sync.RWMutex
	current     *agentcomposev1.DashboardOverview
	subscribers map[chan DashboardOverviewEvent]struct{}
}

type DashboardOverviewEvent struct {
	Overview *agentcomposev1.DashboardOverview
	Reason   string
}

func NewDashboardOverviewHub(di do.Injector) (*DashboardOverviewHub, error) {
	ctx := do.MustInvoke[context.Context](di)
	if ctx == nil {
		ctx = context.Background()
	}
	aggregator := do.MustInvoke[*DashboardOverviewAggregator](di)
	return NewHub(ctx, aggregator, 250*time.Millisecond), nil
}

func NewHub(ctx context.Context, aggregator *DashboardOverviewAggregator, debounce time.Duration) *DashboardOverviewHub {
	hubCtx, cancel := context.WithCancel(ctx)
	hub := &DashboardOverviewHub{
		ctx:         hubCtx,
		cancel:      cancel,
		aggregator:  aggregator,
		debounce:    debounce,
		notifyCh:    make(chan string, 1),
		subscribers: make(map[chan DashboardOverviewEvent]struct{}),
	}
	go hub.run()
	return hub
}

func (h *DashboardOverviewHub) SetDebounceForTest(debounce time.Duration) {
	if h == nil {
		return
	}
	h.debounce = debounce
}

func (h *DashboardOverviewHub) Current(ctx context.Context) (*agentcomposev1.DashboardOverview, error) {
	h.mu.RLock()
	current := h.current
	h.mu.RUnlock()
	if current != nil {
		return cloneDashboardOverview(current), nil
	}
	overview, err := h.aggregator.Build(ctx)
	if err != nil {
		return nil, err
	}
	h.setCurrent(overview)
	return cloneDashboardOverview(overview), nil
}

func (h *DashboardOverviewHub) Notify(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "updated"
	}
	select {
	case h.notifyCh <- reason:
	default:
	}
}

func (h *DashboardOverviewHub) Watch(ctx context.Context) (<-chan DashboardOverviewEvent, func()) {
	ch := make(chan DashboardOverviewEvent, 8)
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

func (h *DashboardOverviewHub) run() {
	for {
		select {
		case <-h.ctx.Done():
			h.closeSubscribers()
			return
		case reason := <-h.notifyCh:
			timer := time.NewTimer(h.debounce)
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
			h.broadcast(DashboardOverviewEvent{Overview: overview, Reason: latestReason})
		}
	}
}

func (h *DashboardOverviewHub) setCurrent(overview *agentcomposev1.DashboardOverview) {
	h.mu.Lock()
	h.current = cloneDashboardOverview(overview)
	h.mu.Unlock()
}

func (h *DashboardOverviewHub) broadcast(event DashboardOverviewEvent) {
	event.Overview = cloneDashboardOverview(event.Overview)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func (h *DashboardOverviewHub) closeSubscribers() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		delete(h.subscribers, ch)
		close(ch)
	}
}

type Service struct {
	dashboard *DashboardOverviewHub
}

func NewService(hub *DashboardOverviewHub) *Service {
	return &Service{dashboard: hub}
}

func (s *Service) GetDashboardOverview(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.DashboardOverviewResponse], error) {
	_ = req
	overview, err := s.dashboard.Current(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev1.DashboardOverviewResponse{Overview: overview}), nil
}

func (s *Service) WatchDashboardOverview(ctx context.Context, req *connect.Request[emptypb.Empty], stream *connect.ServerStream[agentcomposev1.DashboardOverviewEvent]) error {
	_ = req
	prepareStreamingHeaders(stream.ResponseHeader())
	overview, err := s.dashboard.Current(ctx)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if err := stream.Send(&agentcomposev1.DashboardOverviewEvent{Overview: overview, Reason: "initial"}); err != nil {
		return connect.NewError(connect.CodeUnknown, err)
	}
	events, cancel := s.dashboard.Watch(ctx)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := stream.Send(&agentcomposev1.DashboardOverviewEvent{Overview: event.Overview, Reason: event.Reason}); err != nil {
				return connect.NewError(connect.CodeUnknown, err)
			}
		}
	}
}

func isDashboardRunningStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case model.VMStatusPending, model.VMStatusRunning:
		return true
	default:
		return false
	}
}

func isDashboardAttentionStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case model.VMStatusFailed, "SKIPPED", "CANCELED", "CANCELLED":
		return true
	default:
		return false
	}
}

func prepareStreamingHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	headers.Set("Cache-Control", "no-cache, no-transform")
	headers.Set("X-Accel-Buffering", "no")
}

func cloneDashboardOverview(item *agentcomposev1.DashboardOverview) *agentcomposev1.DashboardOverview {
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
