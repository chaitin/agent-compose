package compose

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/sources"
)

func newTestScriptSourceResolver() *defaultScriptSourceResolver {
	resolver := NewDefaultScriptSourceResolver(nil).(*defaultScriptSourceResolver)
	resolver.validateNetworkTarget = func(context.Context, *url.URL) error { return nil }
	transport := resolver.client.Transport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{}).DialContext
	resolver.client.Transport = transport
	return resolver
}

func TestDefaultScriptSourceResolverRejectsPrivateHTTPTargets(t *testing.T) {
	resolver := NewDefaultScriptSourceResolver(nil)
	for _, location := range []string{
		"http://127.0.0.1/script.js",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/script.js",
		"http://100.64.0.1/script.js",
		"http://[::1]/script.js",
		"http://0.0.0.1/script.js",
		"http://198.18.0.1/script.js",
		"http://240.0.0.1/script.js",
		"http://[fec0::1]/script.js",
	} {
		if _, err := resolver.Resolve(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: location}); err == nil || !strings.Contains(err.Error(), "prohibited address") {
			t.Fatalf("Resolve(%q) error = %v, want prohibited address error", location, err)
		}
	}
}

func TestIsPublicScriptAddressRejectsSpecialUseNetworks(t *testing.T) {
	for _, test := range []struct {
		address string
		public  bool
	}{
		{address: "8.8.8.8", public: true},
		{address: "100.64.0.1"},
		{address: "198.18.0.1"},
		{address: "240.0.0.1"},
		{address: "0.0.0.1"},
		{address: "fec0::1"},
		{address: "::1"},
	} {
		if got := isPublicScriptAddress(net.ParseIP(test.address)); got != test.public {
			t.Errorf("isPublicScriptAddress(%q) = %t, want %t", test.address, got, test.public)
		}
	}
}

func TestDefaultScriptSourceResolverDisablesEnvironmentProxy(t *testing.T) {
	proxyRequests := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("scheduler.interval('direct', 1000, main);"))
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	target.Listener = listener
	target.Start()
	defer target.Close()
	targetAddress := listener.Addr().String()
	targetPort := strings.TrimPrefix(target.URL, "http://127.0.0.1:")

	resolver := newTestScriptSourceResolver()
	transport := resolver.client.Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr == nil && host == "public.test" {
			address = targetAddress
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	resolver.client.Transport = transport
	data, err := resolver.Resolve(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: "http://public.test:" + targetPort + "/scheduler.js"})
	if err != nil {
		t.Fatalf("Resolve with configured proxy = %v", err)
	}
	if !strings.Contains(string(data), "direct") || proxyRequests != 0 {
		t.Fatalf("script data=%q proxyRequests=%d, want direct fetch and zero proxy requests", data, proxyRequests)
	}
}

func TestDefaultScriptSourceResolverDiagnosesDisabledEnvironmentProxy(t *testing.T) {
	for _, proxyName := range []string{"HTTP_PROXY", "http_proxy"} {
		t.Run(proxyName, func(t *testing.T) {
			t.Setenv("HTTP_PROXY", "")
			t.Setenv("http_proxy", "")
			t.Setenv("HTTPS_PROXY", "")
			t.Setenv("https_proxy", "")
			t.Setenv("ALL_PROXY", "")
			t.Setenv("all_proxy", "")
			t.Setenv(proxyName, "http://proxy.invalid:8080")
			server := httptest.NewServer(http.NotFoundHandler())
			location := server.URL
			server.Close()
			_, err := newTestScriptSourceResolver().Resolve(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: location})
			if err == nil || !strings.Contains(err.Error(), proxyName+" is intentionally disabled") {
				t.Fatalf("Resolve error = %v, want %s disabled proxy diagnostic", err, proxyName)
			}
		})
	}
}

func TestSafeScriptDialContextSkipsPrivateAddresses(t *testing.T) {
	var dialed []string
	_, err := safeScriptDialContextWithResolver(
		context.Background(), "tcp", "mixed.example:443",
		func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("8.8.8.8")}, nil
		},
		func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return nil, errors.New("dial intentionally stopped")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "dial intentionally stopped") {
		t.Fatalf("dial error = %v, want intentional dial error", err)
	}
	if !reflect.DeepEqual(dialed, []string{"8.8.8.8:443"}) {
		t.Fatalf("dialed addresses = %v, want only public address", dialed)
	}
}

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
	resolver := newTestScriptSourceResolver()
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
	data, err := newTestScriptSourceResolver().Resolve(context.Background(), sources.Source{
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

	data, err := newTestScriptSourceResolver().Resolve(context.Background(), sources.Source{
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
		_, err := newTestScriptSourceResolver().Resolve(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: server.URL + "/scheduler.js?token=super-secret"})
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
		_, err := newTestScriptSourceResolver().Resolve(ctx, sources.Source{Provider: sources.ProviderHTTP, URL: server.URL})
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
		_, err := newTestScriptSourceResolver().Resolve(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: server.URL + "/0"})
		if err == nil || !strings.Contains(err.Error(), "too many redirects") {
			t.Fatalf("Resolve redirects error = %v", err)
		}
	})

	t.Run("unsupported redirect", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "file:///tmp/scheduler.js", http.StatusFound)
		}))
		defer server.Close()
		_, err := newTestScriptSourceResolver().Resolve(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: server.URL})
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
	normalized, err := Normalize(spec, NormalizeOptions{
		ResolveScriptURLs:    true,
		ScriptSourceResolver: newTestScriptSourceResolver(),
	})
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
	_, err := newTestScriptSourceResolver().Resolve(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Resolve oversized content error = %v", err)
	}
}

func TestDefaultScriptSourceResolverRejectsPrivateRedirect(t *testing.T) {
	resolver := NewDefaultScriptSourceResolver(nil).(*defaultScriptSourceResolver)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/script.js", nil)
	if err := resolver.client.CheckRedirect(req, nil); err == nil || !strings.Contains(err.Error(), "prohibited address") {
		t.Fatalf("private redirect error = %v, want prohibited address error", err)
	}
}

func TestDefaultScriptSourceResolverRejectsHTTPSDowngrade(t *testing.T) {
	resolver := newTestScriptSourceResolver()
	httpsRequest := httptest.NewRequest(http.MethodGet, "https://example.test/source", nil)
	httpRequest := httptest.NewRequest(http.MethodGet, "http://example.test/target", nil)
	err := resolver.client.CheckRedirect(httpRequest, []*http.Request{httpsRequest})
	if err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("CheckRedirect error = %v", err)
	}
}
