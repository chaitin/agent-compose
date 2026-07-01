package sessions

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type Executor interface {
	ExecuteCell(ctx context.Context, session *Session, cellType, source string) (NotebookCell, error)
	ExecuteCellStream(ctx context.Context, session *Session, cellType, source string, stream CellExecutionStream) (NotebookCell, error)
	ExecuteAgentRequest(ctx context.Context, session *Session, request ExecuteAgentRequest) (NotebookCell, SessionEvent, SessionEvent, error)
}

type AgentExecutionConfig struct {
	Provider          string
	AgentDefinitionID string
	Model             string
	EnvItems          []SessionEnvVar
}

type AgentConfigResolver func(ctx context.Context, session *Session, requested string) AgentExecutionConfig

type Service struct {
	store              *Store
	executor           Executor
	bus                *LoaderBus
	streams            *SessionStreamBroker
	sessions           *SessionRPCBridge
	resolveAgentConfig AgentConfigResolver
}

func NewService(store *Store, executor Executor, bus *LoaderBus, streams *SessionStreamBroker, sessions *SessionRPCBridge, resolver AgentConfigResolver) *Service {
	return &Service{
		store:              store,
		executor:           executor,
		bus:                bus,
		streams:            streams,
		sessions:           sessions,
		resolveAgentConfig: resolver,
	}
}

func (s *Service) CreateSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessions.CreateSession(ctx, req)
}

func (s *Service) WatchSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest], stream *connect.ServerStream[agentcomposev1.WatchSessionResponse]) error {
	prepareStreamingHeaders(stream.ResponseHeader())
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if sendErr := stream.Send(&agentcomposev1.WatchSessionResponse{
		EventType: agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_SESSION_UPDATED,
		Session:   toProtoSessionSummary(&session.Summary),
	}); sendErr != nil {
		return connect.NewError(connect.CodeUnknown, sendErr)
	}
	events, cancel := s.streams.Subscribe(session.Summary.ID)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if sendErr := stream.Send(toProtoWatchSessionResponse(event)); sendErr != nil {
				return connect.NewError(connect.CodeUnknown, sendErr)
			}
		}
	}
}

func (s *Service) ResumeSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessions.ResumeSession(ctx, req)
}

func (s *Service) StopSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessions.StopSession(ctx, req)
}

func (s *Service) GetSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessions.GetSession(ctx, req)
}

func (s *Service) ListSessions(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
	return s.sessions.ListSessions(ctx, req)
}

func (s *Service) EnsureSessionProxyReady(ctx context.Context, sessionID string) (*Session, ProxyState, error) {
	return s.sessions.EnsureSessionProxyReady(ctx, sessionID)
}

func (s *Service) GetSessionProxy(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionProxyResponse], error) {
	return s.sessions.GetSessionProxy(ctx, req)
}

func (s *Service) ExecuteCell(ctx context.Context, req *connect.Request[agentcomposev1.ExecuteCellRequest]) (*connect.Response[agentcomposev1.ExecuteCellResponse], error) {
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is not running"))
	}
	cell, err := s.executor.ExecuteCell(ctx, session, fromProtoCellType(req.Msg.GetType()), req.Msg.GetSource())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	loaded, err := s.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.publishLoaderTopic("agent-compose.cell.completed", cellTopicPayload(session.Summary.ID, cell, "api"))
	return connect.NewResponse(&agentcomposev1.ExecuteCellResponse{Session: toProtoSessionSummary(&loaded.Summary), Cell: toProtoCell(cell)}), nil
}

func (s *Service) ExecuteCellStream(ctx context.Context, req *connect.Request[agentcomposev1.ExecuteCellRequest], stream *connect.ServerStream[agentcomposev1.ExecuteCellStreamResponse]) error {
	prepareStreamingHeaders(stream.ResponseHeader())
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != VMStatusRunning {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is not running"))
	}

	streamErr := func(sendErr error) error {
		if sendErr == nil {
			return nil
		}
		return connect.NewError(connect.CodeUnknown, sendErr)
	}
	cell, err := s.executor.ExecuteCellStream(ctx, session, fromProtoCellType(req.Msg.GetType()), req.Msg.GetSource(), CellExecutionStream{
		OnStart: func(cell NotebookCell) error {
			return streamErr(stream.Send(&agentcomposev1.ExecuteCellStreamResponse{
				EventType: agentcomposev1.ExecuteCellStreamEventType_EXECUTE_CELL_STREAM_EVENT_TYPE_STARTED,
				Session:   toProtoSessionSummary(&session.Summary),
				Cell:      toProtoCell(cell),
				CellId:    cell.ID,
			}))
		},
		OnChunk: func(cellID string, chunk ExecChunk) error {
			if chunk.Text == "" {
				return nil
			}
			return streamErr(stream.Send(&agentcomposev1.ExecuteCellStreamResponse{
				EventType: agentcomposev1.ExecuteCellStreamEventType_EXECUTE_CELL_STREAM_EVENT_TYPE_OUTPUT,
				CellId:    cellID,
				Chunk:     chunk.Text,
				IsStderr:  chunk.IsStderr,
			}))
		},
	})
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	loaded, err := s.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	s.publishLoaderTopic("agent-compose.cell.completed", cellTopicPayload(session.Summary.ID, cell, "api"))
	return streamErr(stream.Send(&agentcomposev1.ExecuteCellStreamResponse{
		EventType: agentcomposev1.ExecuteCellStreamEventType_EXECUTE_CELL_STREAM_EVENT_TYPE_COMPLETED,
		Session:   toProtoSessionSummary(&loaded.Summary),
		Cell:      toProtoCell(cell),
		CellId:    cell.ID,
	}))
}

