package exec

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	agentResultPrefix         = "__AGENT_RESULT__"
	agentSystemPromptFileName = "system-prompt.txt"
	llmFacadeTokenSourceAgent = "agent"
	agentSessionTagID         = "agent_id"
)

const AgentResultPrefix = agentResultPrefix

type agentExecResponse struct {
	Provider   string `json:"provider"`
	SessionID  string `json:"sessionId"`
	StopReason string `json:"stopReason"`
	FinalText  string `json:"finalText"`
	JSON       any    `json:"json"`
	Transcript string `json:"transcript"`
	Stderr     string `json:"stderr"`
}

func applyAgentProviderEnv(session *Session, agentEnv []SessionEnvVar) {
	if session == nil || len(agentEnv) == 0 {
		return
	}
	providerEnv := session.ProviderEnvItems
	if len(providerEnv) == 0 {
		providerEnv = session.EnvItems
	}
	session.ProviderEnvItems = mergeEnvItems(agentEnv, providerEnv)
}

func writeAgentPromptFile(config *appconfig.Config, session *Session, agent, message string) (string, error) {
	hostSessionDir := filepath.Dir(session.Summary.WorkspacePath)
	promptDir := filepath.Join(hostSessionDir, "state", "agents", "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		return "", fmt.Errorf("create agent prompt dir: %w", err)
	}
	name := fmt.Sprintf("%s-%d.txt", normalizeAgentKind(agent), time.Now().UTC().UnixNano())
	hostPath := filepath.Join(promptDir, name)
	if err := os.WriteFile(hostPath, []byte(message), 0o644); err != nil {
		return "", fmt.Errorf("write agent prompt file: %w", err)
	}
	return filepath.Join(config.GuestStateRoot, "agents", "prompts", name), nil
}

func writeAgentOutputSchemaFile(config *appconfig.Config, session *Session, agent, schemaJSON string) (string, error) {
	schemaJSON = strings.TrimSpace(schemaJSON)
	if schemaJSON == "" {
		return "", nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(schemaJSON), &decoded); err != nil {
		return "", fmt.Errorf("decode agent output schema json: %w", err)
	}
	if _, ok := decoded.(map[string]any); !ok {
		return "", fmt.Errorf("agent output schema must be a JSON object")
	}
	schemaDir := filepath.Join(filepath.Dir(session.Summary.WorkspacePath), "state", "agents", "schemas")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		return "", fmt.Errorf("create agent schema dir: %w", err)
	}
	name := fmt.Sprintf("%s-%d.json", normalizeAgentKind(agent), time.Now().UTC().UnixNano())
	hostPath := filepath.Join(schemaDir, name)
	if err := os.WriteFile(hostPath, []byte(schemaJSON), 0o644); err != nil {
		return "", fmt.Errorf("write agent schema file: %w", err)
	}
	return filepath.Join(config.GuestStateRoot, "agents", "schemas", name), nil
}

func hostAgentSystemPromptPath(session *Session) string {
	if session == nil || strings.TrimSpace(session.Summary.WorkspacePath) == "" {
		return ""
	}
	return filepath.Join(hostSessionDir(session), "state", "agents", "system-prompts", agentSystemPromptFileName)
}

