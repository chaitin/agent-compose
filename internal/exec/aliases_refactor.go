package exec

import (
	appconfig "agent-compose/pkg/config"
	llmdomain "agent-compose/pkg/llm"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	agentdomain "agent-compose/internal/agent"
	loadertypes "agent-compose/internal/loadertypes"
	modeldomain "agent-compose/internal/model"
	runtimedomain "agent-compose/internal/runtime"
	"github.com/samber/do/v2"
)

type SessionEnvVar = modeldomain.SessionEnvVar
type SessionTag = modeldomain.SessionTag
type Session = modeldomain.Session
type NotebookCell = modeldomain.NotebookCell
type ExecChunk = modeldomain.ExecChunk
type SessionEvent = modeldomain.SessionEvent
type ExecSpec = modeldomain.ExecSpec
type ExecResult = modeldomain.ExecResult
type ExecStreamWriter = modeldomain.ExecStreamWriter
type VMState = modeldomain.VMState
type ProxyState = modeldomain.ProxyState
type RuntimeCommandArtifacts = modeldomain.RuntimeCommandArtifacts
type RuntimeCommandResult = modeldomain.RuntimeCommandResult
type AgentRunResult = modeldomain.AgentRunResult
type AgentResumeInfo = modeldomain.AgentResumeInfo
type AgentDefinition = agentdomain.AgentDefinition
type LoaderCommandRequest = loadertypes.LoaderCommandRequest
type LoaderCommandResult = loadertypes.LoaderCommandResult
type LLMFacadeToken = llmdomain.LLMFacadeToken

type Store interface {
	AddCell(context.Context, *Session, NotebookCell) error
	AddEvent(context.Context, string, SessionEvent) error
	GetVMState(string) (VMState, error)
}

type ConfigStore interface {
	ListGlobalEnv(context.Context) ([]SessionEnvVar, error)
	GetAgentDefinition(context.Context, string) (AgentDefinition, error)
	SaveLLMFacadeToken(context.Context, LLMFacadeToken) error
	DeleteLLMFacadeToken(context.Context, string) error
}

type SessionVMInfo = runtimedomain.SessionVMInfo
type BoxRuntime = runtimedomain.BoxRuntime
type RuntimeProvider = runtimedomain.RuntimeProvider

type SessionStreamBroker interface {
	PublishCellStarted(string, NotebookCell)
	PublishCellOutput(string, string, string, bool)
	PublishCellCompleted(string, NotebookCell)
	PublishEventAdded(string, SessionEvent)
}

const (
	VMStatusFailed  = modeldomain.VMStatusFailed
	VMStatusRunning = modeldomain.VMStatusRunning
)

var mergeEnvItems = modeldomain.MergeEnvItems

func ensureSessionLLMFacadeConfig(ctx context.Context, config *appconfig.Config, configDB ConfigStore, session *Session, agent, model, source, runID string) (map[string]string, error) {
	if configDB == nil || session == nil || strings.TrimSpace(agent) == "" || strings.TrimSpace(config.RuntimeBaseURL) == "" {
		return nil, nil
	}
	tokenValue, token, err := newLocalLLMFacadeToken(session.Summary.ID, model, "", llmAPIProtocolForAgent(agent), source, runID)
	if err != nil {
		return nil, err
	}
	if err := configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.RuntimeBaseURL), "/") + "/agent-compose/session/" + session.Summary.ID + "/llm"
	env := map[string]string{
		"AGENT_COMPOSE_SESSION_TOKEN":    tokenValue,
		"AGENT_COMPOSE_RUNTIME_BASE_URL": strings.TrimRight(strings.TrimSpace(config.RuntimeBaseURL), "/"),
	}
	switch normalizeAgentKind(agent) {
	case "claude":
		env["ANTHROPIC_BASE_URL"] = baseURL + "/anthropic"
		env["ANTHROPIC_API_KEY"] = tokenValue
	case "opencode":
		if strings.TrimSpace(model) == "" {
			return nil, nil
		}
		env["OPENAI_API_KEY"] = tokenValue
		env["LLM_API_ENDPOINT"] = baseURL + "/responses"
	default:
		env["OPENAI_API_KEY"] = tokenValue
		env["LLM_API_ENDPOINT"] = baseURL + "/responses"
		env["LLM_API_PROTOCOL"] = "responses"
	}
	return env, nil
}

func newLocalLLMFacadeToken(sessionID, model, providerID, wireAPI, source, runID string) (string, LLMFacadeToken, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", LLMFacadeToken{}, err
	}
	tokenValue := "ac_llm_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(strings.TrimSpace(tokenValue)))
	hash := hex.EncodeToString(sum[:])
	return tokenValue, LLMFacadeToken{
		SessionID:        strings.TrimSpace(sessionID),
		TokenHash:        hash,
		TokenFingerprint: hash[:12],
		Model:            strings.TrimSpace(model),
		ProviderID:       strings.TrimSpace(providerID),
		WireAPI:          wireAPI,
		Source:           strings.TrimSpace(source),
		RunID:            strings.TrimSpace(runID),
		IssuedAt:         time.Now().UTC(),
	}, nil
}

func llmAPIProtocolForAgent(agent string) string {
	if normalizeAgentKind(agent) == "claude" {
		return "messages"
	}
	return "responses"
}

func NewExecutorDependencies(di do.Injector) (Store, ConfigStore, RuntimeProvider, SessionStreamBroker) {
	return do.MustInvoke[Store](di), do.MustInvoke[ConfigStore](di), do.MustInvoke[RuntimeProvider](di), do.MustInvoke[SessionStreamBroker](di)
}
