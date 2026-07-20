package workspaces

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"agent-compose/pkg/sources"
)

// storedGitWorkspaceConfig is the read model for persisted Git workspace
// configuration. The canonical fields mirror GitWorkspaceConfig; the legacy
// fields are kept only so databases written before the unified resource-source
// schema can still provision their existing workspaces.
//
// TODO(compat-issue-332-workspace-config): Remove Branch, Commit, Credential,
// LegacyPath, and their fallback mapping only after both conditions hold:
//   - the oldest supported direct upgrade starts after the release containing
//     issue #332; and
//   - all pre-#332 workspace_config.config_json values and sandbox workspace
//     snapshots have either been migrated to ref/target or expired.
//
// Keep DecodeGitWorkspaceConfig after removing these aliases: it remains the
// named persistence boundary for the canonical representation.
type storedGitWorkspaceConfig struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Ref      string `json:"ref"`
	Format   string `json:"format"`
	Target   string `json:"target"`
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`

	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	Credential string `json:"credential"`
	LegacyPath string `json:"path"`
}

// DecodeGitWorkspaceConfig decodes the persisted Git workspace representation.
// New writes contain provider/ref/target only; the legacy aliases are accepted
// here, after persistence, and are deliberately not added back to the compose
// authoring schema.
func DecodeGitWorkspaceConfig(raw string) (GitWorkspaceConfig, error) {
	var stored storedGitWorkspaceConfig
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return GitWorkspaceConfig{}, err
	}

	ref := strings.TrimSpace(stored.Ref)
	if ref == "" {
		// The legacy implementation cloned Branch first and then checked out
		// Commit, so Commit must win when both historical fields are present.
		ref = strings.TrimSpace(stored.Commit)
		if ref == "" {
			ref = strings.TrimSpace(stored.Branch)
		}
	}
	target := strings.TrimSpace(stored.Target)
	if target == "" {
		// Before issue #332, path meant the clone destination for Git
		// workspaces. It must not populate Source.Path, whose meaning is a path
		// inside fetched content and is unsupported for Git workspaces.
		target = strings.TrimSpace(stored.LegacyPath)
	}

	username := strings.TrimSpace(stored.Username)
	password := strings.TrimSpace(stored.Password)
	if credential := strings.TrimSpace(stored.Credential); credential != "" {
		// credential was the legacy field with the highest authentication
		// precedence. Decode its URL-userinfo form into the new explicit fields
		// without retaining credentials in the repository URL.
		var err error
		username, password, err = decodeLegacyGitCredential(credential)
		if err != nil {
			return GitWorkspaceConfig{}, fmt.Errorf("decode legacy git workspace credential: %w", err)
		}
	}

	provider := strings.TrimSpace(stored.Provider)
	if provider == "" {
		provider = sources.ProviderGit
	}
	return GitWorkspaceConfig{
		Source: sources.Source{
			Provider: provider,
			URL:      stored.URL,
			Ref:      ref,
			Format:   stored.Format,
			Username: username,
			Password: password,
			Token:    stored.Token,
		}.Normalized(),
		Target: target,
	}, nil
}

func decodeLegacyGitCredential(credential string) (string, string, error) {
	username, password, hasPassword := strings.Cut(credential, ":")
	decodedUsername, err := url.PathUnescape(username)
	if err != nil {
		return "", "", fmt.Errorf("decode username: %w", err)
	}
	if !hasPassword {
		return decodedUsername, "", nil
	}
	decodedPassword, err := url.PathUnescape(password)
	if err != nil {
		return "", "", fmt.Errorf("decode password: %w", err)
	}
	return decodedUsername, decodedPassword, nil
}
