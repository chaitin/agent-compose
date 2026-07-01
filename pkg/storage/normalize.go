package storage

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func normalizeAgentDefinition(item AgentDefinition, assignDefaults bool) (AgentDefinition, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	item.Provider = normalizeAgentKind(item.Provider)
	if item.Provider == "" && assignDefaults {
		item.Provider = modelDefaultAgentProvider
	}
	item.Model = strings.TrimSpace(item.Model)
	item.SystemPrompt = strings.TrimSpace(item.SystemPrompt)
	item.Driver = strings.TrimSpace(item.Driver)
	item.GuestImage = strings.TrimSpace(item.GuestImage)
	item.WorkspaceID = strings.TrimSpace(item.WorkspaceID)
	item.CapsetIDs = normalizeCapsetIDs(item.CapsetIDs)
	item.ManagedProjectID = strings.TrimSpace(item.ManagedProjectID)
	item.ManagedAgentName = strings.TrimSpace(item.ManagedAgentName)
	item.ConfigJSON = strings.TrimSpace(item.ConfigJSON)
	if item.ConfigJSON == "" {
		item.ConfigJSON = "{}"
	}
	if item.ID == "" {
		return AgentDefinition{}, fmt.Errorf("agent definition id is required")
	}
	if item.Name == "" {
		return AgentDefinition{}, fmt.Errorf("agent definition name is required")
	}
	if item.Provider == "" {
		return AgentDefinition{}, fmt.Errorf("agent definition provider is required")
	}
	if item.Provider != "codex" && item.Provider != "claude" && item.Provider != "gemini" && item.Provider != "opencode" {
		return AgentDefinition{}, fmt.Errorf("agent definition provider %q is not supported", item.Provider)
	}
	if !isJSONObject(item.ConfigJSON) {
		return AgentDefinition{}, fmt.Errorf("agent definition config_json must be a JSON object")
	}
	if item.ManagedProjectID == "" {
		item.ManagedProjectRevision = 0
		item.ManagedAgentName = ""
	} else {
		if item.ManagedAgentName == "" {
			return AgentDefinition{}, fmt.Errorf("managed agent name is required")
		}
		if item.ManagedProjectRevision < 0 {
			return AgentDefinition{}, fmt.Errorf("managed project revision cannot be negative")
		}
	}
	item.EnvItems = normalizeEnvItems(item.EnvItems)
	return item, nil
}

const modelDefaultAgentProvider = "codex"

func isJSONObject(raw string) bool {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded); err != nil {
		return false
	}
	return decoded != nil
}

func normalizeLoaderRuntime(runtime string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "", LoaderRuntimeScheduler:
		return LoaderRuntimeScheduler, nil
	default:
		return "", fmt.Errorf("unsupported loader runtime %q", runtime)
	}
}

func normalizeLoaderTriggerKind(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case LoaderTriggerKindInterval:
		return LoaderTriggerKindInterval, nil
	case LoaderTriggerKindEvent:
		return LoaderTriggerKindEvent, nil
	case LoaderTriggerKindTimeout:
		return LoaderTriggerKindTimeout, nil
	case LoaderTriggerKindCron:
		return LoaderTriggerKindCron, nil
	default:
		return "", fmt.Errorf("unsupported loader trigger kind %q", kind)
	}
}

func normalizeLoaderSessionPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case LoaderSessionPolicyNew:
		return LoaderSessionPolicyNew
	case LoaderSessionPolicyReuse:
		return LoaderSessionPolicyReuse
	default:
		return LoaderSessionPolicySticky
	}
}

func normalizeLoaderConcurrencyPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case LoaderConcurrencyPolicyParallel:
		return LoaderConcurrencyPolicyParallel
	default:
		return LoaderConcurrencyPolicySkip
	}
}

func normalizeLoaderRunStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case LoaderRunStatusRunning:
		return LoaderRunStatusRunning
	case LoaderRunStatusSucceeded:
		return LoaderRunStatusSucceeded
	case LoaderRunStatusFailed:
		return LoaderRunStatusFailed
	case LoaderRunStatusSkipped:
		return LoaderRunStatusSkipped
	default:
		return ""
	}
}

func normalizeProjectRunSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case ProjectRunSourceScheduler:
		return ProjectRunSourceScheduler
	case ProjectRunSourceAPI:
		return ProjectRunSourceAPI
	default:
		return ProjectRunSourceManual
	}
}

func defaultLoaderName(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return "Loader " + now.UTC().Format("2006-01-02 15:04")
}
