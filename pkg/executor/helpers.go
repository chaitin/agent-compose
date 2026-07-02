package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/model"
)

const agentSystemPromptFileName = "system-prompt.txt" // keep in sync with runtime/javascript/src/system-context.ts

type agentExecResponse struct {
	Provider   string `json:"provider"`
	SessionID  string `json:"sessionId"`
	StopReason string `json:"stopReason"`
	FinalText  string `json:"finalText"`
	JSON       any    `json:"json"`
	Transcript string `json:"transcript"`
	Stderr     string `json:"stderr"`
}

const AgentResultPrefix = "__AGENT_RESULT__"
const CommandResultPrefix = "__COMMAND_RESULT__"

type runtimeCommandRequestJSON struct {
	Mode           string            `json:"mode"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Script         string            `json:"script,omitempty"`
	Cwd            string            `json:"cwd"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutMs      int64             `json:"timeoutMs,omitempty"`
	MaxOutputBytes int64             `json:"maxOutputBytes"`
	ArtifactDir    string            `json:"artifactDir"`
}

type RuntimeCommandRequestJSON = runtimeCommandRequestJSON

func NormalizeAgentKind(agent string) string {
	return model.NormalizeAgentProvider(agent)
}

func normalizeAgentKind(agent string) string {
	return NormalizeAgentKind(agent)
}

func HostAgentSystemPromptPath(session *Session) string {
	if session == nil || strings.TrimSpace(session.Summary.WorkspacePath) == "" {
		return ""
	}
	return filepath.Join(hostSessionDir(session), "state", "agents", "system-prompts", agentSystemPromptFileName)
}

func hostAgentSystemPromptPath(session *Session) string {
	return HostAgentSystemPromptPath(session)
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

func WriteAgentSystemPromptFile(session *Session, systemPrompt string) error {
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

func writeAgentSystemPromptFile(session *Session, systemPrompt string) error {
	return WriteAgentSystemPromptFile(session, systemPrompt)
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
	hostSessionDir := filepath.Dir(session.Summary.WorkspacePath)
	schemaDir := filepath.Join(hostSessionDir, "state", "agents", "schemas")
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

func findAgentExecPayload(raw string) (agentExecResponse, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, AgentResultPrefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, AgentResultPrefix))
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

func ParseAgentExecResult(agent string, result ExecResult) (AgentRunResult, error) {
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

func parseAgentExecResult(agent string, result ExecResult) (AgentRunResult, error) {
	return ParseAgentExecResult(agent, result)
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
		details, consumed := collectAgentTraceDetails(eventType, lines[index+1:])
		index += consumed
		message := name
		if strings.TrimSpace(details) != "" {
			if message == "" {
				message = strings.TrimSpace(details)
			} else {
				message += "\n" + strings.TrimSpace(details)
			}
		}
		events = append(events, SessionEvent{
			ID:        uuid.NewString(),
			Type:      eventType,
			Level:     "info",
			Message:   message,
			CreatedAt: createdAt,
		})
	}
	return events
}

func AgentTraceEvents(transcript string, createdAt time.Time) []SessionEvent {
	return agentTraceEvents(transcript, createdAt)
}

func collectAgentTraceDetails(eventType string, lines []string) (string, int) {
	details := make([]string, 0, len(lines))
	for offset, raw := range lines {
		line := strings.TrimSpace(raw)
		if _, _, marker := parseAgentTraceMarker(line); marker {
			return strings.Join(details, "\n"), offset
		}
		if eventType != "agent.assistant" && line == "" {
			return strings.Join(details, "\n"), offset + 1
		}
		details = append(details, raw)
	}
	return strings.Join(details, "\n"), len(lines)
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

func ValidateLoaderCommandRequest(request LoaderCommandRequest) error {
	switch strings.ToLower(strings.TrimSpace(request.Mode)) {
	case "exec":
		if strings.TrimSpace(request.Command) == "" {
			return fmt.Errorf("command is required")
		}
	case "shell":
		if strings.TrimSpace(request.Script) == "" {
			return fmt.Errorf("script is required")
		}
	default:
		return fmt.Errorf("loader command mode must be exec or shell")
	}
	return nil
}

func validateLoaderCommandRequest(request LoaderCommandRequest) error {
	return ValidateLoaderCommandRequest(request)
}

func loaderCommandContext(ctx context.Context, timeoutMs int64) (context.Context, context.CancelFunc) {
	if timeoutMs <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
}

func LoaderCommandContext(ctx context.Context, timeoutMs int64) (context.Context, context.CancelFunc) {
	return loaderCommandContext(ctx, timeoutMs)
}

func LoaderCommandCellSource(request LoaderCommandRequest) string {
	if strings.EqualFold(strings.TrimSpace(request.Mode), "shell") {
		return request.Script
	}
	items := append([]string{request.Command}, request.Args...)
	return strings.Join(items, " ")
}

func loaderCommandCellSource(request LoaderCommandRequest) string {
	return LoaderCommandCellSource(request)
}

func RuntimeCommandRequestPayload(config *appconfig.Config, request LoaderCommandRequest, guestCellDir string) runtimeCommandRequestJSON {
	appconfig.ApplyDefaultGuestPaths(config)
	maxOutputBytes := request.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultLoaderCommandMaxOutputBytes
	}
	cwd := strings.TrimSpace(request.Cwd)
	if cwd == "" {
		cwd = config.GuestWorkspacePath
	}
	return runtimeCommandRequestJSON{
		Mode:           strings.ToLower(strings.TrimSpace(request.Mode)),
		Command:        request.Command,
		Args:           append([]string(nil), request.Args...),
		Script:         request.Script,
		Cwd:            cwd,
		Env:            request.Env,
		TimeoutMs:      request.TimeoutMs,
		MaxOutputBytes: maxOutputBytes,
		ArtifactDir:    guestCellDir,
	}
}

func runtimeCommandRequestPayload(config *appconfig.Config, request LoaderCommandRequest, guestCellDir string) runtimeCommandRequestJSON {
	return RuntimeCommandRequestPayload(config, request, guestCellDir)
}

func BuildLoaderCommandExecSpec(config *appconfig.Config, session *Session, guestRequestPath string) ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	commandHome := guestSessionHome(config)
	env := buildSessionExecEnv(config, session, commandHome)

	command := strings.Join([]string{
		"set -e",
		"cd " + shellQuote(config.GuestWorkspacePath),
		"mkdir -p " + shellQuote(commandHome),
		"agent-compose-runtime exec" +
			" --request-file " + shellQuote(guestRequestPath) +
			" --state-root " + shellQuote(config.GuestStateRoot) +
			" --workspace " + shellQuote(config.GuestWorkspacePath) +
			" --home " + shellQuote(commandHome),
	}, " && ")

	return ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", command},
		Env:     env,
		Cwd:     config.GuestWorkspacePath,
	}
}

