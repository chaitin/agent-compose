package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

const AgentSystemPromptFileName = "system-prompt.txt"

// AgentPromptFileRequest describes the daemon and optional guest copies of an
// agent prompt.
type AgentPromptFileRequest struct {
	Config         *appconfig.Config
	Sandbox        *domain.Sandbox
	Agent          string
	Message        string
	WriteGuestFile GuestFileWriterFunc
}

// AgentOutputSchemaFileRequest describes the daemon and optional guest copies
// of an agent output schema.
type AgentOutputSchemaFileRequest struct {
	Config         *appconfig.Config
	Sandbox        *domain.Sandbox
	Agent          string
	SchemaJSON     string
	WriteGuestFile GuestFileWriterFunc
}

func HostAgentSystemPromptPath(session *domain.Sandbox) string {
	if session == nil || strings.TrimSpace(session.Summary.WorkspacePath) == "" {
		return ""
	}
	return filepath.Join(HostSandboxDir(session), "state", "agents", "system-prompts", AgentSystemPromptFileName)
}

func WriteAgentPromptFile(ctx context.Context, req AgentPromptFileRequest) (string, error) {
	config, session := req.Config, req.Sandbox
	agent, message, writeGuestFile := req.Agent, req.Message, req.WriteGuestFile
	hostSandboxDir := filepath.Dir(session.Summary.WorkspacePath)
	promptDir := filepath.Join(hostSandboxDir, "state", "agents", "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		return "", fmt.Errorf("create agent prompt dir: %w", err)
	}
	name := fmt.Sprintf("%s-%d.txt", domain.NormalizeAgentKind(agent), time.Now().UTC().UnixNano())
	hostPath := filepath.Join(promptDir, name)
	if err := os.WriteFile(hostPath, []byte(message), 0o644); err != nil {
		return "", fmt.Errorf("write agent prompt file: %w", err)
	}
	guestPath := filepath.Join(config.GuestStateRoot, "agents", "prompts", name)
	// No shared filesystem (k8s - see design doc §2.1): the local write above
	// is daemon-side bookkeeping only, and the guest process reading
	// guestPath needs the content pushed there separately.
	if writeGuestFile != nil {
		if err := writeGuestFile(ctx, guestPath, []byte(message)); err != nil {
			return "", fmt.Errorf("push agent prompt file to guest: %w", err)
		}
	}
	return guestPath, nil
}

// WriteAgentSystemPromptFile materializes agent identity for the guest runtime at a
// fixed convention path under the sandbox state tree.
func WriteAgentSystemPromptFile(ctx context.Context, config *appconfig.Config, session *domain.Sandbox, systemPrompt string, writeGuestFile GuestFileWriterFunc) error {
	systemPrompt = strings.TrimSpace(systemPrompt)
	hostPath := HostAgentSystemPromptPath(session)
	if hostPath == "" {
		if systemPrompt == "" {
			return nil
		}
		return fmt.Errorf("sandbox workspace path is required to write agent system prompt")
	}
	if systemPrompt == "" {
		_, statErr := os.Stat(hostPath)
		hadExisting := statErr == nil
		// docker/boxlite see the deletion for free through their shared mount.
		// A no-shared-mount guest (k8s) only finds out via this push, so a
		// sandbox reused across runs must still be told the system prompt was
		// cleared - but only if this sandbox ever had one pushed in the first
		// place, so an agent with no system prompt at all doesn't pay for an
		// exec round trip on every prepare call.
		//
		// Push to the guest before removing the host file: hostPath's
		// existence is what hadExisting is derived from on the next call, so
		// if the guest push failed and we'd already deleted hostPath, a
		// retry would see hadExisting=false and skip clearing the guest
		// forever, leaving it stuck with a stale prompt.
		if hadExisting && writeGuestFile != nil {
			appconfig.ApplyDefaultGuestPaths(config)
			guestPath := filepath.Join(config.GuestStateRoot, "agents", "system-prompts", AgentSystemPromptFileName)
			if err := writeGuestFile(ctx, guestPath, nil); err != nil {
				return fmt.Errorf("clear agent system prompt file on guest: %w", err)
			}
		}
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
	if writeGuestFile != nil {
		appconfig.ApplyDefaultGuestPaths(config)
		guestPath := filepath.Join(config.GuestStateRoot, "agents", "system-prompts", AgentSystemPromptFileName)
		if err := writeGuestFile(ctx, guestPath, []byte(systemPrompt)); err != nil {
			return fmt.Errorf("push agent system prompt file to guest: %w", err)
		}
	}
	return nil
}

func WriteAgentOutputSchemaFile(ctx context.Context, req AgentOutputSchemaFileRequest) (string, error) {
	config, session := req.Config, req.Sandbox
	agent, schemaJSON, writeGuestFile := req.Agent, req.SchemaJSON, req.WriteGuestFile
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
	hostSandboxDir := filepath.Dir(session.Summary.WorkspacePath)
	schemaDir := filepath.Join(hostSandboxDir, "state", "agents", "schemas")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		return "", fmt.Errorf("create agent schema dir: %w", err)
	}
	name := fmt.Sprintf("%s-%d.json", domain.NormalizeAgentKind(agent), time.Now().UTC().UnixNano())
	hostPath := filepath.Join(schemaDir, name)
	if err := os.WriteFile(hostPath, []byte(schemaJSON), 0o644); err != nil {
		return "", fmt.Errorf("write agent schema file: %w", err)
	}
	guestPath := filepath.Join(config.GuestStateRoot, "agents", "schemas", name)
	if writeGuestFile != nil {
		if err := writeGuestFile(ctx, guestPath, []byte(schemaJSON)); err != nil {
			return "", fmt.Errorf("push agent schema file to guest: %w", err)
		}
	}
	return guestPath, nil
}
