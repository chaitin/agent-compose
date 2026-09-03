package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/sources"
)

const (
	defaultScriptSourceTimeout = 10 * time.Second
	maxScriptSourceBytes       = 1 << 20
	maxScriptSourceRedirects   = 5
)

// ScriptSourceResolver fetches a structurally validated, normalized script
// location. Plain paths are absolute; URL locations use file/http/https.
type ScriptSourceResolver interface {
	Resolve(ctx context.Context, source sources.Source) ([]byte, error)
}

// ScriptSourceResolverFunc adapts a function into a ScriptSourceResolver.
type ScriptSourceResolverFunc func(context.Context, sources.Source) ([]byte, error)

func (f ScriptSourceResolverFunc) Resolve(ctx context.Context, source sources.Source) ([]byte, error) {
	return f(ctx, source)
}

type defaultScriptSourceResolver struct {
	client                *http.Client
	env                   map[string]string
	validateNetworkTarget func(context.Context, *url.URL) error
}

// NewDefaultScriptSourceResolver returns the bounded file and HTTP(S) resolver
// used by CLI compose loading.
func NewDefaultScriptSourceResolver(env map[string]string) ScriptSourceResolver {
	resolver := &defaultScriptSourceResolver{env: env, validateNetworkTarget: validateScriptNetworkTarget}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Do not use environment-configured proxies here. A forward proxy resolves
	// and fetches the target outside this process, so the daemon cannot enforce
	// the same public-address SSRF policy on the actual connection.
	transport.Proxy = nil
	transport.DialContext = safeScriptDialContext
	resolver.client = &http.Client{
		Timeout:   defaultScriptSourceTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxScriptSourceRedirects {
				return fmt.Errorf("too many redirects (maximum %d)", maxScriptSourceRedirects)
			}
			if req.URL.User != nil {
				return errors.New("redirect URL userinfo is not allowed")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect scheme %q is not supported", req.URL.Scheme)
			}
			if req.URL.Host == "" || req.URL.Hostname() == "" {
				return errors.New("redirect URL requires a valid host")
			}
			if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
				return errors.New("HTTPS redirect downgrade to HTTP is not allowed")
			}
			return resolver.validateNetworkTarget(req.Context(), req.URL)
		},
	}
	return resolver
}

func normalizeScriptSourceURL(raw string, options NormalizeOptions) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("invalid script URL")
	}
	if parsed.User != nil {
		return "", errors.New("URL userinfo is not allowed")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "":
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", errors.New("local script path must not contain a query or fragment")
		}
		path := raw
		if !filepath.IsAbs(path) {
			path = filepath.Join(scriptSourceBaseDir(options), path)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve local script path: %w", err)
		}
		return filepath.Clean(abs), nil
	case "file":
		if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
			return "", errors.New("file URL authority must be local")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", errors.New("file URL must not contain a query or fragment")
		}
		if parsed.Path == "" {
			return "", errors.New("file URL path is required")
		}
		path, err := url.PathUnescape(parsed.Path)
		if err != nil {
			return "", errors.New("file URL path is invalid")
		}
		if !filepath.IsAbs(path) {
			return "", errors.New("file URL path must be absolute")
		}
		return (&url.URL{Scheme: "file", Path: filepath.Clean(path)}).String(), nil
	case "http", "https":
		if parsed.Host == "" || parsed.Hostname() == "" {
			return "", errors.New("HTTP(S) script URL requires a valid host")
		}
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		return parsed.String(), nil
	default:
		return "", fmt.Errorf("script URL scheme %q is not supported", parsed.Scheme)
	}
}

func scriptSourceBaseDir(options NormalizeOptions) string {
	if path := strings.TrimSpace(options.ComposePath); path != "" {
		return filepath.Dir(path)
	}
	if dir := strings.TrimSpace(options.ProjectDir); dir != "" {
		return dir
	}
	return "."
}

func (r *defaultScriptSourceResolver) Resolve(ctx context.Context, source sources.Source) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, defaultScriptSourceTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source = source.Normalized()
	switch source.Provider {
	case sources.ProviderFile:
		parsed, err := url.Parse(source.Path)
		if err != nil {
			return nil, errors.New("invalid script path")
		}
		path := source.Path
		if parsed.Scheme == "file" {
			path, err = url.PathUnescape(parsed.Path)
			if err != nil {
				return nil, errors.New("invalid file URL path")
			}
		}
		return readScriptFileWithContext(ctx, path)
	case sources.ProviderHTTP:
		parsed, err := url.Parse(source.URL)
		if err != nil {
			return nil, errors.New("invalid script URL")
		}
		return r.readHTTP(ctx, parsed, source)
	case sources.ProviderGit:
		return r.readGit(ctx, source)
	default:
		return nil, fmt.Errorf("script provider %q is not supported", source.Provider)
	}
}

