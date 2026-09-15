package workspaces

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/archive"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sources"
)

const (
	httpWorkspaceStagingPrefix = ".agent-compose-http-workspace-"
	httpWorkspaceArchiveName   = "workspace.zip"
	httpWorkspaceContentDir    = "content"
)

// HTTPWorkspaceLimits bounds one HTTP workspace materialization. The defaults
// are wider than the skill resolver's because a workspace usually carries a
// full source tree and its dependencies.
type HTTPWorkspaceLimits struct {
	DownloadBytes              int64
	ExpandedBytes              int64
	MaxEntries                 int
	MaxCompressionRatio        int64
	CompressionRatioFloorBytes int64
	FetchTimeout               time.Duration
}

func DefaultHTTPWorkspaceLimits() HTTPWorkspaceLimits {
	return HTTPWorkspaceLimits{
		DownloadBytes:              256 << 20,
		ExpandedBytes:              1 << 30,
		MaxEntries:                 100000,
		MaxCompressionRatio:        100,
		CompressionRatioFloorBytes: 64 << 20,
		FetchTimeout:               10 * time.Minute,
	}
}

type HTTPWorkspaceConfig struct {
	sources.Source
	Target string `json:"target,omitempty"`
}

func DecodeHTTPWorkspaceConfig(raw string) (HTTPWorkspaceConfig, error) {
	var stored HTTPWorkspaceConfig
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &stored); err != nil {
		return HTTPWorkspaceConfig{}, err
	}
	// Normalized is promoted from the embedded source.
	stored.Source = stored.Normalized()
	stored.Target = strings.TrimSpace(stored.Target)
	return stored, nil
}

// NewHTTPWorkspaceConfig builds the materialization config for one HTTP
// workspace. It owns provider, url, ref, format, and target validation so the
// project-run path and the scheduler path cannot drift apart.
func NewHTTPWorkspaceConfig(workspaceID, name, comment string, source sources.Source, target string) (domain.WorkspaceConfig, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return domain.WorkspaceConfig{}, fmt.Errorf("http workspace id is required")
	}
	source = source.Normalized()
	switch {
	case source.Provider != sources.ProviderHTTP:
		return domain.WorkspaceConfig{}, fmt.Errorf("http workspace requires provider %q", sources.ProviderHTTP)
	case source.URL == "":
		return domain.WorkspaceConfig{}, fmt.Errorf("http workspace url is required")
	case source.Ref != "":
		return domain.WorkspaceConfig{}, fmt.Errorf("http workspace does not support ref")
	case source.Format != sources.FormatZIP:
		return domain.WorkspaceConfig{}, fmt.Errorf("http workspace format must be %q", sources.FormatZIP)
	}
	target = strings.TrimSpace(target)
	if _, err := NormalizeWorkspaceTarget(workspaceID, target); err != nil {
		return domain.WorkspaceConfig{}, err
	}
	payload, err := json.Marshal(HTTPWorkspaceConfig{Source: source, Target: target})
	if err != nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("encode http workspace config: %w", err)
	}
	return domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       strings.TrimSpace(name),
		Type:       "http",
		ConfigJSON: string(payload),
		Comment:    strings.TrimSpace(comment),
	}, nil
}

type httpWorkspace struct {
	workspace domain.WorkspaceConfig
	limits    HTTPWorkspaceLimits
	// client is an injection point for tests and a future configuration
	// surface; nil uses the hardened transport built by the archive fetcher.
	client *http.Client
}

