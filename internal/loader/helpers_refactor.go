package loader

import (
	"context"
	"strings"

	eventdomain "agent-compose/internal/event"
	modeldomain "agent-compose/internal/model"
	workspacedomain "agent-compose/internal/workspace"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const (
	TopicEventSourceWebhook         = eventdomain.TopicEventSourceWebhook
	TopicEventSourceLoader          = eventdomain.TopicEventSourceLoader
	TopicEventDispatchPending       = eventdomain.TopicEventDispatchPending
	EventDeliveryStatusMatched      = eventdomain.EventDeliveryStatusMatched
	EventDeliveryStatusRunStarted   = eventdomain.EventDeliveryStatusRunStarted
	EventDeliveryStatusRunSucceeded = eventdomain.EventDeliveryStatusRunSucceeded
	EventDeliveryStatusRunFailed    = eventdomain.EventDeliveryStatusRunFailed
	EventDeliveryStatusSkipped      = eventdomain.EventDeliveryStatusSkipped
	ProjectRunStatusFailed          = "failed"
	ProjectRunStatusCanceled        = "canceled"
	ProjectRunStatusPending         = "pending"
	ProjectRunStatusRunning         = "running"
	ProjectRunSourceScheduler       = "scheduler"
	ProjectRunSourceManual          = "manual"
	ProjectRunSourceAPI             = "api"
)

var topicEventPayloadSHA256 = eventdomain.TopicEventPayloadSHA256
var validateTopicEventName = eventdomain.ValidateTopicEventName

type agentExecutionConfig struct {
	Provider          string
	AgentDefinitionID string
	Model             string
	EnvItems          []SessionEnvVar
}

func agentExecutionConfigFromDefinition(agent AgentDefinition, fallbackProvider string) agentExecutionConfig {
	provider := normalizeAgentKind(agent.Provider)
	if provider == "" {
		provider = normalizeAgentKind(fallbackProvider)
	}
	model := strings.TrimSpace(agent.Model)
	if provider == "opencode" {
		model = strings.TrimSpace(sessionEnvMap(agent.EnvItems)["OPENCODE_MODEL"])
	}
	return agentExecutionConfig{
		Provider:          provider,
		AgentDefinitionID: strings.TrimSpace(agent.ID),
		Model:             model,
		EnvItems:          append([]SessionEnvVar(nil), agent.EnvItems...),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeAgentKind(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "":
		return ""
	case "codex":
		return "codex"
	case "claude":
		return "claude"
	case "gemini":
		return "gemini"
	case "opencode", "open-code", "open_code":
		return "opencode"
	default:
		return strings.ToLower(strings.TrimSpace(agent))
	}
}

func normalizeEnvItems(items []SessionEnvVar) []SessionEnvVar {
	result := make([]SessionEnvVar, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		key := strings.ToUpper(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		item.Name = name
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func sessionEnvMap(groups ...[]SessionEnvVar) map[string]string {
	result := map[string]string{}
	for _, group := range groups {
		for _, item := range group {
			name := strings.TrimSpace(item.Name)
			if name == "" {
				continue
			}
			result[name] = item.Value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func loaderCommandEventPayload(request LoaderCommandRequest, result LoaderCommandResult) map[string]any {
	payload := map[string]any{
		"mode":            strings.TrimSpace(request.Mode),
		"command":         strings.TrimSpace(request.Command),
		"args":            append([]string(nil), request.Args...),
		"cwd":             strings.TrimSpace(request.Cwd),
		"exitCode":        result.ExitCode,
		"success":         result.Success,
		"stdoutTruncated": result.StdoutTruncated,
		"stderrTruncated": result.StderrTruncated,
		"sessionId":       result.SessionID,
		"cellId":          result.CellID,
	}
	if payload["mode"] == "shell" {
		payload["command"] = ""
	}
	return payload
}

func sessionTopicPayload(session *Session, source string) map[string]any {
	if session == nil {
		return nil
	}
	return map[string]any{
		"sessionId":     session.Summary.ID,
		"title":         session.Summary.Title,
		"driver":        session.Summary.Driver,
		"vmStatus":      session.Summary.VMStatus,
		"guestImage":    session.Summary.GuestImage,
		"triggerSource": session.Summary.TriggerSource,
		"source":        source,
	}
}

func mergeEnvItems(globalItems, sessionItems []SessionEnvVar) []SessionEnvVar {
	return modeldomain.MergeEnvItems(globalItems, sessionItems)
}

func filterPersistedRuntimeEnv(items []SessionEnvVar) []SessionEnvVar {
	result := make([]SessionEnvVar, 0, len(items))
	for _, item := range items {
		name := strings.ToUpper(strings.TrimSpace(item.Name))
		switch name {
		case "LLM_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN":
			continue
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func buildCapabilityGatewaySessionVars(string, []string) ([]SessionEnvVar, []SessionTag) {
	return nil, nil
}

func capabilityGatewayProxyTarget(CapabilityProvider) string {
	return ""
}

func agentDefinitionTags(agent AgentDefinition) []*agentcomposev1.SessionTag {
	return []*agentcomposev1.SessionTag{
		{Name: "source", Value: "agent"},
		{Name: "agent_id", Value: agent.ID},
		{Name: "agent_name", Value: agent.Name},
	}
}

func prepareSessionWorkspace(ctx context.Context, config *appconfig.Config, configDB ConfigStore, session *Session) error {
	return workspacedomain.PrepareSessionWorkspace(ctx, config, configDB, session)
}

func writeCapabilityGuide(context.Context, CapabilityProvider, *Store, *SessionStreamBroker, *Session, []string) {
}

func sessionCapabilityCapsets(session *Session) []string {
	if session == nil {
		return nil
	}
	var result []string
	for _, tag := range session.Summary.Tags {
		if strings.TrimSpace(tag.Name) == "capset_id" && strings.TrimSpace(tag.Value) != "" {
			result = append(result, strings.TrimSpace(tag.Value))
		}
	}
	return result
}

func restoreSessionTransientFields(dst, src *Session) {
	if dst == nil || src == nil {
		return
	}
	if len(src.RuntimeEnvItems) > 0 {
		dst.RuntimeEnvItems = append([]SessionEnvVar(nil), src.RuntimeEnvItems...)
	}
	if len(src.ProviderEnvItems) > 0 {
		dst.ProviderEnvItems = append([]SessionEnvVar(nil), src.ProviderEnvItems...)
	}
}

func toSessionWorkspaceSnapshot(item WorkspaceConfig) *SessionWorkspace {
	return &SessionWorkspace{
		ID:         strings.TrimSpace(item.ID),
		Name:       item.Name,
		Type:       item.Type,
		ConfigJSON: item.ConfigJSON,
	}
}

func toDriverProxyState(state ProxyState) driverpkg.ProxyState {
	return driverpkg.ProxyState{
		ProxyPath:  state.ProxyPath,
		GuestHost:  state.GuestHost,
		HostPort:   state.HostPort,
		GuestPort:  state.GuestPort,
		JupyterURL: state.JupyterURL,
		Token:      state.Token,
	}
}
