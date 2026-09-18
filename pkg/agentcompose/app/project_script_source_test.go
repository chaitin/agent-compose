package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestNormalizeProjectRequestRejectsInvalidScriptSources(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scheduler *agentcomposev2.SchedulerSpec
	}{
		{"empty", &agentcomposev2.SchedulerSpec{ScriptSource: &agentcomposev2.SchedulerScriptSource{}}},
		{"inline and source", &agentcomposev2.SchedulerSpec{Script: "text", ScriptSource: &agentcomposev2.SchedulerScriptSource{Provider: "file", Path: "ignored.js"}}},
		{"triggers and source", &agentcomposev2.SchedulerSpec{Triggers: []*agentcomposev2.TriggerSpec{{Name: "x"}}, ScriptSource: &agentcomposev2.SchedulerScriptSource{Provider: "http", Url: "http://example.invalid/script"}}},
		{"unknown provider", &agentcomposev2.SchedulerSpec{ScriptSource: &agentcomposev2.SchedulerScriptSource{Provider: "s3"}}},
		{"file missing path", &agentcomposev2.SchedulerSpec{ScriptSource: &agentcomposev2.SchedulerScriptSource{Provider: "file"}}},
		{"http path", &agentcomposev2.SchedulerSpec{ScriptSource: &agentcomposev2.SchedulerScriptSource{Provider: "http", Url: "https://example.invalid/script", Path: "x"}}},
		{"git traversal", &agentcomposev2.SchedulerSpec{ScriptSource: &agentcomposev2.SchedulerScriptSource{Provider: "git", Url: "https://example.invalid/repo", Path: "../secret"}}},
		{"git missing path", &agentcomposev2.SchedulerSpec{ScriptSource: &agentcomposev2.SchedulerScriptSource{Provider: "git", Url: "https://example.invalid/repo"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := scriptSourceProjectSpec(nil)
			spec.Agents[0].Scheduler = tc.scheduler
			normalized, issues, err := normalizeProjectRequest(t.Context(), spec, nil, "")
			if err != nil || normalized.Spec != nil || len(issues) == 0 || !strings.Contains(issues[0].Path, "scheduler") {
				t.Fatalf("invalid source: %#v %#v %v", normalized, issues, err)
			}
		})
	}
}

func TestNormalizeProjectRequestFileSourceBaseAndHash(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "script.js"), []byte(sourceTestScript), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := scriptSourceProjectSpec(&agentcomposev2.SchedulerScriptSource{Provider: "file", Path: "script.js"})
	normalized, issues, err := normalizeProjectRequest(t.Context(), spec, &agentcomposev2.ProjectSource{ProjectDir: root}, "")
	if err != nil || len(issues) > 0 || normalized.Spec.Agents[0].Scheduler.Script != sourceTestScript {
		t.Fatalf("file: %#v %v", issues, err)
	}
	_, issues, err = normalizeProjectRequest(t.Context(), spec, &agentcomposev2.ProjectSource{ProjectDir: root}, "stale-hash")
	if err != nil || len(issues) != 1 || issues[0].Path != "submitted_spec_hash" {
		t.Fatalf("hash mismatch: %#v %v", issues, err)
	}
}

func TestIntegrationNormalizeProjectRequestScriptSourceCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := normalizeProjectRequest(ctx, scriptSourceProjectSpec(&agentcomposev2.SchedulerScriptSource{Provider: "http", Url: server.URL}), nil, "")
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		cancel()
		<-done
		t.Fatal("fetch did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || connect.CodeOf(projectConnectError(err)) != connect.CodeCanceled {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fetch did not cancel")
	}
}

func TestIntegrationNormalizeProjectRequestScriptSourceOptionalAuthentication(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "daemon-only-token")
	for _, tc := range []struct {
		name     string
		source   *agentcomposev2.SchedulerScriptSource
		wantAuth string
	}{
		{"anonymous", &agentcomposev2.SchedulerScriptSource{}, ""},
		{"token", &agentcomposev2.SchedulerScriptSource{Token: "caller-token"}, "Bearer caller-token"},
		{"basic", &agentcomposev2.SchedulerScriptSource{Username: "user", Password: "password"}, "Basic dXNlcjpwYXNzd29yZA=="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != tc.wantAuth {
					t.Error("unexpected authentication header")
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				if _, err := w.Write([]byte(sourceTestScript)); err != nil {
					t.Errorf("write source response: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			tc.source.Provider = "http"
			tc.source.Url = server.URL
			normalized, issues, err := normalizeProjectRequest(t.Context(), scriptSourceProjectSpec(tc.source), nil, "")
			if err != nil || len(issues) > 0 || normalized.Spec.Agents[0].Scheduler.Script != sourceTestScript {
				t.Fatalf("source authentication: %#v %v", issues, err)
			}
		})
	}
}
