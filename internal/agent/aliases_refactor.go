package agent

import (
	"context"
	"sort"
	"strings"

	modeldomain "agent-compose/internal/model"
)

type SessionEnvVar = modeldomain.SessionEnvVar
type Session = modeldomain.Session
type VMState = modeldomain.VMState
type ProxyState = modeldomain.ProxyState
type ExecSpec = modeldomain.ExecSpec
type ExecResult = modeldomain.ExecResult
type ExecStreamWriter = modeldomain.ExecStreamWriter
type WorkspaceConfig = modeldomain.WorkspaceConfig

type SessionVMInfo struct {
	BoxID      string
	JupyterURL string
	ProxyState *ProxyState
}

type BoxRuntime interface {
	EnsureSession(context.Context, *Session, VMState, ProxyState) (SessionVMInfo, error)
	StopSession(context.Context, *Session, VMState) (bool, error)
	Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error)
	ExecStream(context.Context, *Session, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error)
}

var normalizeCapsetIDs = modeldomain.NormalizeCapsetIDs

func normalizeAgentKind(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "", "codex":
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
	byName := map[string]SessionEnvVar{}
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		key := strings.ToUpper(name)
		item.Name = name
		byName[key] = item
	}
	keys := make([]string, 0, len(byName))
	for key := range byName {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SessionEnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, byName[key])
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