func writeAgentSystemPromptFile(session *Session, systemPrompt string) error {
	systemPrompt = strings.TrimSpace(systemPrompt)
	hostPath := hostAgentSystemPromptPath(session)
	if hostPath == "" {
		if systemPrompt == "" {
			return nil
		}
		return fmt.Errorf("session workspace path is required to write agent system prompt")
	}
	if systemPrompt == "" {
		if err := os.Remove(hostPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove agent system prompt file: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		return fmt.Errorf("create agent system prompt dir: %w", err)
	}
	if err := os.WriteFile(hostPath, []byte(systemPrompt), 0o644); err != nil {
		return fmt.Errorf("write agent system prompt file: %w", err)
	}
	return nil
}

func (e *Executor) resolveAgentSystemPrompt(ctx context.Context, session *Session, agentDefinitionID string) (string, error) {
	if e == nil || e.configDB == nil {
		return "", nil
	}
	agentID := strings.TrimSpace(agentDefinitionID)
	if agentID == "" && session != nil {
		agentID = sessionTagValue(session.Summary.Tags, agentSessionTagID)
	}
	if agentID == "" {
		return "", nil
	}
	agent, err := e.configDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(agent.SystemPrompt), nil
}

func sessionTagValue(tags []SessionTag, name string) string {
	name = strings.TrimSpace(name)
	for _, tag := range tags {
		if strings.TrimSpace(tag.Name) == name {
			return strings.TrimSpace(tag.Value)
		}
	}
	return ""
}

func (e *Executor) executeAgentRun(ctx context.Context, session *Session, agent, agentDefinitionID, model, runID, message, outputSchemaJSON string, stream ExecStreamWriter) (ExecResult, AgentRunResult, error) {
	if session.Summary.VMStatus != VMStatusRunning {
		return ExecResult{}, AgentRunResult{}, fmt.Errorf("session is not running")
	}
	appconfig.ApplyDefaultGuestPaths(e.config)
	vmState, err := e.store.GetVMState(session.Summary.ID)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	promptPath, err := writeAgentPromptFile(e.config, session, agent, message)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	schemaPath, err := writeAgentOutputSchemaFile(e.config, session, agent, outputSchemaJSON)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	systemPrompt, err := e.resolveAgentSystemPrompt(ctx, session, agentDefinitionID)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	if err := writeAgentSystemPromptFile(session, systemPrompt); err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	runtime, err := e.runtimes.ForSession(session)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	spec := buildAgentExecSpec(e.config, session, agent, model, promptPath, schemaPath)
	managedEnv, err := ensureSessionLLMFacadeConfig(ctx, e.config, e.configDB, session, agent, model, llmFacadeTokenSourceAgent, runID)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	if len(managedEnv) > 0 {
		spec.Env = mergeManagedExecEnv(spec.Env, managedEnv)
		if e.configDB != nil {
			if token := managedEnv["AGENT_COMPOSE_SESSION_TOKEN"]; token != "" {
				defer func() { _ = e.configDB.DeleteLLMFacadeToken(context.WithoutCancel(ctx), token) }()
			}
		}
	}
	result, err := runtime.ExecStream(ctx, session, vmState, spec, stream)
	if err != nil {
		return sanitizeAgentExecResult(result), AgentRunResult{}, err
	}
	parsed, err := parseAgentExecResult(agent, result)
	if err != nil {
		return sanitizeAgentExecResult(result), AgentRunResult{}, err
	}
	return sanitizeAgentExecResult(result), parsed, nil
}

func buildAgentExecSpec(config *appconfig.Config, session *Session, agent, model, promptPath, schemaPath string) ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	agentHome := guestSessionHome(config)
	env := buildSessionExecEnv(config, session, agentHome)
	promptCommand := "agent-compose-runtime prompt" +
		" --provider " + shellQuote(agent) +
		" --message-file " + shellQuote(promptPath) +
		" --state-root " + shellQuote(config.GuestStateRoot) +
		" --workspace " + shellQuote(config.GuestWorkspacePath) +
		" --home " + shellQuote(agentHome)
	if strings.TrimSpace(model) != "" {
		promptCommand += " --model " + shellQuote(strings.TrimSpace(model))
	}
	if strings.TrimSpace(schemaPath) != "" {
		promptCommand += " --output-schema-file " + shellQuote(schemaPath)
	}
	command := strings.Join([]string{
		"set -e",
		"cd " + shellQuote(config.GuestWorkspacePath),
		"mkdir -p " + shellQuote(agentHome),
		promptCommand,
	}, " && ")
	return ExecSpec{Command: "sh", Args: []string{"-lc", command}, Env: env, Cwd: config.GuestWorkspacePath}
}

func findAgentExecPayload(raw string) (agentExecResponse, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, agentResultPrefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, agentResultPrefix))
		}
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var payload agentExecResponse
		if json.Unmarshal([]byte(line), &payload) == nil {
			return payload, true
		}
	}
	return agentExecResponse{}, false
}

