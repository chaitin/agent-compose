package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-compose/pkg/sources"
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
	client *http.Client
	env    map[string]string
}

// NewDefaultScriptSourceResolver returns the bounded file and HTTP(S) resolver
// used by CLI compose loading.
func NewDefaultScriptSourceResolver(env map[string]string) ScriptSourceResolver {
	resolver := &defaultScriptSourceResolver{env: env}
	resolver.client = &http.Client{
		Timeout: defaultScriptSourceTimeout,
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
			return nil
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create script request for %s", redactedScriptURL(location))
	}
	sources.ApplyHTTPAuthentication(req, source, r.env)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch script from %s: %s", redactedScriptURL(location), sanitizeScriptFetchError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch script from %s: unexpected HTTP status %d", redactedScriptURL(location), resp.StatusCode)
	}
	return readLimitedScript(resp.Body)
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