func (w httpWorkspace) Prepare(ctx context.Context, session *domain.Sandbox) error {
	cfg, err := DecodeHTTPWorkspaceConfig(w.workspace.ConfigJSON)
	if err != nil {
		return fmt.Errorf("decode http workspace config %s: %w", w.workspace.ID, err)
	}
	if cfg.Provider != sources.ProviderHTTP || cfg.Format != sources.FormatZIP || cfg.URL == "" {
		return fmt.Errorf("http workspace %s has invalid source", w.workspace.ID)
	}
	root := strings.TrimSpace(session.Summary.WorkspacePath)
	if root == "" {
		return fmt.Errorf("session %s missing workspace path", session.Summary.ID)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("prepare workspace %s failed: create workspace root: %w", w.workspace.Name, err)
	}
	target, err := NormalizeWorkspaceTarget(w.workspace.ID, cfg.Target)
	if err != nil {
		return err
	}
	// Staging lives beside the workspace inside the sandbox directory: it uses
	// the same filesystem as the destination, it is never exposed to the guest,
	// and sandbox removal discards it even if this process is killed.
	staging, err := os.MkdirTemp(filepath.Dir(root), httpWorkspaceStagingPrefix)
	if err != nil {
		return fmt.Errorf("prepare workspace %s failed: create staging directory: %w", w.workspace.Name, err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	fetcher, err := archive.NewFetcher(w.client, w.fetchPolicy())
	if err != nil {
		return fmt.Errorf("prepare workspace %s failed: %w", w.workspace.Name, err)
	}
	archivePath := filepath.Join(staging, httpWorkspaceArchiveName)
	// A nil env resolves "${NAME}" credentials from the daemon process
	// environment, matching how the git workspace provider resolves them.
	if _, err := fetcher.Fetch(ctx, cfg.Source, nil, archivePath); err != nil {
		return fmt.Errorf("prepare workspace %s failed: %w", w.workspace.Name, err)
	}
	content := filepath.Join(staging, httpWorkspaceContentDir)
	if err := archive.ExtractZip(archivePath, content, w.extractPolicy()); err != nil {
		return fmt.Errorf("prepare workspace %s failed: extract archive: %w", w.workspace.Name, err)
	}
	source, err := w.selectSource(content, cfg.Path)
	if err != nil {
		return err
	}
	destination := root
	if target != "." {
		destination = filepath.Join(root, target)
		if err := os.MkdirAll(destination, 0o755); err != nil {
			return fmt.Errorf("prepare workspace %s failed: create target %s: %w", w.workspace.Name, target, err)
		}
	}
	if err := copyHTTPWorkspaceContent(ctx, source, destination); err != nil {
		return fmt.Errorf("prepare workspace %s failed: %w", w.workspace.Name, err)
	}
	return nil
}

// selectSource resolves the optional in-archive subdirectory. A path that names
// a file, or nothing at all, fails loudly: silently materializing an empty
// workspace would hide an authoring mistake behind a successful run.
func (w httpWorkspace) selectSource(content, subpath string) (string, error) {
	trimmed := strings.TrimSpace(subpath)
	if trimmed == "" {
		return content, nil
	}
	selected, err := archive.SafeJoinRelative(content, trimmed)
	if err != nil {
		return "", fmt.Errorf("prepare workspace %s failed: archive path %q is invalid", w.workspace.Name, subpath)
	}
	info, err := os.Stat(selected)
	if err != nil {
		return "", fmt.Errorf("prepare workspace %s failed: archive path %q is not present in the archive", w.workspace.Name, subpath)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("prepare workspace %s failed: archive path %q is not a directory", w.workspace.Name, subpath)
	}
	return selected, nil
}

func (w httpWorkspace) fetchPolicy() archive.FetchPolicy {
	return archive.FetchPolicy{
		MaxBytes:              w.limits.DownloadBytes,
		Timeout:               w.limits.FetchTimeout,
		RequireZipContentType: true,
		// A workspace URL is written by the operator who deploys the project,
		// and internal artifact servers are a normal source: the archive often
		// comes from another compose service, from a published loopback port,
		// or from a host on a private network. Skill resolution keeps the
		// public-address-only default, because a skill may be authored for
		// someone else to resolve.
		AllowPrivateAddresses: true,
	}
}

func (w httpWorkspace) extractPolicy() archive.ExtractPolicy {
	return archive.ExtractPolicy{
		MaxExpandedBytes:           w.limits.ExpandedBytes,
		MaxEntries:                 w.limits.MaxEntries,
		MaxCompressionRatio:        w.limits.MaxCompressionRatio,
		CompressionRatioFloorBytes: w.limits.CompressionRatioFloorBytes,
	}
}

// copyHTTPWorkspaceContent copies extracted archive content into the workspace
// destination through the same cancellation-aware, clone-capable copy the file
// workspace provider uses. Extraction has already rejected symlinks and
// escaping entries.
func copyHTTPWorkspaceContent(ctx context.Context, source, destination string) error {
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return fmt.Errorf("open extracted archive content: %w", err)
	}
	defer func() { _ = sourceRoot.Close() }()
	return CopyRootDirectoryContentsContext(ctx, sourceRoot, destination)
}
