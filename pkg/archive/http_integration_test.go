package archive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chaitin/agent-compose/pkg/sources"
)

func TestFetcherIntegrationDownloadsZipWithAuthentication(t *testing.T) {
	t.Setenv("ARCHIVE_TOKEN", "process-value")
	payload := []byte("zip-payload")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer ${ARCHIVE_TOKEN}" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	fetcher := newLoopbackFetcher(t, FetchPolicy{MaxBytes: 1 << 20, RequireZipContentType: true})
	destination := filepath.Join(t.TempDir(), "archive.zip")
	written, err := fetcher.Fetch(context.Background(), sources.Source{
		Provider: sources.ProviderHTTP,
		URL:      server.URL + "/archive.zip",
		Token:    "${ARCHIVE_TOKEN}",
	}, destination)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if written != int64(len(payload)) {
		t.Fatalf("Fetch wrote %d bytes, want %d", written, len(payload))
	}
	stored, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(payload) {
		t.Fatalf("stored archive = %q, want %q", stored, payload)
	}
}

func TestFetcherIntegrationEnforcesDownloadLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte(strings.Repeat("a", 4096)))
	}))
	defer server.Close()

	fetcher := newLoopbackFetcher(t, FetchPolicy{MaxBytes: 1024})
	destination := filepath.Join(t.TempDir(), "archive.zip")
	if _, err := fetcher.Fetch(context.Background(), sources.Source{URL: server.URL + "/archive.zip"}, destination); err == nil || !strings.Contains(err.Error(), "download exceeds") {
		t.Fatalf("Fetch error = %v, want download limit rejection", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("over-limit download left a staging file: %v", err)
	}
}

func TestFetcherIntegrationRejectsUnexpectedContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>not an archive</html>"))
	}))
	defer server.Close()

	fetcher := newLoopbackFetcher(t, FetchPolicy{MaxBytes: 1 << 20, RequireZipContentType: true})
	_, err := fetcher.Fetch(context.Background(), sources.Source{URL: server.URL + "/download"}, filepath.Join(t.TempDir(), "archive.zip"))
	if err == nil || !strings.Contains(err.Error(), "unexpected content type") {
		t.Fatalf("Fetch error = %v, want content type rejection", err)
	}
}

func TestFetcherIntegrationFollowsRedirectsWithinPolicy(t *testing.T) {
	var requested atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requested.Add(1)
		if request.URL.Path == "/start.zip" {
			http.Redirect(w, request, "/archive.zip", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("redirected-archive"))
	}))
	defer server.Close()

	fetcher := newLoopbackFetcher(t, FetchPolicy{MaxBytes: 1 << 20})
	destination := filepath.Join(t.TempDir(), "archive.zip")
	if _, err := fetcher.Fetch(context.Background(), sources.Source{URL: server.URL + "/start.zip"}, destination); err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if requested.Load() != 2 {
		t.Fatalf("server saw %d requests, want 2", requested.Load())
	}
	stored, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "redirected-archive" {
		t.Fatalf("stored archive = %q", stored)
	}
}

// TestFetcherIntegrationRequiresOptInForPrivateHosts proves the default policy
// refuses a loopback target without contacting it, which is what keeps a
// remote archive URL from reaching the daemon's own network.
func TestFetcherIntegrationRequiresOptInForPrivateHosts(t *testing.T) {
	var requested atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requested.Add(1)
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("private-archive"))
	}))
	defer server.Close()

	fetcher, err := NewFetcher(nil, FetchPolicy{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background(), sources.Source{URL: server.URL + "/archive.zip"}, filepath.Join(t.TempDir(), "archive.zip"))
	if err == nil || !strings.Contains(err.Error(), "private address") {
		t.Fatalf("Fetch error = %v, want private address rejection", err)
	}
	if requested.Load() != 0 {
		t.Fatalf("server received %d requests, want none", requested.Load())
	}
}

// TestFetcherIntegrationAppliesAddressPolicyToResolvedHostnames documents that
// the address policy is decided by what a host resolves to, never by how the
// URL is written. A Docker Compose service name resolves to a private
// container address through the embedded DNS resolver, so it is refused under
// the hardened default exactly like its IP would be, and only an explicit
// opt-in reaches it.
func TestFetcherIntegrationAppliesAddressPolicyToResolvedHostnames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("service-archive"))
	}))
	defer server.Close()
	// The same server, addressed by name instead of by IP.
	serviceURL := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	if serviceURL == server.URL {
		t.Fatalf("fixture url %s was not rewritten to a hostname", server.URL)
	}

	hardened, err := NewFetcher(nil, FetchPolicy{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	_, err = hardened.Fetch(context.Background(), sources.Source{URL: serviceURL + "/archive.zip"}, filepath.Join(t.TempDir(), "archive.zip"))
	if err == nil || !strings.Contains(err.Error(), "private address") {
		t.Fatalf("hardened Fetch error = %v, want the resolved private address to be refused", err)
	}

	optedIn, err := NewFetcher(nil, FetchPolicy{MaxBytes: 1 << 20, AllowPrivateAddresses: true})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "archive.zip")
	written, err := optedIn.Fetch(context.Background(), sources.Source{URL: serviceURL + "/archive.zip"}, destination)
	if err != nil {
		t.Fatalf("opt-in Fetch returned error: %v, want the hostname to be fetched", err)
	}
	stored, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len("service-archive")) || string(stored) != "service-archive" {
		t.Fatalf("stored archive = %q (%d bytes)", stored, written)
	}
}

func newLoopbackFetcher(t *testing.T, policy FetchPolicy) Fetcher {
	t.Helper()
	policy.AllowPrivateAddresses = true
	fetcher, err := NewFetcher(nil, policy)
	if err != nil {
		t.Fatalf("NewFetcher returned error: %v", err)
	}
	return fetcher
}
