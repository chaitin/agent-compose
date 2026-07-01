package sessions

import (
	"context"
	"net"
	"sort"
	"strings"
	"time"

	"agent-compose/pkg/bus"
	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/model"
	"agent-compose/pkg/runtimes"
	"agent-compose/pkg/storage"
	"agent-compose/pkg/workspaces"
)

const (
	VMStatusPending = model.VMStatusPending
	VMStatusRunning = model.VMStatusRunning
	VMStatusStopped = model.VMStatusStopped
	VMStatusFailed  = model.VMStatusFailed

	SessionTypeManual = model.SessionTypeManual
	SessionTypeScript = model.SessionTypeScript

	CellTypeShell      = model.CellTypeShell
	CellTypePython     = model.CellTypePython
	CellTypeAgent      = model.CellTypeAgent
	CellTypeJavaScript = model.CellTypeJavaScript
)

type SessionTag = model.SessionTag
type SessionEnvVar = model.SessionEnvVar
type SessionSummary = model.SessionSummary
type SessionListOptions = model.SessionListOptions
type SessionWorkspace = model.SessionWorkspace
type Session = model.Session
type WorkspaceConfig = model.WorkspaceConfig
type NotebookCell = model.NotebookCell
type ExecChunk = model.ExecChunk
type SessionEvent = model.SessionEvent
type CellExecutionStream = model.CellExecutionStream
type AgentExecutionStream = model.AgentExecutionStream
type ExecuteAgentRequest = model.ExecuteAgentRequest
type VMState = model.VMState
type ProxyState = model.ProxyState

type Store = storage.Store
type ConfigStore = storage.ConfigStore
type Driver = runtimes.Driver
type RuntimeProvider = runtimes.RuntimeProvider
type AliveRuntime = runtimes.AliveRuntime
type LoaderBus = bus.LoaderBus
type LoaderTopicEvent = bus.LoaderTopicEvent
type CapabilityProvider = capabilities.Provider
type CapabilityIntegration = capabilities.Integration
type DashboardOverviewHub = dashboard.DashboardOverviewHub

func normalizeEnvItems(items []SessionEnvVar) []SessionEnvVar {
	merged := make(map[string]SessionEnvVar, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		merged[name] = SessionEnvVar{Name: name, Value: item.Value, Secret: item.Secret}
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SessionEnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

func mergeEnvItems(globalItems, sessionItems []SessionEnvVar) []SessionEnvVar {
	merged := make(map[string]SessionEnvVar, len(globalItems)+len(sessionItems))
	for _, item := range normalizeEnvItems(globalItems) {
		merged[item.Name] = item
	}
	for _, item := range normalizeEnvItems(sessionItems) {
		merged[item.Name] = item
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SessionEnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

func filterPersistedRuntimeEnv(items []SessionEnvVar) []SessionEnvVar {
	result := make([]SessionEnvVar, 0, len(items))
	for _, item := range normalizeEnvItems(items) {
		if driverpkg.LLMProviderKeyName(item.Name) {
			continue
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func toSessionWorkspaceSnapshot(item WorkspaceConfig) *SessionWorkspace {
	return &SessionWorkspace{
		ID:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJSON: item.ConfigJSON,
	}
}

func restoreSessionTransientFields(dst, src *Session) {
	model.RestoreSessionTransientFields(dst, src)
}

func prepareSessionWorkspace(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session) error {
	return workspaces.PrepareSessionWorkspace(ctx, config, configDB, session)
}

func sessionCapabilityCapsets(session *Session) []string {
	return capabilities.SessionCapabilityCapsets(session)
}

func buildCapabilityGatewaySessionVars(publicTarget string, capsetIDs []string) ([]SessionEnvVar, []SessionTag) {
	return capabilities.BuildGatewaySessionVars(publicTarget, capsetIDs)
}

func writeCapabilityGuide(ctx context.Context, provider CapabilityProvider, store *Store, streams *SessionStreamBroker, session *Session, capsetIDs []string) {
	capabilities.WriteGuide(ctx, provider, store, streams, session, capsetIDs)
}

func capabilityGatewayProxyTarget(provider CapabilityProvider) string {
	return capabilities.GatewayProxyTarget(provider)
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

func cellTopicPayload(sessionID string, cell NotebookCell, source string) map[string]any {
	return map[string]any{
		"sessionId":      sessionID,
		"cellId":         cell.ID,
		"cellType":       cell.Type,
		"success":        cell.Success,
		"exitCode":       cell.ExitCode,
		"agent":          cell.Agent,
		"agentSessionId": cell.AgentSessionID,
		"stopReason":     cell.StopReason,
		"source":         source,
	}
}

func toDriverProxyState(state ProxyState) driverpkg.ProxyState {
	return runtimes.ToDriverProxyState(state)
}

func jupyterTargetReachable(proxyState ProxyState, timeout time.Duration) bool {
	_, port := driverpkg.JupyterConnectTarget(toDriverProxyState(proxyState))
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", driverpkg.JupyterConnectAddress(toDriverProxyState(proxyState)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
