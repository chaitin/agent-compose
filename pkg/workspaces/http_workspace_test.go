package workspaces

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sources"
)

func TestDecodeHTTPWorkspaceConfigNormalizesSource(t *testing.T) {
	decoded, err := DecodeHTTPWorkspaceConfig(`{"provider":" HTTP ","url":" https://example.com/a.zip ","format":"ZIP","target":" src "}`)
	if err != nil {
		t.Fatalf("DecodeHTTPWorkspaceConfig returned error: %v", err)
	}
	if decoded.Provider != sources.ProviderHTTP || decoded.Format != sources.FormatZIP {
		t.Fatalf("decoded source = %#v", decoded.Source)
	}
	if decoded.URL != "https://example.com/a.zip" || decoded.Target != "src" {
		t.Fatalf("decoded config = %#v", decoded)
	}
	if _, err := DecodeHTTPWorkspaceConfig("not-json"); err == nil {
		t.Fatal("expected malformed config to be rejected")
	}
}

func TestNewHTTPWorkspaceConfigValidatesSource(t *testing.T) {
	valid := sources.Source{Provider: sources.ProviderHTTP, URL: "https://example.com/a.zip", Format: sources.FormatZIP}
	configured, err := NewHTTPWorkspaceConfig("run-http", " run-http ", " comment ", valid, " src ")
	if err != nil {
		t.Fatalf("NewHTTPWorkspaceConfig returned error: %v", err)
	}
	if configured.Type != "http" || configured.Name != "run-http" || configured.Comment != "comment" {
		t.Fatalf("workspace config = %#v", configured)
	}
	for _, want := range []string{`"url":"https://example.com/a.zip"`, `"format":"zip"`, `"target":"src"`} {
		if !strings.Contains(configured.ConfigJSON, want) {
			t.Fatalf("config JSON %s is missing %s", configured.ConfigJSON, want)
		}
	}

	for _, test := range []struct {
		name   string
		source sources.Source
		target string
	}{
		{name: "wrong provider", source: sources.Source{Provider: sources.ProviderGit, URL: "https://example.com/a.zip", Format: sources.FormatZIP}},
		{name: "missing url", source: sources.Source{Provider: sources.ProviderHTTP, Format: sources.FormatZIP}},
		{name: "unsupported ref", source: sources.Source{Provider: sources.ProviderHTTP, URL: "https://example.com/a.zip", Ref: "main", Format: sources.FormatZIP}},
		{name: "missing format", source: sources.Source{Provider: sources.ProviderHTTP, URL: "https://example.com/a.zip"}},
		{name: "escaping target", source: valid, target: "../escape"},
		{name: "absolute target", source: valid, target: "/tmp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewHTTPWorkspaceConfig("run-http", "run-http", "", test.source, test.target); err == nil {
				t.Fatal("expected an invalid http workspace config to be rejected")
			}
		})
	}
	if _, err := NewHTTPWorkspaceConfig("", "run-http", "", valid, "."); err == nil {
		t.Fatal("expected a missing workspace id to be rejected")
	}
}

func TestHTTPWorkspaceSelectSourceRejectsMissingAndNonDirectoryPaths(t *testing.T) {
	content := t.TempDir()
	if err := os.MkdirAll(filepath.Join(content, "service", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "service", "app.txt"), []byte("app"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := httpWorkspace{}
	if selected, err := workspace.selectSource(content, "  "); err != nil || selected != content {
		t.Fatalf("selectSource(blank) = %q, %v", selected, err)
	}
	if selected, err := workspace.selectSource(content, "service"); err != nil || selected != filepath.Join(content, "service") {
		t.Fatalf("selectSource(service) = %q, %v", selected, err)
	}
	for _, test := range []struct{ name, subpath, want string }{
		{name: "file instead of directory", subpath: "service/app.txt", want: "is not a directory"},
		{name: "missing path", subpath: "absent", want: "not present"},
		{name: "escaping path", subpath: "../escape", want: "is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := workspace.selectSource(content, test.subpath)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("selectSource(%q) error = %v, want %q", test.subpath, err, test.want)
			}
		})
	}
}

