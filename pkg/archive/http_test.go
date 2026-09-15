package archive

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/sources"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestNewFetcherRequiresPositiveMaxBytes(t *testing.T) {
	if _, err := NewFetcher(nil, FetchPolicy{}); err == nil {
		t.Fatal("expected a missing max bytes limit to be rejected")
	}
	fetcher, err := NewFetcher(nil, FetchPolicy{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("NewFetcher returned error: %v", err)
	}
	if fetcher.policy.Timeout != DefaultFetchTimeout || fetcher.policy.MaxRedirects != DefaultMaxRedirects {
		t.Fatalf("policy defaults = %#v", fetcher.policy)
	}
}

func TestValidateDownloadURLRejectsUnsupportedSchemesAndPrivateHosts(t *testing.T) {
	for _, test := range []struct {
		name      string
		rawURL    string
		allow     bool
		wantError bool
	}{
		{name: "file scheme", rawURL: "file:///tmp/archive.zip", wantError: true},
		{name: "missing scheme", rawURL: "example.com/archive.zip", wantError: true},
		{name: "missing host", rawURL: "https:///archive.zip", wantError: true},
		{name: "loopback", rawURL: "http://127.0.0.1/archive.zip", wantError: true},
		{name: "metadata", rawURL: "http://169.254.169.254/latest/meta-data/", wantError: true},
		{name: "private range", rawURL: "http://10.0.0.1/archive.zip", wantError: true},
		{name: "public literal", rawURL: "https://93.184.216.34/archive.zip"},
		{name: "loopback allowed", rawURL: "http://127.0.0.1/archive.zip", allow: true},
		{name: "metadata allowed", rawURL: "http://169.254.169.254/archive.zip", allow: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDownloadURL(test.rawURL, test.allow)
			if test.wantError && err == nil {
				t.Fatalf("ValidateDownloadURL(%q) = nil, want rejection", test.rawURL)
			}
			if !test.wantError && err != nil {
				t.Fatalf("ValidateDownloadURL(%q) = %v, want acceptance", test.rawURL, err)
			}
		})
	}
}

func TestIsPrivateAddress(t *testing.T) {
	for _, test := range []struct {
		address string
		want    bool
	}{
		{address: "127.0.0.1", want: true},
		{address: "::1", want: true},
		{address: "10.1.2.3", want: true},
		{address: "172.16.0.1", want: true},
		{address: "192.168.1.1", want: true},
		{address: "169.254.169.254", want: true},
		{address: "fe80::1", want: true},
		{address: "0.0.0.0", want: true},
		{address: "93.184.216.34", want: false},
	} {
		t.Run(test.address, func(t *testing.T) {
			if got := isPrivateAddress(net.ParseIP(test.address)); got != test.want {
				t.Fatalf("isPrivateAddress(%s) = %t, want %t", test.address, got, test.want)
			}
		})
	}
}

func TestDialPublicAddressRejectsPrivateAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1:80", "169.254.169.254:80"} {
		t.Run(address, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := dialPublicAddress(ctx, net.Dialer{}, "tcp", address)
			if conn != nil {
				_ = conn.Close()
			}
			if err == nil {
				t.Fatal("expected private address dial to be rejected")
			}
			if !strings.Contains(err.Error(), "no allowed public address") {
				t.Fatalf("error = %q, want public address validation", err)
			}
		})
	}
}

func TestFetchRejectsPrivateHostBeforeConnecting(t *testing.T) {
	fetcher, err := NewFetcher(nil, FetchPolicy{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "archive.zip")
	_, err = fetcher.Fetch(context.Background(), sources.Source{Provider: sources.ProviderHTTP, URL: "http://169.254.169.254/archive.zip"}, nil, destination)
	if err == nil || !strings.Contains(err.Error(), "private address") {
		t.Fatalf("Fetch error = %v, want private address rejection", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("rejected fetch created a staging file: %v", statErr)
	}
}

func TestFetchRejectsRedirectToPrivateHost(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Status:     "302 Found",
			Header:     http.Header{"Location": []string{"http://127.0.0.1/archive.zip"}},
			Body:       http.NoBody,
			Request:    request,
		}, nil
	})}
	fetcher, err := NewFetcher(client, FetchPolicy{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background(), sources.Source{URL: "http://93.184.216.34/archive.zip"}, nil, filepath.Join(t.TempDir(), "archive.zip"))
	if err == nil || !strings.Contains(err.Error(), "private address") {
		t.Fatalf("Fetch error = %v, want redirect rejection", err)
	}
}

// TestFetchRestrictsRedirectSchemeDowngrade covers the credential-leak path:
// Go only strips sensitive headers when a redirect changes host, so a redirect
// from https to http on the same host would resend the Authorization header,
// and fetch the archive, in cleartext.
func TestFetchRestrictsRedirectSchemeDowngrade(t *testing.T) {
	for _, test := range []struct {
		name     string
		location string
		wantErr  bool
	}{
		{name: "https to http downgrade", location: "http://93.184.216.34/archive.zip", wantErr: true},
		{name: "https stays https", location: "https://93.184.216.34/archive.zip"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if requests.Add(1) == 1 {
					return &http.Response{
						StatusCode: http.StatusFound,
						Status:     "302 Found",
						Header:     http.Header{"Location": []string{test.location}},
						Body:       http.NoBody,
						Request:    request,
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     http.Header{"Content-Type": []string{"application/zip"}},
					Body:       io.NopCloser(strings.NewReader("redirected-archive")),
					Request:    request,
				}, nil
			})}
			fetcher, err := NewFetcher(client, FetchPolicy{MaxBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(t.TempDir(), "archive.zip")
			_, err = fetcher.Fetch(context.Background(), sources.Source{
				URL:   "https://93.184.216.34/start.zip",
				Token: "archive-secret",
			}, nil, destination)
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "refusing redirect from https") {
					t.Fatalf("Fetch error = %v, want a downgrade rejection", err)
				}
				if requests.Load() != 1 {
					t.Fatalf("server saw %d requests, want the downgrade to stop after the first", requests.Load())
				}
				return
			}
			if err != nil {
				t.Fatalf("Fetch returned error: %v", err)
			}
			stored, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			if string(stored) != "redirected-archive" {
				t.Fatalf("stored archive = %q", stored)
			}
		})
	}
}

func TestFetchRejectsUnexpectedStatus(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Body:       io.NopCloser(strings.NewReader("missing")),
			Request:    request,
		}, nil
	})}
	fetcher, err := NewFetcher(client, FetchPolicy{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background(), sources.Source{URL: "http://93.184.216.34/archive.zip"}, nil, filepath.Join(t.TempDir(), "archive.zip"))
	if err == nil || !strings.Contains(err.Error(), "unexpected status") {
		t.Fatalf("Fetch error = %v, want status rejection", err)
	}
}