func parseAgentExecResult(agent string, result ExecResult) (AgentRunResult, error) {
	raw := firstNonEmpty(result.Stdout, result.Output)
	if strings.TrimSpace(raw) == "" {
		if detail := summarizeAgentExecFailure(result); detail != "" {
			return AgentRunResult{}, fmt.Errorf("agent %s returned empty stdout: %s", agent, detail)
		}
		return AgentRunResult{}, fmt.Errorf("agent %s returned empty stdout", agent)
	}
	payload, ok := findAgentExecPayload(raw)
	if !ok && strings.TrimSpace(result.Output) != strings.TrimSpace(raw) {
		payload, ok = findAgentExecPayload(result.Output)
	}
	if !ok {
		if detail := summarizeAgentExecFailure(result); detail != "" {
			return AgentRunResult{}, fmt.Errorf("decode agent result for %s: no result payload found: %s", agent, detail)
		}
		return AgentRunResult{}, fmt.Errorf("decode agent result for %s: no result payload found", agent)
	}
	humanOutput := strings.TrimSpace(result.Stderr)
	if transcript := strings.TrimSpace(payload.Transcript); transcript != "" {
		humanOutput = transcript
	} else if strings.TrimSpace(humanOutput) == "" {
		humanOutput = strings.TrimSpace(payload.FinalText)
	}
	return AgentRunResult{
		Agent:         firstNonEmpty(strings.TrimSpace(payload.Provider), normalizeAgentKind(agent)),
		DisplayOutput: humanOutput,
		FinalText:     strings.TrimSpace(payload.FinalText),
		JSONText:      strings.TrimSpace(payload.FinalText),
		Transcript:    strings.TrimSpace(payload.Transcript),
		SessionID:     strings.TrimSpace(payload.SessionID),
		StopReason:    strings.TrimSpace(payload.StopReason),
		ExitCode:      result.ExitCode,
		Success:       result.Success,
	}, nil
}

func agentTraceEvents(transcript string, createdAt time.Time) []SessionEvent {
	lines := strings.Split(transcript, "\n")
	events := make([]SessionEvent, 0)
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		eventType, name, ok := parseAgentTraceMarker(line)
		if !ok {
			continue
		}
		events = append(events, SessionEvent{ID: uuid.NewString(), Type: eventType, Level: "info", Message: name, CreatedAt: createdAt})
	}
	return events
}

func parseAgentTraceMarker(line string) (string, string, bool) {
	if strings.HasPrefix(line, "[tool:") && strings.HasSuffix(line, "]") {
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "[tool:"), "]"))
		if name != "" {
			return "agent.tool", name, true
		}
	}
	if strings.HasPrefix(line, "[hook:") && strings.HasSuffix(line, "]") {
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "[hook:"), "]"))
		if name != "" {
			return "agent.hook", name, true
		}
	}
	return "", "", false
}

func summarizeAgentExecFailure(result ExecResult) string {
	detail := strings.TrimSpace(firstNonEmpty(result.Stderr, result.Output, result.Stdout))
	if detail == "" {
		return ""
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 240 {
		detail = detail[:240] + "..."
	}
	return detail
}

func stripAgentResultPayload(raw string) string {
	idx := strings.LastIndex(raw, agentResultPrefix)
	if idx < 0 {
		return raw
	}
	return raw[:idx]
}

func sanitizeAgentExecResult(result ExecResult) ExecResult {
	cleaned := result
	cleaned.Stdout = stripAgentResultPayload(result.Stdout)
	cleaned.Output = stripAgentResultPayload(result.Output)
	return cleaned
}

func summarizeAgentResult(result AgentRunResult) string {
	body := firstNonEmpty(result.FinalText, result.DisplayOutput, result.Transcript)
	if strings.TrimSpace(body) == "" {
		if result.Success {
			return fmt.Sprintf("%s finished without output", result.Agent)
		}
		return fmt.Sprintf("%s failed without output", result.Agent)
	}
	return body
}