func (r *defaultScriptSourceResolver) readGit(ctx context.Context, source sources.Source) ([]byte, error) {
	tempDir, err := os.MkdirTemp("", "agent-compose-script-git-*")
	if err != nil {
		return nil, fmt.Errorf("create git script temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	checkoutDir := filepath.Join(tempDir, "repository")
	if _, err := (sources.GitClient{Env: r.env}).Checkout(ctx, source, checkoutDir); err != nil {
		return nil, err
	}
	scriptPath, err := resolveGitScriptPath(checkoutDir, source.Path)
	if err != nil {
		return nil, err
	}
	return readScriptFileWithContext(ctx, scriptPath)
}

func resolveGitScriptPath(checkoutDir, sourcePath string) (string, error) {
	root, err := filepath.EvalSymlinks(checkoutDir)
	if err != nil {
		return "", fmt.Errorf("resolve git checkout directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(sourcePath)))
	if err != nil {
		return "", fmt.Errorf("resolve git script path %q: %w", sourcePath, err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", fmt.Errorf("resolve git script path %q relative to checkout: %w", sourcePath, err)
	}
	parentPrefix := ".." + string(filepath.Separator)
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, parentPrefix) {
		return "", fmt.Errorf("git script path %q must stay within the repository", sourcePath)
	}
	return resolved, nil
}

func readScriptFileWithContext(ctx context.Context, path string) ([]byte, error) {
	data, err := readScriptFile(path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return data, err
}

func readScriptFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat script file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("script file %q is not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read script file %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat script file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("script file %q is not a regular file", path)
	}
	return readLimitedScript(file)
}

func (r *defaultScriptSourceResolver) readHTTP(ctx context.Context, location *url.URL, source sources.Source) ([]byte, error) {
	if err := r.validateNetworkTarget(ctx, location); err != nil {
		return nil, fmt.Errorf("fetch script from %s: %w", redactedScriptURL(location), err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create script request for %s", redactedScriptURL(location))
	}
	sources.ApplyHTTPAuthentication(req, source, r.env)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch script from %s: %s%s", redactedScriptURL(location), sanitizeScriptFetchError(err), scriptProxyDisabledDiagnostic())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch script from %s: unexpected HTTP status %d", redactedScriptURL(location), resp.StatusCode)
	}
	return readLimitedScript(resp.Body)
}

func scriptProxyDisabledDiagnostic() string {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return fmt.Sprintf(" (environment %s is intentionally disabled for SSRF protection)", name)
		}
	}
	return ""
}

func validateScriptNetworkTarget(ctx context.Context, target *url.URL) error {
	host := strings.TrimSpace(target.Hostname())
	if host == "" {
		return errors.New("script URL requires a valid host")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve script URL host: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("script URL host resolved to no addresses")
	}
	for _, address := range addresses {
		if !isPublicScriptAddress(address.IP) {
			return fmt.Errorf("script URL host resolves to prohibited address %s", address.IP)
		}
	}
	return nil
}

func safeScriptDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{}
	return safeScriptDialContextWithResolver(ctx, network, address,
		func(ctx context.Context, host string) ([]net.IP, error) {
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			ips := make([]net.IP, 0, len(addresses))
			for _, address := range addresses {
				ips = append(ips, address.IP)
			}
			return ips, nil
		}, dialer.DialContext)
}

func safeScriptDialContextWithResolver(
	ctx context.Context,
	network, address string,
	lookup func(context.Context, string) ([]net.IP, error),
	dial func(context.Context, string, string) (net.Conn, error),
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse script endpoint: %w", err)
	}
	addresses, err := lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve script endpoint: %w", err)
	}
	var dialErrs error
	for _, candidate := range addresses {
		if !isPublicScriptAddress(candidate) {
			continue
		}
		conn, dialErr := dial(ctx, network, net.JoinHostPort(candidate.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		dialErrs = errors.Join(dialErrs, dialErr)
	}
	if dialErrs != nil {
		return nil, dialErrs
	}
	return nil, errors.New("script endpoint has no permitted public address")
}

func isPublicScriptAddress(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, network := range prohibitedScriptNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

var prohibitedScriptNetworks = mustParseScriptNetworks([]string{
	"0.0.0.0/8",
	"100.64.0.0/10", // RFC 6598 carrier-grade NAT.
	"192.0.0.0/24",
	"192.0.2.0/24",    // RFC 5737 documentation.
	"198.18.0.0/15",   // RFC 2544 benchmarking.
	"198.51.100.0/24", // RFC 5737 documentation.
	"203.0.113.0/24",  // RFC 5737 documentation.
	"224.0.0.0/4",
	"240.0.0.0/4",
	"fec0::/10",     // deprecated IPv6 site-local.
	"2001:db8::/32", // IPv6 documentation.
})

func mustParseScriptNetworks(cidrs []string) []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("invalid script network %q: %v", cidr, err))
		}
		networks = append(networks, network)
	}
	return networks
}

func readLimitedScript(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxScriptSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read script content: %w", err)
	}
	if len(data) > maxScriptSourceBytes {
		return nil, fmt.Errorf("script content exceeds %d bytes", maxScriptSourceBytes)
	}
	return data, nil
}

func redactedScriptURL(value *url.URL) string {
	cloned := *value
	if cloned.RawQuery != "" {
		cloned.RawQuery = "redacted"
		cloned.ForceQuery = false
	}
	cloned.User = nil
	return cloned.String()
}

func sanitizeScriptFetchError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Err != nil {
			return urlErr.Err.Error()
		}
		return "request failed"
	}
	return err.Error()
}
