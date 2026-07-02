package agentcompose

import (
	dashboarddomain "agent-compose/internal/agentcompose/dashboard"
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const dashboardOverviewPageSize = dashboarddomain.OverviewPageSize

type DashboardOverviewAggregator struct {
	store    *Store
	configDB *ConfigStore
	clock    func() time.Time
}

func NewDashboardOverviewAggregator(di do.Injector) (*DashboardOverviewAggregator, error) {
	return newDashboardOverviewAggregator(do.MustInvoke[*Store](di), do.MustInvoke[*ConfigStore](di)), nil
}

func newDashboardOverviewAggregator(store *Store, configDB *ConfigStore) *DashboardOverviewAggregator {
	return &DashboardOverviewAggregator{
		store:    store,
		configDB: configDB,
		clock:    func() time.Time { return time.Now().UTC() },
	}
}

func (a *DashboardOverviewAggregator) Build(ctx context.Context) (*agentcomposev1.DashboardOverview, error) {
	sessions, err := a.store.ListSessions(ctx, SessionListOptions{Limit: dashboardOverviewPageSize})
	if err != nil {
		return nil, err
	}
	runs, err := a.configDB.ListRecentLoaderRuns(ctx, dashboardOverviewPageSize)
	if err != nil {
		return nil, err
	}

	input := dashboarddomain.OverviewInput{
		Sessions: make([]dashboarddomain.RunSource, 0, len(sessions.Sessions)),
		Runs:     make([]dashboarddomain.RunSource, 0, len(runs)),
		Now:      a.clock(),
	}
	for _, session := range sessions.Sessions {
		status := ""
		if session != nil {
			status = session.Summary.VMStatus
		}
		input.Sessions = append(input.Sessions, dashboarddomain.RunSource{Status: status})
	}
	for _, run := range runs {
		input.Runs = append(input.Runs, dashboarddomain.RunSource{Status: run.Status})
	}
	return dashboarddomain.BuildOverview(input), nil
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
	return newDashboardOverviewHub(ctx, aggregator, 250*time.Millisecond), nil
}

func newDashboardOverviewHub(ctx context.Context, aggregator *DashboardOverviewAggregator, debounce time.Duration) *DashboardOverviewHub {
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

func cloneDashboardOverview(item *agentcomposev1.DashboardOverview) *agentcomposev1.DashboardOverview {
	return dashboarddomain.CloneOverview(item)
}
