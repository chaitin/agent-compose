package workspaces

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sources"
)

func TestIntegrationWorkspaceRejectsDaemonCredentialReferences(t *testing.T) {
	t.Setenv("WORKSPACE_DAEMON_SECRET", "daemon-only-secret")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected source request", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	for _, provider := range []string{"http", "git"} {
		for _, field := range []string{"username", "password", "token", "credential"} {
			if provider != "git" && field == "credential" {
				continue
			}
			t.Run(provider+"/"+field, func(t *testing.T) {
				// Include legacy persisted credentials, which bypass compose normalization.
				config := map[string]string{"provider": provider, "url": server.URL, field: " ${WORKSPACE_DAEMON_SECRET} "}
				if provider == "http" {
					config["format"] = "zip"
				}
				payload, err := json.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				w, err := newWorkspace(nil, domain.WorkspaceConfig{ID: "credential-test", Type: provider, ConfigJSON: string(payload)})
				if err != nil {
					t.Fatal(err)
				}
				sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "test", WorkspacePath: filepath.Join(t.TempDir(), "workspace")}}
				err = w.Prepare(t.Context(), sandbox)
				if err == nil || !strings.Contains(err.Error(), "must be resolved by the caller") {
					t.Fatalf("expected unresolved credential rejection, got %v", err)
				}
				if strings.Contains(err.Error(), "daemon-only-secret") {
					t.Fatal("workspace error leaked a daemon secret")
				}
			})
		}
	}
	if requests.Load() != 0 {
		t.Fatal("unresolved credentials triggered outbound requests")
	}
}

func TestIntegrationHTTPWorkspacePreservesExplicitAuthentication(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "daemon-only-token")
	fixture := newHTTPWorkspaceFixture(t, "", ".")
	cfg, err := DecodeHTTPWorkspaceConfig(fixture.workspace.ConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		source   sources.Source
		wantAuth string
	}{
		{"anonymous", sources.Source{}, ""},
		{"token", sources.Source{Token: "caller-token"}, "Bearer caller-token"},
		{"basic", sources.Source{Username: "user", Password: "password"}, "Basic dXNlcjpwYXNzd29yZA=="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != tc.wantAuth {
					t.Error("unexpected authentication header")
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, cfg.URL, http.StatusFound)
			}))
			t.Cleanup(server.Close)
			source := tc.source
			source.Provider, source.URL, source.Format = "http", server.URL, "zip"
			config, err := NewHTTPWorkspaceConfig("auth-test", "auth-test", "", source, ".")
			if err != nil {
				t.Fatal(err)
			}
			w := httpWorkspace{workspace: config, limits: DefaultHTTPWorkspaceLimits()}
			root := filepath.Join(t.TempDir(), "workspace")
			sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "test", WorkspacePath: root}}
			if err := w.Prepare(t.Context(), sandbox); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(root, "LICENSE"))
			if err != nil || string(content) != "license" {
				t.Fatalf("workspace content = %q, error = %v", content, err)
			}
		})
	}
}
