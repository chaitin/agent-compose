package loader

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type recordingLoaderHost struct {
	sessionCalls []string
	requests     map[string]map[string]any
	agentPrompts []string
	agentCalls   []LoaderAgentRequest
	commandCalls []LoaderCommandRequest
	llmPrompts   []string
	llmCalls     []LoaderLLMRequest
	published    []string
}

func (h *recordingLoaderHost) Log(context.Context, string, any) error { return nil }

func (h *recordingLoaderHost) PublishEvent(_ context.Context, topic string, payloadJSON string) (TopicEventRecord, error) {
	h.published = append(h.published, topic+" "+payloadJSON)
	return TopicEventRecord{ID: "evt-test", Sequence: 1, Topic: topic, CorrelationID: "corr-test"}, nil
}

func (h *recordingLoaderHost) Agent(_ context.Context, prompt string, request LoaderAgentRequest) (LoaderAgentResult, error) {
	h.agentPrompts = append(h.agentPrompts, prompt)
	h.agentCalls = append(h.agentCalls, request)
	text := "agent-output"
	if strings.TrimSpace(request.OutputSchema) != "" {
		text = `{"summary":"ok","risk":"low"}`
	}
	return LoaderAgentResult{Text: text, Output: text, FinalText: text, SessionID: "agent-session", CellID: "agent-cell", Agent: firstNonEmpty(request.Agent, "codex"), AgentSessionID: "agent-runtime-session", StopReason: "completed", Success: true}, nil
}

func (h *recordingLoaderHost) Command(_ context.Context, request LoaderCommandRequest) (LoaderCommandResult, error) {
	h.commandCalls = append(h.commandCalls, request)
	return LoaderCommandResult{Stdout: "command-output", Output: "command-output", ExitCode: 0, Success: true, SessionID: "command-session", CellID: "command-cell", Artifacts: map[string]string{"stdout": "/tmp/stdout.txt"}}, nil
}

func (h *recordingLoaderHost) LLM(_ context.Context, prompt string, request LoaderLLMRequest) (LoaderLLMResult, error) {
	h.llmPrompts = append(h.llmPrompts, prompt)
	h.llmCalls = append(h.llmCalls, request)
	if strings.TrimSpace(request.OutputSchema) != "" {
		return LoaderLLMResult{Text: `{"summary":"ok","risk":"low"}`, Model: firstNonEmpty(request.Model, "gpt-5.4"), ResponseID: "resp-1", FinishReason: "completed"}, nil
	}
	return LoaderLLMResult{Text: "llm-output", Model: firstNonEmpty(request.Model, "gpt-5.4"), ResponseID: "resp-1", FinishReason: "completed"}, nil
}

func (h *recordingLoaderHost) StateGet(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (h *recordingLoaderHost) StateSet(context.Context, string, string) error { return nil }

func (h *recordingLoaderHost) StateDelete(context.Context, string) error { return nil }

func (h *recordingLoaderHost) CallSessionRPC(_ context.Context, method, requestJSON string) (string, error) {
	if h.requests == nil {
		h.requests = make(map[string]map[string]any)
	}
	h.sessionCalls = append(h.sessionCalls, method)
	if requestJSON != "" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(requestJSON), &payload); err != nil {
			return "", err
		}
		h.requests[method] = payload
	} else {
		h.requests[method] = nil
	}
	const sessionID = "session-from-host"
	switch method {
	case "CreateSession":
		return `{"session":{"summary":{"sessionId":"` + sessionID + `","title":"created","vmStatus":"RUNNING"}}}`, nil
	case "GetSession":
		return `{"session":{"summary":{"sessionId":"` + sessionID + `","title":"current","vmStatus":"RUNNING"}}}`, nil
	case "ListSessions":
		return `{"sessions":[{"sessionId":"` + sessionID + `","title":"listed","vmStatus":"RUNNING"}]}`, nil
	case "GetSessionProxy":
		return `{"sessionId":"` + sessionID + `","proxyPath":"/agent-compose/session/` + sessionID + `/lab","notebookUrl":"/agent-compose/session/` + sessionID + `/lab?token=test-token","driver":"boxlite","vmStatus":"RUNNING"}`, nil
	case "StopSession":
		return `{"session":{"summary":{"sessionId":"` + sessionID + `","title":"stopped","vmStatus":"STOPPED"}}}`, nil
	case "ResumeSession":
		return `{"session":{"summary":{"sessionId":"` + sessionID + `","title":"resumed","vmStatus":"RUNNING"}}}`, nil
	default:
		return "{}", nil
	}
}

type statefulRecordingLoaderHost struct {
	recordingLoaderHost
	state     map[string]string
	setValues []string
	deleted   []string
}

func (h *statefulRecordingLoaderHost) StateGet(_ context.Context, key string) (string, bool, error) {
	value, ok := h.state[key]
	return value, ok, nil
}

func (h *statefulRecordingLoaderHost) StateSet(_ context.Context, key string, value string) error {
	if h.state == nil {
		h.state = make(map[string]string)
	}
	h.state[key] = value
	h.setValues = append(h.setValues, value)
	return nil
}

func (h *statefulRecordingLoaderHost) StateDelete(_ context.Context, key string) error {
	delete(h.state, key)
	h.deleted = append(h.deleted, key)
	return nil
}

func parseLoaderRunTimeout(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("loader run timeout must be positive")
	}
	return timeout, nil
}