func (s *Service) ListCells(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.ListCellsResponse], error) {
	cells, err := s.store.ListCells(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListCellsResponse{SessionId: req.Msg.GetSessionId()}
	for _, cell := range cells {
		resp.Cells = append(resp.Cells, toProtoCell(cell))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) SendAgentMessage(ctx context.Context, req *connect.Request[agentcomposev1.SendAgentMessageRequest]) (*connect.Response[agentcomposev1.SendAgentMessageResponse], error) {
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is not running"))
	}
	message := strings.TrimSpace(req.Msg.GetMessage())
	if message == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message is required"))
	}
	agentConfig := s.agentConfig(ctx, session, req.Msg.GetAgent())
	cell, userEvent, assistantEvent, err := s.executor.ExecuteAgentRequest(ctx, session, ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: agentConfig.AgentDefinitionID,
		Model:             agentConfig.Model,
		ProviderEnvItems:  agentConfig.EnvItems,
		Message:           message,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.publishLoaderTopic("agent-compose.agent.completed", cellTopicPayload(session.Summary.ID, cell, "api"))
	return connect.NewResponse(&agentcomposev1.SendAgentMessageResponse{UserEvent: toProtoEvent(userEvent), AssistantEvent: toProtoEvent(assistantEvent)}), nil
}

func (s *Service) SendAgentMessageStream(ctx context.Context, req *connect.Request[agentcomposev1.SendAgentMessageRequest], stream *connect.ServerStream[agentcomposev1.SendAgentMessageStreamResponse]) error {
	prepareStreamingHeaders(stream.ResponseHeader())
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != VMStatusRunning {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is not running"))
	}
	message := strings.TrimSpace(req.Msg.GetMessage())
	if message == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message is required"))
	}
	agentConfig := s.agentConfig(ctx, session, req.Msg.GetAgent())

	streamErr := func(sendErr error) error {
		if sendErr == nil {
			return nil
		}
		return connect.NewError(connect.CodeUnknown, sendErr)
	}

	cell, userEvent, assistantEvent, err := s.executor.ExecuteAgentRequest(ctx, session, ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: agentConfig.AgentDefinitionID,
		Model:             agentConfig.Model,
		ProviderEnvItems:  agentConfig.EnvItems,
		Message:           message,
		Stream: AgentExecutionStream{
			OnStart: func(cell NotebookCell) error {
				return streamErr(stream.Send(&agentcomposev1.SendAgentMessageStreamResponse{
					EventType: agentcomposev1.SendAgentMessageStreamEventType_SEND_AGENT_MESSAGE_STREAM_EVENT_TYPE_STARTED,
					Session:   toProtoSessionSummary(&session.Summary),
					Run:       toProtoAgentRun(cell),
					RunId:     cell.ID,
				}))
			},
			OnChunk: func(cellID string, chunk ExecChunk) error {
				return streamErr(stream.Send(&agentcomposev1.SendAgentMessageStreamResponse{
					EventType: agentcomposev1.SendAgentMessageStreamEventType_SEND_AGENT_MESSAGE_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     cellID,
					Chunk:     chunk.Text,
					IsStderr:  chunk.IsStderr,
				}))
			},
		},
	})
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	loaded, err := s.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	s.publishLoaderTopic("agent-compose.agent.completed", cellTopicPayload(session.Summary.ID, cell, "api"))
	return streamErr(stream.Send(&agentcomposev1.SendAgentMessageStreamResponse{
		EventType:      agentcomposev1.SendAgentMessageStreamEventType_SEND_AGENT_MESSAGE_STREAM_EVENT_TYPE_COMPLETED,
		Session:        toProtoSessionSummary(&loaded.Summary),
		Run:            toProtoAgentRun(cell),
		RunId:          cell.ID,
		UserEvent:      toProtoEvent(userEvent),
		AssistantEvent: toProtoEvent(assistantEvent),
	}))
}

func (s *Service) ListSessionEvents(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.ListSessionEventsResponse], error) {
	events, err := s.store.ListEvents(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListSessionEventsResponse{SessionId: req.Msg.GetSessionId()}
	for _, event := range events {
		resp.Events = append(resp.Events, toProtoEvent(event))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) publishLoaderTopic(topic string, payload map[string]any) {
	if s == nil || s.bus == nil {
		return
	}
	s.bus.Publish(LoaderTopicEvent{
		Topic:     topic,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
}

func (s *Service) agentConfig(ctx context.Context, session *Session, requested string) AgentExecutionConfig {
	if s == nil || s.resolveAgentConfig == nil {
		return AgentExecutionConfig{Provider: strings.TrimSpace(requested)}
	}
	return s.resolveAgentConfig(ctx, session, requested)
}

func prepareStreamingHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	headers.Set("Cache-Control", "no-cache, no-transform")
	headers.Set("X-Accel-Buffering", "no")
}
