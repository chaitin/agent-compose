package compose

import (
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-compose/pkg/sources"
)

func TestDefaultScriptSourceResolverReadsFilesAndFileURLs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scheduler script.js")
	if err := os.WriteFile(path, []byte("scheduler.interval('x', 1000, main);"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "scheduler-link.js")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	resolver := NewDefaultScriptSourceResolver(nil)
	for _, location := range []string{path, (&url.URL{Scheme: "file", Path: link}).String()} {
		data, err := resolver.Resolve(context.Background(), sources.Source{Provider: sources.ProviderFile, Path: location})
		if err != nil || !strings.Contains(string(data), "scheduler.interval") {
			t.Fatalf("Resolve(%q) data=%q err=%v", location, data, err)
		}
	}
	if _, err := resolver.Resolve(context.Background(), sources.Source{Provider: sources.ProviderFile, Path: dir}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory Resolve error = %v", err)
	}
}

func TestDefaultScriptSourceResolverReadsGitFile(t *testing.T) {
	repository := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "agent-compose@example.test"},
		{"config", "user.name", "Agent Compose"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repository
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	if err := os.MkdirAll(filepath.Join(repository, "schedulers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "schedulers", "review.js"), []byte("scheduler.agent('review');\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "add scheduler"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repository
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	data, err := NewDefaultScriptSourceResolver(nil).Resolve(context.Background(), sources.Source{
		Provider: sources.ProviderGit,
		URL:      repository,
		Ref:      "main",
		Path:     "schedulers/review.js",
	})
	if err != nil || !strings.Contains(string(data), "scheduler.agent") {
		t.Fatalf("git script = %q, err=%v", data, err)
	}
}

func TestDefaultScriptSourceResolverRejectsEscapingGitSymlink(t *testing.T) {
	repository := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "agent-compose@example.test"},
		{"config", "user.name", "Agent Compose"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repository
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	outside := filepath.Join(t.TempDir(), "host-secret.js")
	if err := os.WriteFile(outside, []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repository, "scheduler.js")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "scheduler.js"}, {"commit", "-m", "add scheduler symlink"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repository
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	data, err := NewDefaultScriptSourceResolver(nil).Resolve(context.Background(), sources.Source{
		Provider: sources.ProviderGit,
		URL:      repository,
		Ref:      "main",
		Path:     "scheduler.js",
	})
	if err == nil || !strings.Contains(err.Error(), "must stay within the repository") {
		t.Fatalf("escaping git symlink data=%q err=%v", data, err)
	}
}

func TestDefaultScriptSourceResolverHTTPFailures(t *testing.T) {
	t.Run("status and query redaction", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()
		_, err := NewDefaultScriptSourceResolver(nil).Resolve(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: server.URL + "/scheduler.js?token=super-secret"})
		if err == nil || !strings.Contains(err.Error(), "status 502") || strings.Contains(err.Error(), "super-secret") {
			t.Fatalf("Resolve error = %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := NewDefaultScriptSourceResolver(nil).Resolve(ctx, sources.Source{Provider: sources.ProviderHTTP, URL: server.URL})
		if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
			t.Fatalf("Resolve timeout error = %v", err)
		}
	})

	t.Run("redirect limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var n int
			_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/"), "%d", &n)
			http.Redirect(w, r, fmt.Sprintf("/%d", n+1), http.StatusFound)
		}))
		defer server.Close()
		_, err := NewDefaultScriptSourceResolver(nil).Resolve(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: server.URL + "/0"})
		if err == nil || !strings.Contains(err.Error(), "too many redirects") {
			t.Fatalf("Resolve redirects error = %v", err)
		}
	})

	t.Run("unsupported redirect", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "file:///tmp/scheduler.js", http.StatusFound)
		}))
		defer server.Close()
		_, err := NewDefaultScriptSourceResolver(nil).Resolve(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: server.URL})
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("Resolve redirect error = %v", err)
		}
	})
}

func TestNormalizeResolvesUppercaseHTTPScriptURLScheme(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("scheduler.timeout('once', 1000, main);"))
	}))
	defer server.Close()

	location := "HTTP" + strings.TrimPrefix(server.URL, "http")
	spec := mustParseCompose(t, fmt.Sprintf(`
name: uppercase-http-script
agents:
  reviewer:
    scheduler:
      script:
        provider: http
        url: %s
`, location))
	normalized, err := Normalize(spec, NormalizeOptions{ResolveScriptURLs: true})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if got := normalized.Agents[0].Scheduler.Script; got != "scheduler.timeout('once', 1000, main);" {
		t.Fatalf("scheduler script = %q", got)
	}
}

func TestDefaultScriptSourceResolverLimitsDecodedHTTPContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = writer.Write([]byte(strings.Repeat("x", maxScriptSourceBytes+1)))
		_ = writer.Close()
	}))
	defer server.Close()
	_, err := NewDefaultScriptSourceResolver(nil).Resolve(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Resolve oversized content error = %v", err)
	}
}

func TestDefaultScriptSourceResolverRejectsHTTPSDowngrade(t *testing.T) {
	resolver := NewDefaultScriptSourceResolver(nil).(*defaultScriptSourceResolver)
	httpsRequest := httptest.NewRequest(http.MethodGet, "https://example.test/source", nil)
	httpRequest := httptest.NewRequest(http.MethodGet, "http://example.test/target", nil)
	err := resolver.client.CheckRedirect(httpRequest, []*http.Request{httpsRequest})
	if err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("CheckRedirect error = %v", err)
	}
}
