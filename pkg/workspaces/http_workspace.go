package workspaces

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sources"
)

const (
	// Workspaces commonly contain source trees and dependencies that are much
	// larger than individual skills, so their archive limits are intentionally
	// wider than the skill resolver's limits.
	HTTPWorkspaceDownloadLimit = 1 << 30
	HTTPWorkspaceExpandedLimit = 4 << 30
)

type HTTPWorkspaceConfig struct {
	sources.Source
	Target string `json:"target,omitempty"`
}

type httpWorkspace struct{ workspace domain.WorkspaceConfig }

func (w httpWorkspace) Prepare(ctx context.Context, session *domain.Sandbox) error {
	var cfg HTTPWorkspaceConfig
	if err := json.Unmarshal([]byte(w.workspace.ConfigJSON), &cfg); err != nil {
		return fmt.Errorf("decode http workspace config %s: %w", w.workspace.ID, err)
	}
	if cfg.Provider != sources.ProviderHTTP || cfg.Format != sources.FormatZIP || strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("http workspace %s has invalid source", w.workspace.ID)
	}
	root := strings.TrimSpace(session.Summary.WorkspacePath)
	if root == "" {
		return fmt.Errorf("session %s missing workspace path", session.Summary.ID)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "agent-compose-http-workspace-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "workspace.zip")
	if err := downloadWorkspace(ctx, cfg.Source, archive); err != nil {
		return err
	}
	extracted := filepath.Join(tmp, "content")
	if err := extractWorkspaceZip(archive, extracted); err != nil {
		return err
	}
	source := extracted
	if strings.TrimSpace(cfg.Path) != "" {
		source, err = safeWorkspaceSubdir(extracted, cfg.Path)
		if err != nil {
			return err
		}
	}
	target, err := NormalizeWorkspaceTarget(w.workspace.ID, cfg.Target)
	if err != nil {
		return err
	}
	destination := root
	if target != "." {
		destination = filepath.Join(root, target)
		if err := os.MkdirAll(destination, 0o755); err != nil {
			return err
		}
	}
	return copyWorkspaceDir(source, destination)
}

func downloadWorkspace(ctx context.Context, source sources.Source, destination string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return err
	}
	sources.ApplyHTTPAuthentication(req, source, nil)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("download workspace: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download workspace: unexpected HTTP status %d", resp.StatusCode)
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, io.LimitReader(resp.Body, HTTPWorkspaceDownloadLimit+1)); err != nil {
		return err
	}
	info, err := out.Stat()
	if err != nil {
		return err
	}
	if info.Size() > HTTPWorkspaceDownloadLimit {
		return fmt.Errorf("workspace archive exceeds download limit")
	}
	return nil
}

func extractWorkspaceZip(archive, destination string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	var expanded int64
	for _, f := range r.File {
		name, err := safeWorkspaceSubdir(destination, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace archive contains symlink %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(name, 0o755); err != nil {
				return err
			}
			continue
		}
		expanded += int64(f.UncompressedSize64)
		if expanded > HTTPWorkspaceExpandedLimit {
			return fmt.Errorf("workspace archive exceeds expanded size limit")
		}
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(in, HTTPWorkspaceExpandedLimit-expanded+1))
		in.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

func safeWorkspaceSubdir(root, subdir string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(subdir, "\\", "/")))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace archive path %q escapes root", subdir)
	}
	return filepath.Join(root, clean), nil
}

func copyWorkspaceDir(source, destination string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(destination, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("workspace contains unsupported file %q", rel)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		in.Close()
		out.Close()
		return copyErr
	})
}
