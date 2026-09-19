package workspaces

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sources"
)

// Persisted legacy values use the same literal contract as new RPC values.
func TestIntegrationWorkspaceUsesLiteralCredentialReferences(t *testing.T) {
	t.Setenv("WORKSPACE_TOKEN", "process-value")
	for _, provider := range []string{"http", "git"} {
		for _, field := range []string{"username", "password", "token", "credential"} {
			if provider != "git" && field == "credential" {
				continue
			}
			t.Run(provider+"/"+field, func(t *testing.T) {
				var authenticated atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") == "" {
						w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
						http.Error(w, "auth required", http.StatusUnauthorized)
						return
					}
					authenticated.Add(1)
					value := "${WORKSPACE_TOKEN}"
					if provider == "http" && field == "token" {
						if r.Header.Get("Authorization") != "Bearer "+value {
							t.Error("token changed")
						}
					} else {
						user, password, ok := r.BasicAuth()
						wantUser, wantPassword := "", ""
						switch field {
						case "username", "credential":
							wantUser = value
						case "password":
							wantPassword = value
						default:
							wantUser = "oauth2"
							wantPassword = value
						}
						if !ok || user != wantUser || password != wantPassword {
							t.Error("basic credentials changed")
						}
					}
					http.Error(w, "fixture rejects authentication", http.StatusUnauthorized)
				}))
				t.Cleanup(server.Close)
				config := map[string]string{"provider": provider, "url": server.URL, field: "${WORKSPACE_TOKEN}"}
				if provider == "http" {
					config["format"] = "zip"
				}
				payload, err := json.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				w, err := newWorkspace(nil, domain.WorkspaceConfig{ID: "literal", Type: provider, ConfigJSON: string(payload)})
				if err != nil {
					t.Fatal(err)
				}
				sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "test", WorkspacePath: filepath.Join(t.TempDir(), "workspace")}}
				if err := w.Prepare(t.Context(), sandbox); err == nil {
					t.Fatal("fixture must fail authentication")
				}
				if authenticated.Load() == 0 {
					t.Fatal("literal credentials did not reach source")
				}
			})
		}
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
