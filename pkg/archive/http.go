// Package archive fetches remote archives and expands them inside explicit
// limits. Every feature that accepts an author-provided archive URL (skills and
// HTTP workspaces) uses this package so that address filtering, redirect
// validation, credential application, and archive limits cannot drift apart.
package archive

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/sources"
)

const (
	DefaultFetchTimeout          = 30 * time.Second
	DefaultResponseHeaderTimeout = 30 * time.Second
	DefaultDialTimeout           = 30 * time.Second
	DefaultMaxRedirects          = 10
)

// FetchPolicy bounds one remote archive download. Zero values select the
// documented defaults; MaxBytes is required.
type FetchPolicy struct {
	// MaxBytes is the hard cap on the downloaded archive size. A larger
	// response fails instead of being silently truncated.
	MaxBytes int64
	// Timeout bounds the entire request, including reading the body.
	Timeout time.Duration
	// ResponseHeaderTimeout bounds how long the server may take to send
	// response headers, so a stalled server cannot pin the caller for Timeout.
	ResponseHeaderTimeout time.Duration
	// MaxRedirects caps the redirect chain. Every hop is revalidated.
	MaxRedirects int
	// AllowPrivateAddresses permits loopback, link-local, private-range, and
	// metadata targets. It is false by default: an archive URL is
	// author-controlled input and the daemon usually sits next to services the
	// author cannot otherwise reach.
	AllowPrivateAddresses bool
	// RequireZipContentType rejects responses that are neither named .zip nor
	// served with a zip-like content type.
	RequireZipContentType bool
}

// Fetcher downloads remote archives under a fixed policy.
type Fetcher struct {
	policy FetchPolicy
	client *http.Client
}

// NewFetcher builds a fetcher for the policy. A nil base client uses a
// transport that connects directly (no environment proxy, which would resolve
// the target itself and bypass the address filter below) and refuses
// non-public addresses unless the policy allows them.
func NewFetcher(base *http.Client, policy FetchPolicy) (Fetcher, error) {
	if policy.MaxBytes <= 0 {
		return Fetcher{}, fmt.Errorf("archive fetch policy requires a positive max bytes")
	}
	if policy.Timeout <= 0 {
		policy.Timeout = DefaultFetchTimeout
	}
	if policy.ResponseHeaderTimeout <= 0 {
		policy.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	}
	if policy.MaxRedirects <= 0 {
		policy.MaxRedirects = DefaultMaxRedirects
	}
	return Fetcher{policy: policy, client: newFetchClient(base, policy)}, nil
}

// Fetch downloads source.URL into destination. env resolves "${NAME}"
// credential references in source; nil means the daemon process environment.
// The caller owns destination and its staging directory.
func (f Fetcher) Fetch(ctx context.Context, source sources.Source, env map[string]string, destination string) (int64, error) {
	rawURL := strings.TrimSpace(source.URL)
	if err := ValidateDownloadURL(rawURL, f.policy.AllowPrivateAddresses); err != nil {
		return 0, fmt.Errorf("validate archive url: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	sources.ApplyHTTPAuthentication(request, source, env)
	response, err := f.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("download archive: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, fmt.Errorf("download archive: unexpected status %s", response.Status)
	}
	if f.policy.RequireZipContentType {
		if err := validateZipResponse(response); err != nil {
			return 0, err
		}
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create archive staging file: %w", err)
	}
	written, copyErr := io.Copy(out, io.LimitReader(response.Body, f.policy.MaxBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		return written, fmt.Errorf("write archive: %w", copyErr)
	}
	if closeErr != nil {
		return written, fmt.Errorf("write archive: %w", closeErr)
	}
	if written > f.policy.MaxBytes {
		// A truncated archive is never useful, so the staging file does not
		// outlive the failed download.
		_ = os.Remove(destination)
		return written, fmt.Errorf("download exceeds %d bytes", f.policy.MaxBytes)
	}
	return written, nil
}

func newFetchClient(base *http.Client, policy FetchPolicy) *http.Client {
	client := http.Client{}
	if base != nil {
		client = *base
	}
	if client.Timeout <= 0 {
		client.Timeout = policy.Timeout
	}
	if client.Transport == nil {
		client.Transport = secureTransport(policy)
	}
	if client.CheckRedirect == nil {
		client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
			if len(via) >= policy.MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", policy.MaxRedirects)
			}
			return ValidateDownloadURL(request.URL.String(), policy.AllowPrivateAddresses)
		}
	}
	return &client
}

func secureTransport(policy FetchPolicy) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// An environment proxy resolves the target itself, which would make the
	// address filter below meaningless, so archive downloads connect directly.
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = policy.ResponseHeaderTimeout
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialer := net.Dialer{Timeout: DefaultDialTimeout}
		if policy.AllowPrivateAddresses {
			return dialer.DialContext(ctx, network, address)
		}
		return dialPublicAddress(ctx, dialer, network, address)
	}
	return transport
}

// ValidateDownloadURL rejects unsupported schemes and, unless explicitly
// allowed, hosts that resolve to a non-public address.
func ValidateDownloadURL(rawURL string, allowPrivateAddresses bool) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported download scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("download host is required")
	}
	if allowPrivateAddresses {
		return nil
	}
	// The check here can only compare the addresses DNS returns now, so the
	// dial path below resolves again and pins the address it actually uses.
	addresses, err := net.LookupIP(host)
	if err != nil {
		return err
	}
	for _, address := range addresses {
		if isPrivateAddress(address) {
			return fmt.Errorf("download host %s resolves to private address %s", host, address)
		}
	}
	return nil
}

func dialPublicAddress(ctx context.Context, dialer net.Dialer, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var selected net.IP
	for _, candidate := range addresses {
		ip := candidate.IP
		if ip == nil || isPrivateAddress(ip) {
			continue
		}
		if network == "tcp4" && ip.To4() == nil {
			continue
		}
		if network == "tcp6" && ip.To4() != nil {
			continue
		}
		selected = ip
		break
	}
	if selected == nil {
		return nil, fmt.Errorf("download host %s has no allowed public address", host)
	}
	return dialer.DialContext(ctx, network, net.JoinHostPort(selected.String(), port))
}

func isPrivateAddress(ip net.IP) bool {
	ip = ip.To16()
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip.Equal(net.ParseIP("169.254.169.254")) {
		return true
	}
	return false
}

func validateZipResponse(response *http.Response) error {
	if response == nil || response.Request == nil || response.Request.URL == nil {
		return nil
	}
	if strings.HasSuffix(strings.ToLower(response.Request.URL.Path), ".zip") {
		return nil
	}
	contentType := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type")))
	if index := strings.Index(contentType, ";"); index >= 0 {
		contentType = strings.TrimSpace(contentType[:index])
	}
	switch contentType {
	case "application/zip", "application/octet-stream", "application/x-zip-compressed", "binary/octet-stream":
		return nil
	default:
		return fmt.Errorf("unexpected content type %q for zip download", response.Header.Get("Content-Type"))
	}
}