func buildLoaderCommandExecSpec(config *appconfig.Config, session *Session, guestRequestPath string) ExecSpec {
	return BuildLoaderCommandExecSpec(config, session, guestRequestPath)
}

func buildSessionExecEnv(config *appconfig.Config, session *Session, home string) map[string]string {
	appconfig.ApplyDefaultGuestPaths(config)
	env := runtimeEnvMap(session.EnvItems)
	if env == nil {
		env = map[string]string{}
	}
	for key, value := range managedRuntimeEnvMap(session.RuntimeEnvItems) {
		env[key] = value
	}
	env["GOPATH"] = "/usr/local/go"
	env["PATH"] = "/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	env["SESSION_ID"] = session.Summary.ID
	env["WORKSPACE"] = config.GuestWorkspacePath
	env["STATE_ROOT"] = config.GuestStateRoot
	env["RUNTIME_ROOT"] = config.GuestRuntimeRoot
	env["VERSION"] = config.Version
	return env
}

func findCommandExecPayload(raw string) (RuntimeCommandResult, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, CommandResultPrefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, CommandResultPrefix))
		}
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var payload RuntimeCommandResult
		if json.Unmarshal([]byte(line), &payload) == nil {
			return payload, true
		}
	}
	return RuntimeCommandResult{}, false
}

func parseCommandExecResult(result ExecResult) (RuntimeCommandResult, error) {
	raw := firstNonEmpty(result.Stdout, result.Output)
	if strings.TrimSpace(raw) == "" {
		return RuntimeCommandResult{}, fmt.Errorf("decode command result: empty stdout")
	}
	payload, ok := findCommandExecPayload(raw)
	if !ok && strings.TrimSpace(result.Output) != strings.TrimSpace(raw) {
		payload, ok = findCommandExecPayload(result.Output)
	}
	if !ok {
		return RuntimeCommandResult{}, fmt.Errorf("decode command result: no result payload found")
	}
	return payload, nil
}

func ParseCommandExecResult(result ExecResult) (RuntimeCommandResult, error) {
	return parseCommandExecResult(result)
}

func mirrorRuntimeCommandArtifacts(hostCellDir string, result RuntimeCommandResult) error {
	files := map[string]string{
		"stdout.txt": result.Stdout,
		"stderr.txt": result.Stderr,
		"output.txt": result.Output,
	}
	for name, content := range files {
		path := filepath.Join(hostCellDir, name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write command artifact %s: %w", name, err)
		}
	}
	return nil
}

