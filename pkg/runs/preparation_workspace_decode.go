package runs

import (
	"encoding/json"
	"fmt"
	"strings"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type storedRevisionSpec struct {
	Workspaces []json.RawMessage     `json:"workspaces"`
	Agents     []storedRevisionAgent `json:"agents"`
}

type storedRevisionAgent struct {
	Workspace json.RawMessage `json:"workspace"`
}

// storedNamedRevisionWorkspace supports both representations that can exist
// in project_revision.spec_json: canonical compose snapshots store workspace
// fields beside key, while API-shaped snapshots nest them under workspace.
// Supporting both wrappers is an ongoing storage concern, independent of the
// removable pre-#332 field aliases documented on storedRevisionWorkspace.
type storedNamedRevisionWorkspace struct {
	Key       string          `json:"key"`
	Name      string          `json:"name"`
	Workspace json.RawMessage `json:"workspace"`
}

type storedRevisionWorkspace struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Ref      string `json:"ref"`
	Path     string `json:"path"`
	Format   string `json:"format"`
	Target   string `json:"target"`
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`

	// Branch and Commit are read-only aliases for revisions persisted before
	// issue #332 unified Git revisions under ref. In that historical format,
	// Git path meant the clone destination; restoreStoredRevisionWorkspace
	// therefore moves it to target instead of retaining it as Source.Path.
	//
	// TODO(compat-issue-332-project-revision): Remove Branch, Commit, and the
	// Git path-to-target fallback only after the oldest supported database can
	// no longer contain pre-#332 project_revision.spec_json rows. Because users
	// can retain project revisions indefinitely, removal requires either an
	// explicit data migration or a documented revision-retention boundary; the
	// passage of several releases alone is not sufficient. Keep the flat versus
	// nested wrapper restoration above when removing these legacy field aliases.
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

// restoreCanonicalRevisionWorkspaces restores workspace fields from the raw
// persisted snapshot after the generated API-shaped decoder has run. Reading
// the raw JSON is essential for pre-#332 rows because encoding/json discards
// their removed branch and commit fields when decoding WorkspaceSpec directly.
// This boundary affects database snapshots only; it does not make the legacy
// fields valid in current compose YAML.
func restoreCanonicalRevisionWorkspaces(data []byte, spec *agentcomposev2.ProjectSpec) error {
	if spec == nil {
		return nil
	}
	var stored storedRevisionSpec
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("decode project revision workspace compatibility shape: %w", err)
	}
	if err := restoreStoredProjectWorkspaces(stored.Workspaces, spec.Workspaces); err != nil {
		return err
	}
	if err := restoreStoredAgentWorkspaces(stored.Agents, spec.Agents); err != nil {
		return err
	}
	return nil
}

func restoreStoredProjectWorkspaces(stored []json.RawMessage, decoded []*agentcomposev2.NamedWorkspaceSpec) error {
	for i, raw := range stored {
		if i >= len(decoded) || decoded[i] == nil {
			continue
		}
		var named storedNamedRevisionWorkspace
		if err := json.Unmarshal(raw, &named); err != nil {
			return fmt.Errorf("decode project revision workspace %d: %w", i, err)
		}

		workspaceJSON := raw
		nested := len(named.Workspace) != 0 && string(named.Workspace) != "null"
		if nested {
			workspaceJSON = named.Workspace
		}
		workspace, present, err := restoreStoredRevisionWorkspace(workspaceJSON, nested)
		if err != nil {
			return fmt.Errorf("decode project revision workspace %d fields: %w", i, err)
		}
		if !present {
			continue
		}
		if key := strings.TrimSpace(named.Key); key != "" {
			decoded[i].Name = key
		} else if strings.TrimSpace(decoded[i].GetName()) == "" {
			decoded[i].Name = strings.TrimSpace(named.Name)
		}
		// In the flat canonical shape, name belongs to the named-workspace
		// wrapper rather than the WorkspaceSpec payload.
		if !nested {
			workspace.Name = ""
		}
		decoded[i].Workspace = workspace
	}
	return nil
}

func restoreStoredAgentWorkspaces(stored []storedRevisionAgent, decoded []*agentcomposev2.AgentSpec) error {
	for i, agent := range stored {
		if i >= len(decoded) || decoded[i] == nil || len(agent.Workspace) == 0 || string(agent.Workspace) == "null" {
			continue
		}
		workspace, present, err := restoreStoredRevisionWorkspace(agent.Workspace, true)
		if err != nil {
			return fmt.Errorf("decode project revision agent %d workspace: %w", i, err)
		}
		if present {
			decoded[i].Workspace = workspace
		}
	}
	return nil
}

func restoreStoredRevisionWorkspace(raw json.RawMessage, nameIsWorkspaceField bool) (*agentcomposev2.WorkspaceSpec, bool, error) {
	var stored storedRevisionWorkspace
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, false, err
	}

	provider := strings.TrimSpace(stored.Provider)
	ref := strings.TrimSpace(stored.Ref)
	path := strings.TrimSpace(stored.Path)
	target := strings.TrimSpace(stored.Target)
	if strings.EqualFold(provider, "git") {
		if ref == "" {
			// The old runtime cloned Branch and then checked out Commit, so a
			// persisted Commit is the closest equivalent to the unified ref.
			ref = strings.TrimSpace(stored.Commit)
			if ref == "" {
				ref = strings.TrimSpace(stored.Branch)
			}
		}
		if target == "" {
			target = path
		}
		// Git workspaces do not support a content subpath. Every persisted Git
		// path from the old schema represented the clone target.
		path = ""
	}

	name := ""
	if nameIsWorkspaceField {
		name = strings.TrimSpace(stored.Name)
	}
	present := name != "" || provider != "" || strings.TrimSpace(stored.URL) != "" ||
		ref != "" || path != "" || strings.TrimSpace(stored.Format) != "" || target != "" ||
		strings.TrimSpace(stored.Username) != "" || strings.TrimSpace(stored.Password) != "" || strings.TrimSpace(stored.Token) != ""
	if !present {
		return nil, false, nil
	}
	return &agentcomposev2.WorkspaceSpec{
		Name:     name,
		Provider: provider,
		Url:      strings.TrimSpace(stored.URL),
		Ref:      ref,
		Path:     path,
		Format:   strings.TrimSpace(stored.Format),
		Target:   target,
		Username: strings.TrimSpace(stored.Username),
		Password: strings.TrimSpace(stored.Password),
		Token:    strings.TrimSpace(stored.Token),
	}, true, nil
}