type httpWorkspaceFixture struct {
	archiveURL string
	workspace  domain.WorkspaceConfig
	sandboxDir string
	root       string
}

// newHTTPWorkspaceFixture serves a zip containing a nested "service" directory
// and a top-level file, then wires the workspace config to it.
func newHTTPWorkspaceFixture(t *testing.T, path, target string) httpWorkspaceFixture {
	t.Helper()
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	if err := writeZipEntry(writer, "LICENSE", []byte("license"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeZipEntry(writer, "service/app.txt", []byte("application"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeZipEntry(writer, "service/scripts/run.sh", []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(payload.Bytes())
	}))
	t.Cleanup(server.Close)

	workspace, err := NewHTTPWorkspaceConfig("run-http", "run-http", "fixture", sources.Source{
		Provider: sources.ProviderHTTP,
		URL:      server.URL + "/archive.zip",
		Format:   sources.FormatZIP,
		Path:     path,
	}, target)
	if err != nil {
		t.Fatalf("NewHTTPWorkspaceConfig returned error: %v", err)
	}
	sandboxDir := t.TempDir()
	return httpWorkspaceFixture{
		archiveURL: workspace.ConfigJSON,
		workspace:  workspace,
		sandboxDir: sandboxDir,
		root:       filepath.Join(sandboxDir, "workspace"),
	}
}

func writeZipEntry(writer *zip.Writer, name string, content []byte, mode os.FileMode) error {
	header := &zip.FileHeader{Name: name}
	header.SetMode(mode)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = entry.Write(content)
	return err
}

func (f httpWorkspaceFixture) sandbox() *domain.Sandbox {
	return &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-http", WorkspacePath: f.root}}
}

func (f httpWorkspaceFixture) prepare(t *testing.T, limits HTTPWorkspaceLimits) error {
	t.Helper()
	return httpWorkspace{workspace: f.workspace, limits: limits}.Prepare(context.Background(), f.sandbox())
}

func TestHTTPWorkspaceIntegrationSelectsArchiveSubdirectory(t *testing.T) {
	fixture := newHTTPWorkspaceFixture(t, "service", "src")
	if err := fixture.prepare(t, DefaultHTTPWorkspaceLimits()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	app, err := os.ReadFile(filepath.Join(fixture.root, "src", "app.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(app) != "application" {
		t.Fatalf("app.txt = %q", app)
	}
	script, err := os.Stat(filepath.Join(fixture.root, "src", "scripts", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if script.Mode().Perm()&0o100 == 0 {
		t.Fatalf("script mode = %v, want the executable bit preserved", script.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "LICENSE")); !os.IsNotExist(err) {
		t.Fatalf("selected subdirectory copied unrelated archive content: %v", err)
	}
	assertNoHTTPWorkspaceStaging(t, fixture.sandboxDir)
}

func TestHTTPWorkspaceIntegrationCopiesWholeArchive(t *testing.T) {
	fixture := newHTTPWorkspaceFixture(t, "", ".")
	if err := fixture.prepare(t, DefaultHTTPWorkspaceLimits()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	for _, path := range []string{"LICENSE", filepath.Join("service", "app.txt")} {
		if _, err := os.Stat(filepath.Join(fixture.root, path)); err != nil {
			t.Fatalf("expected %s in the workspace: %v", path, err)
		}
	}
	assertNoHTTPWorkspaceStaging(t, fixture.sandboxDir)
}

func TestHTTPWorkspaceIntegrationRejectsOverLimitArchive(t *testing.T) {
	fixture := newHTTPWorkspaceFixture(t, "", ".")
	limits := DefaultHTTPWorkspaceLimits()
	limits.ExpandedBytes = 4
	err := fixture.prepare(t, limits)
	if err == nil || !strings.Contains(err.Error(), "expanded size") {
		t.Fatalf("Prepare error = %v, want expanded size limit", err)
	}
	if entries, readErr := os.ReadDir(fixture.root); readErr == nil && len(entries) != 0 {
		t.Fatalf("failed preparation left %d workspace entries", len(entries))
	}
	assertNoHTTPWorkspaceStaging(t, fixture.sandboxDir)
}

func TestHTTPWorkspaceIntegrationRejectsMissingDirectoryPath(t *testing.T) {
	fixture := newHTTPWorkspaceFixture(t, "absent", ".")
	err := fixture.prepare(t, DefaultHTTPWorkspaceLimits())
	if err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("Prepare error = %v, want a missing archive path rejection", err)
	}
}

// TestHTTPWorkspaceIntegrationFetchesFromInternalHost documents a deliberate
// difference from skill resolution: a workspace archive normally lives on an
// internal artifact host, which resolves to a loopback or private address
// (another compose service, a published loopback port, or a host on a private
// network). Those targets must be fetched, not refused.
func TestHTTPWorkspaceIntegrationFetchesFromInternalHost(t *testing.T) {
	fixture := newHTTPWorkspaceFixture(t, "", ".")
	if !strings.Contains(fixture.workspace.ConfigJSON, "127.0.0.1") {
		t.Fatalf("fixture config %s does not use a loopback artifact host", fixture.workspace.ConfigJSON)
	}
	if err := fixture.prepare(t, DefaultHTTPWorkspaceLimits()); err != nil {
		t.Fatalf("Prepare returned error: %v, want the internal archive host to be fetched", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "LICENSE")); err != nil {
		t.Fatalf("expected the archive content in the workspace: %v", err)
	}
}

func TestHTTPWorkspacePrepareRejectsUnsupportedScheme(t *testing.T) {
	workspace, err := NewHTTPWorkspaceConfig("run-http", "run-http", "fixture", sources.Source{
		Provider: sources.ProviderHTTP,
		URL:      "file:///tmp/archive.zip",
		Format:   sources.FormatZIP,
	}, ".")
	if err != nil {
		t.Fatalf("NewHTTPWorkspaceConfig returned error: %v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-http", WorkspacePath: filepath.Join(t.TempDir(), "workspace")}}
	err = httpWorkspace{workspace: workspace, limits: DefaultHTTPWorkspaceLimits()}.Prepare(context.Background(), session)
	if err == nil || !strings.Contains(err.Error(), "unsupported download scheme") {
		t.Fatalf("Prepare error = %v, want a scheme rejection", err)
	}
}

func assertNoHTTPWorkspaceStaging(t *testing.T, sandboxDir string) {
	t.Helper()
	entries, err := os.ReadDir(sandboxDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), httpWorkspaceStagingPrefix) {
			t.Fatalf("staging directory %s was not removed", entry.Name())
		}
	}
}

func TestHTTPWorkspacePrepareRejectsInvalidConfig(t *testing.T) {
	for _, raw := range []string{"{}", `{"provider":"git","url":"https://example.com/a.zip","format":"zip"}`, `{"provider":"http","url":"https://example.com/a.zip"}`} {
		t.Run(raw, func(t *testing.T) {
			session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox", WorkspacePath: filepath.Join(t.TempDir(), "workspace")}}
			err := httpWorkspace{workspace: domain.WorkspaceConfig{ID: "run-http", Type: "http", ConfigJSON: raw}, limits: DefaultHTTPWorkspaceLimits()}.Prepare(context.Background(), session)
			if err == nil {
				t.Fatal("expected an invalid workspace config to be rejected")
			}
		})
	}
}

func TestHTTPWorkspacePrepareRejectsMissingWorkspacePath(t *testing.T) {
	encoded, err := json.Marshal(HTTPWorkspaceConfig{Source: sources.Source{Provider: sources.ProviderHTTP, URL: "https://example.com/a.zip", Format: sources.FormatZIP}})
	if err != nil {
		t.Fatal(err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox"}}
	err = httpWorkspace{workspace: domain.WorkspaceConfig{ID: "run-http", Type: "http", ConfigJSON: string(encoded)}, limits: DefaultHTTPWorkspaceLimits()}.Prepare(context.Background(), session)
	if err == nil || !strings.Contains(err.Error(), "missing workspace path") {
		t.Fatalf("Prepare error = %v, want a missing workspace path rejection", err)
	}
}