func MirrorRuntimeCommandArtifacts(hostCellDir string, result RuntimeCommandResult) error {
	return mirrorRuntimeCommandArtifacts(hostCellDir, result)
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

func SummarizeAgentExecFailure(result ExecResult) string {
	return summarizeAgentExecFailure(result)
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

func SummarizeAgentResult(result AgentRunResult) string {
	return summarizeAgentResult(result)
}

func stripAgentResultPayload(raw string) string {
	idx := strings.LastIndex(raw, AgentResultPrefix)
	if idx < 0 {
		return raw
	}
	return raw[:idx]
}

func StripAgentResultPayload(raw string) string {
	return stripAgentResultPayload(raw)
}

func sanitizeAgentExecResult(result ExecResult) ExecResult {
	cleaned := result
	cleaned.Stdout = stripAgentResultPayload(result.Stdout)
	cleaned.Output = stripAgentResultPayload(result.Output)
	return cleaned
}

func (e *Executor) ResolveAgentSystemPrompt(ctx context.Context, session *Session, agentDefinitionID string) (string, error) {
	if e == nil || e.configDB == nil {
		return "", nil
	}
	agentID := strings.TrimSpace(agentDefinitionID)
	if agentID == "" {
		taggedAgentID := sessionTagValue(session.Summary.Tags, agentSessionTagID)
		if !sessionHasAgentTag(session, taggedAgentID) {
			return "", nil
		}
		agentID = taggedAgentID
	}
	if agentID == "" {
		return "", nil
	}
	agentDef, err := e.configDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		slog.Warn("resolve agent system prompt failed", "agent_id", agentID, "error", err)
		return "", nil
	}
	return strings.TrimSpace(agentDef.SystemPrompt), nil
}

func (e *Executor) resolveAgentSystemPrompt(ctx context.Context, session *Session, agentDefinitionID string) (string, error) {
	return e.ResolveAgentSystemPrompt(ctx, session, agentDefinitionID)
}

func BuildAgentExecSpec(config *appconfig.Config, session *Session, agent, model, promptPath, schemaPath string) ExecSpec {
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

	return ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", command},
		Env:     env,
		Cwd:     config.GuestWorkspacePath,
	}
}

func buildAgentExecSpec(config *appconfig.Config, session *Session, agent, model, promptPath, schemaPath string) ExecSpec {
	return BuildAgentExecSpec(config, session, agent, model, promptPath, schemaPath)
}

func sessionTagValue(tags []SessionTag, name string) string {
	for _, tag := range tags {
		if strings.TrimSpace(tag.Name) == name {
			return strings.TrimSpace(tag.Value)
		}
	}
	return ""
}

const (
	agentSessionTagSource    = "source"
	agentSessionTagSourceVal = "agent"
	agentSessionTagID        = "agent_id"
)

func sessionHasAgentTag(session *Session, agentID string) bool {
	if session == nil {
		return false
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return false
	}
	hasSource := false
	hasAgentID := false
	for _, tag := range session.Summary.Tags {
		name := strings.TrimSpace(tag.Name)
		value := strings.TrimSpace(tag.Value)
		if name == agentSessionTagSource && value == agentSessionTagSourceVal {
			hasSource = true
		}
		if name == agentSessionTagID && value == agentID {
			hasAgentID = true
		}
	}
	return hasSource && hasAgentID
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

func normalizeEnvItems(items []SessionEnvVar) []SessionEnvVar {
	return model.NormalizeSessionEnvItems(items)
}

func mergeEnvItems(globalItems, sessionItems []SessionEnvVar) []SessionEnvVar {
	return model.MergeSessionEnvItems(globalItems, sessionItems)
}

func runtimeEnvMap(items []SessionEnvVar) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range normalizeEnvItems(items) {
		name := strings.TrimSpace(item.Name)
		if name == "" || driverpkg.LLMProviderKeyName(name) {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func managedRuntimeEnvMap(items []SessionEnvVar) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range normalizeEnvItems(items) {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func mergeManagedExecEnv(base map[string]string, managed map[string]string) map[string]string {
	if len(managed) == 0 {
		return base
	}
	if base == nil {
		base = map[string]string{}
	}
	for key, value := range managed {
		base[key] = value
	}
	return base
}

func envItemsFromMap(values map[string]string, secret bool) []SessionEnvVar {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]SessionEnvVar, 0, len(keys))
	for _, key := range keys {
		items = append(items, SessionEnvVar{Name: key, Value: values[key], Secret: secret})
	}
	return items
}

func (e *Executor) prepareSessionLLMFacadeConfig(ctx context.Context, session *Session, agent, modelName, source, runID string) (map[string]string, error) {
	if e == nil || e.prepareLLM == nil {
		return nil, nil
	}
	return e.prepareLLM(ctx, e.config, e.configDB, session, agent, modelName, source, runID)
}

func (e *Executor) ExecuteAgentRun(ctx context.Context, session *Session, agent, agentDefinitionID, model, runID, message, outputSchemaJSON string, stream ExecStreamWriter) (ExecResult, AgentRunResult, error) {
	return e.executeAgentRun(ctx, session, agent, agentDefinitionID, model, runID, message, outputSchemaJSON, stream)
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
	managedEnv, err := e.prepareSessionLLMFacadeConfig(ctx, session, agent, model, LLMFacadeTokenSourceAgent, runID)
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
