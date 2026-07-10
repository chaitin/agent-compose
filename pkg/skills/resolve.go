package skills

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/workspaces"
)

var (
	credentialURLPattern      = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*:)//[^@\s]+@`)
	gitRemoteHelperURLPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*::`)
)

const (
	DefaultDownloadLimitBytes = 64 << 20
	MaxZipExpandedBytes       = 256 << 20
	MaxZipFiles               = 4096
)

type Resolver struct {
	CacheRoot          string
	Env                map[string]string
	HTTPClient         *http.Client
	DownloadLimitBytes int64
	LocalSourceRoots   []string
}

type ResolvedSkill struct {
	Name     string
	LocalDir string
}

func NewResolver(config *appconfig.Config) Resolver {
	dataRoot := ""
	sandboxRoot := ""
	if config != nil {
		dataRoot = config.DataRoot
		sandboxRoot = config.SandboxRoot
	}
	cacheRoot := ""
	if strings.TrimSpace(dataRoot) != "" {
		cacheRoot = filepath.Join(dataRoot, "skills")
	}
	return Resolver{
		CacheRoot:          cacheRoot,
		Env:                nil,
		HTTPClient:         &http.Client{Timeout: 30 * time.Second},
		DownloadLimitBytes: DefaultDownloadLimitBytes,
		LocalSourceRoots:   configuredLocalSourceRoots(dataRoot, sandboxRoot),
	}
}

func (r Resolver) Resolve(ctx context.Context, specs []domain.AgentSkill) ([]ResolvedSkill, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(r.CacheRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create skills cache root: %w", err)
	}
	resolved := make([]ResolvedSkill, 0, len(specs))
	for _, spec := range domain.NormalizeAgentSkills(specs) {
		current, err := r.resolveOne(ctx, spec)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, current)
	}
	return resolved, nil
}

func (r Resolver) Projected(skills []ResolvedSkill) []execution.ResolvedAgentSkill {
	out := make([]execution.ResolvedAgentSkill, 0, len(skills))
	for _, skill := range skills {
		out = append(out, execution.ResolvedAgentSkill{Name: skill.Name, LocalDir: skill.LocalDir})
	}
	return out
}

func (r Resolver) resolveOne(ctx context.Context, spec domain.AgentSkill) (ResolvedSkill, error) {
	source := strings.ToLower(strings.TrimSpace(spec.Source))
	switch source {
	case "file":
		return r.resolveFile(spec)
	case "git":
		return r.resolveGit(ctx, spec)
	case "zip":
		return r.resolveZip(ctx, spec)
	default:
		return ResolvedSkill{}, fmt.Errorf("skill %s source %q is not supported", spec.Name, spec.Source)
	}
}

func (r Resolver) resolveFile(spec domain.AgentSkill) (ResolvedSkill, error) {
	sourceDir := strings.TrimSpace(spec.Path)
	if sourceDir == "" {
		return ResolvedSkill{}, fmt.Errorf("skill %s file path is required", spec.Name)
	}
	if err := r.validateLocalSource(spec, sourceDir); err != nil {
		return ResolvedSkill{}, err
	}
	key, err := directoryFingerprint(sourceDir)
	if err != nil {
		return ResolvedSkill{}, fmt.Errorf("fingerprint skill %s: %w", spec.Name, err)
	}
	dst := filepath.Join(r.CacheRoot, "file-"+key)
	if err := ensureCachedDir(dst, func(tmp string) error {
		return copyDir(sourceDir, tmp)
	}); err != nil {
		return ResolvedSkill{}, err
	}
	if err := validateSkillDir(spec.Name, dst); err != nil {
		return ResolvedSkill{}, err
	}
	return ResolvedSkill{Name: spec.Name, LocalDir: dst}, nil
}

func (r Resolver) resolveGit(ctx context.Context, spec domain.AgentSkill) (ResolvedSkill, error) {
	rawURL := resolveSecretRefs(strings.TrimSpace(spec.URL), r.Env)
	if localPath, ok, err := localGitSourcePath(rawURL); err != nil {
		return ResolvedSkill{}, fmt.Errorf("validate git skill %s url: %w", spec.Name, err)
	} else if ok {
		if err := r.validateLocalSource(spec, localPath); err != nil {
			return ResolvedSkill{}, err
		}
	}
	if isHTTPURL(rawURL) {
		if err := validateDownloadURL(rawURL); err != nil {
			return ResolvedSkill{}, fmt.Errorf("validate git skill %s url: %w", spec.Name, err)
		}
	}
	urlValue := gitURLWithCredentials(rawURL, spec, r.Env)
	if urlValue == "" {
		return ResolvedSkill{}, fmt.Errorf("skill %s git url is required", spec.Name)
	}
	if err := validateGitOperand("git url", urlValue); err != nil {
		return ResolvedSkill{}, fmt.Errorf("validate git skill %s url: %w", spec.Name, err)
	}
	if err := validateGitURLScheme(urlValue); err != nil {
		return ResolvedSkill{}, fmt.Errorf("validate git skill %s url: %w", spec.Name, err)
	}
	ref := strings.TrimSpace(spec.Ref)
	if err := validateGitOperand("git ref", ref); err != nil {
		return ResolvedSkill{}, fmt.Errorf("validate git skill %s ref: %w", spec.Name, err)
	}
	commit, err := gitResolve(ctx, urlValue, ref)
	if err != nil {
		return ResolvedSkill{}, fmt.Errorf("resolve git skill %s ref: %w", spec.Name, err)
	}
	key := cacheKey("git", gitCacheURL(rawURL), commit, spec.Path)
	dst := filepath.Join(r.CacheRoot, key)
	if err := ensureCachedDir(dst, func(tmp string) error {
		cloneDir := filepath.Join(tmp, "repo")
		if err := runGit(ctx, "", "clone", "--no-checkout", "--", urlValue, cloneDir); err != nil {
			return err
		}
		if err := runGit(ctx, cloneDir, "checkout", commit); err != nil {
			return err
		}
		subdir := strings.TrimSpace(spec.Path)
		src := cloneDir
		if subdir != "" {
			var err error
			src, err = safeArtifactSubdir(cloneDir, subdir)
			if err != nil {
				return err
			}
		}
		content := filepath.Join(tmp, "content")
		return copyDir(src, content)
	}); err != nil {
		return ResolvedSkill{}, err
	}
	content := filepath.Join(dst, "content")
	if err := validateSkillDir(spec.Name, content); err != nil {
		return ResolvedSkill{}, err
	}
	return ResolvedSkill{Name: spec.Name, LocalDir: content}, nil
}

func isHTTPURL(raw string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://")
}

func localGitSourcePath(raw string) (string, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, nil
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "file://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", false, err
		}
		if parsed.Host != "" && parsed.Host != "localhost" {
			return "", false, fmt.Errorf("file git URL host %q is not supported", parsed.Host)
		}
		if parsed.Path == "" {
			return "", false, fmt.Errorf("file git URL path is required")
		}
		return parsed.Path, true, nil
	}
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "."+string(filepath.Separator)) || strings.HasPrefix(trimmed, ".."+string(filepath.Separator)) || trimmed == "." || trimmed == ".." {
		return trimmed, true, nil
	}
	return "", false, nil
}

func (r Resolver) resolveZip(ctx context.Context, spec domain.AgentSkill) (ResolvedSkill, error) {
	archivePath := strings.TrimSpace(spec.URL)
	cleanup := func() {}
	if archivePath == "" {
		archivePath = strings.TrimSpace(spec.Path)
	} else if strings.HasPrefix(strings.ToLower(archivePath), "http://") || strings.HasPrefix(strings.ToLower(archivePath), "https://") {
		path, done, err := r.download(ctx, archivePath)
		if err != nil {
			return ResolvedSkill{}, fmt.Errorf("download skill %s zip: %w", spec.Name, err)
		}
		archivePath = path
		cleanup = done
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(spec.URL)), "http://") &&
		!strings.HasPrefix(strings.ToLower(strings.TrimSpace(spec.URL)), "https://") {
		if err := r.validateLocalSource(spec, archivePath); err != nil {
			return ResolvedSkill{}, err
		}
	}
	defer cleanup()
	hash, err := fileSHA256(archivePath)
	if err != nil {
		return ResolvedSkill{}, fmt.Errorf("hash skill %s zip: %w", spec.Name, err)
	}
	key := cacheKey("zip", hash, spec.Path)
	dst := filepath.Join(r.CacheRoot, key)
	if err := ensureCachedDir(dst, func(tmp string) error {
		return extractZip(archivePath, tmp)
	}); err != nil {
		return ResolvedSkill{}, err
	}
	content := dst
	if spec.URL != "" && spec.Path != "" {
		var err error
		content, err = safeArtifactSubdir(dst, spec.Path)
		if err != nil {
			return ResolvedSkill{}, err
		}
	}
	if err := validateSkillDir(spec.Name, content); err != nil {
		return ResolvedSkill{}, err
	}
	return ResolvedSkill{Name: spec.Name, LocalDir: content}, nil
}

func safeArtifactSubdir(root, subdir string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	trimmed := strings.TrimSpace(subdir)
	if root == "" {
		return "", fmt.Errorf("artifact root is required")
	}
	if trimmed == "" {
		return root, nil
	}
	normalized := filepath.FromSlash(strings.ReplaceAll(trimmed, "\\", "/"))
	if filepath.IsAbs(normalized) {
		return "", fmt.Errorf("skill subpath %q must be relative", subdir)
	}
	clean := filepath.Clean(normalized)
	if clean == "." {
		return root, nil
	}
	target := filepath.Clean(filepath.Join(root, clean))
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("validate skill subpath %q: %w", subdir, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("skill subpath %q escapes fetched content", subdir)
	}
	return target, nil
}

func (r Resolver) download(ctx context.Context, rawURL string) (string, func(), error) {
	if err := validateDownloadURL(rawURL); err != nil {
		return "", nil, err
	}
	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, Transport: secureDownloadTransport()}
	}
	clientCopy := *client
	if clientCopy.Transport == nil {
		clientCopy.Transport = secureDownloadTransport()
	}
	if clientCopy.CheckRedirect == nil {
		clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after %d redirects", len(via))
			}
			return validateDownloadURL(req.URL.String())
		}
	}
	limit := r.DownloadLimitBytes
	if limit <= 0 {
		limit = DefaultDownloadLimitBytes
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, err
	}
	resp, err := clientCopy.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	if err := validateZipResponse(resp); err != nil {
		return "", nil, err
	}
	tmp, err := os.CreateTemp("", "agent-compose-skill-*.zip")
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = tmp.Close() }()
	if _, err := io.Copy(tmp, io.LimitReader(resp.Body, limit+1)); err != nil {
		_ = os.Remove(tmp.Name())
		return "", nil, err
	}
	info, err := tmp.Stat()
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", nil, err
	}
	if info.Size() > limit {
		_ = os.Remove(tmp.Name())
		return "", nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return tmp.Name(), func() { _ = os.Remove(tmp.Name()) }, nil
}

func secureDownloadTransport() *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DialContext = validatedDialContext
	return base
}

func validatedDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var selected net.IP
	for _, item := range ips {
		ip := item.IP
		if ip == nil || isPrivateIP(ip) {
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
	dialer := net.Dialer{Timeout: 30 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(selected.String(), port))
}

func validateDownloadURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("unsupported download scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("download host is required")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("download host %s resolves to private address %s", host, ip)
		}
	}
	return nil
}

func validateZipResponse(resp *http.Response) error {
	if resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return nil
	}
	if strings.HasSuffix(strings.ToLower(resp.Request.URL.Path), ".zip") {
		return nil
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if index := strings.Index(contentType, ";"); index >= 0 {
		contentType = strings.TrimSpace(contentType[:index])
	}
	switch contentType {
	case "application/zip", "application/octet-stream", "application/x-zip-compressed", "binary/octet-stream":
		return nil
	default:
		return fmt.Errorf("unexpected content type %q for zip download", resp.Header.Get("Content-Type"))
	}
}

func configuredLocalSourceRoots(paths ...string) []string {
	roots := make([]string, 0, len(paths)+4)
	for _, path := range paths {
		if normalized := cleanLocalRoot(path); normalized != "" {
			roots = append(roots, normalized)
		}
	}
	for _, path := range filepath.SplitList(os.Getenv("AGENT_COMPOSE_SKILL_SOURCE_ROOTS")) {
		if normalized := cleanLocalRoot(path); normalized != "" {
			roots = append(roots, normalized)
		}
	}
	return uniqueStrings(roots)
}

func (r Resolver) validateLocalSource(spec domain.AgentSkill, sourcePath string) error {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return fmt.Errorf("skill %s local source path is required", spec.Name)
	}
	allowedRoots := append([]string{}, r.LocalSourceRoots...)
	if root := cleanLocalRoot(spec.SourceRoot); root != "" {
		allowedRoots = append(allowedRoots, root)
	}
	allowedRoots = uniqueStrings(allowedRoots)
	if len(allowedRoots) == 0 {
		return fmt.Errorf("skill %s local source %s is not allowed; configure AGENT_COMPOSE_SKILL_SOURCE_ROOTS", spec.Name, sourcePath)
	}
	resolvedSource, err := resolveExistingLocalPath(sourcePath)
	if err != nil {
		return fmt.Errorf("resolve skill %s local source: %w", spec.Name, err)
	}
	for _, root := range allowedRoots {
		resolvedRoot, err := resolveExistingLocalPath(root)
		if err != nil {
			continue
		}
		if pathWithinRoot(resolvedSource, resolvedRoot) {
			return nil
		}
	}
	return fmt.Errorf("skill %s local source %s is outside allowed roots", spec.Name, sourcePath)
}

func cleanLocalRoot(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func resolveExistingLocalPath(path string) (string, error) {
	path = cleanLocalRoot(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func pathWithinRoot(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func isPrivateIP(ip net.IP) bool {
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

func ensureCachedDir(dst string, fill func(tmp string) error) error {
	lock, err := os.OpenFile(dst+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open skills cache lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock skills cache: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	if _, err := os.Stat(filepath.Join(dst, ".ready")); err == nil {
		return nil
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create skills cache temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := fill(tmp); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".ready"), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write skills ready flag: %w", err)
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("replace skills cache dir: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("promote skills cache dir: %w", err)
	}
	return nil
}

func copyDir(src, dst string) error {
	root, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return workspaces.CopyRootDirectoryContents(root, dst)
}

func validateSkillDir(expectedName, dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return fmt.Errorf("skill %s missing SKILL.md: %w", expectedName, err)
	}
	name, description, err := parseSkillFrontmatter(data)
	if err != nil {
		return fmt.Errorf("skill %s invalid SKILL.md: %w", expectedName, err)
	}
	if name == "" || description == "" {
		return fmt.Errorf("skill %s SKILL.md requires name and description", expectedName)
	}
	if expectedName != "" && name != expectedName {
		return fmt.Errorf("skill name mismatch: configured %q, SKILL.md %q", expectedName, name)
	}
	return nil
}

func parseSkillFrontmatter(data []byte) (string, string, error) {
	text := strings.TrimLeft(string(data), "\ufeff\r\n\t ")
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return "", "", fmt.Errorf("frontmatter is required")
	}
	text = strings.TrimPrefix(strings.TrimPrefix(text, "---\r\n"), "---\n")
	end := strings.Index(text, "\n---")
	if end < 0 {
		return "", "", fmt.Errorf("frontmatter terminator is required")
	}
	front := text[:end]
	var name string
	var description string
	for _, line := range strings.Split(front, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	return name, description, nil
}

func directoryFingerprint(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %s is not supported", rel)
		}
		_, _ = h.Write([]byte(filepath.ToSlash(rel)))
		_, _ = h.Write([]byte{0})
		if entry.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		_, err = io.Copy(h, file)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func cacheKey(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("git %s failed: %s", redactGitArgs(args), redactSecrets(message))
}

func gitResolve(ctx context.Context, urlValue, ref string) (string, error) {
	if err := validateGitOperand("git url", urlValue); err != nil {
		return "", err
	}
	target := strings.TrimSpace(ref)
	if target == "" {
		target = "HEAD"
	}
	if err := validateGitOperand("git ref", target); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--", urlValue, target)
	output, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) == "" {
		if target == "HEAD" {
			cmd = exec.CommandContext(ctx, "git", "ls-remote", "--symref", "--", urlValue, "HEAD")
			output, err = cmd.Output()
		}
	}
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && len(fields[0]) == 40 {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("could not resolve ref %q", target)
}

func validateGitOperand(label, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("%s must not start with '-'", label)
	}
	return nil
}

func validateGitURLScheme(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if gitRemoteHelperURLPattern.MatchString(trimmed) {
		return fmt.Errorf("git remote helper URLs are not supported")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" {
		return nil
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ssh", "git", "file":
		return nil
	default:
		return fmt.Errorf("git url scheme %q is not supported", parsed.Scheme)
	}
}

func gitCacheURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return trimmed
	}
	parsed.User = nil
	return parsed.String()
}

func extractZip(path, dst string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	if len(reader.File) > MaxZipFiles {
		return fmt.Errorf("zip contains %d files, max %d", len(reader.File), MaxZipFiles)
	}
	var expanded uint64
	var actualExpanded uint64
	for _, file := range reader.File {
		expanded += file.UncompressedSize64
		if expanded > MaxZipExpandedBytes {
			return fmt.Errorf("zip expanded size exceeds %d bytes", MaxZipExpandedBytes)
		}
		rel := filepath.Clean(strings.ReplaceAll(file.Name, "\\", "/"))
		if rel == "." || rel == "" {
			continue
		}
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("zip entry %q escapes destination", file.Name)
		}
		target := filepath.Join(dst, rel)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("zip symlink %s is not supported", file.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, sanitizedZipFileMode(file.Mode()))
		if err != nil {
			_ = src.Close()
			return err
		}
		copyErr := copyWithExpandedLimit(dstFile, src, &actualExpanded, MaxZipExpandedBytes)
		closeDstErr := dstFile.Close()
		closeSrcErr := src.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeDstErr != nil {
			return closeDstErr
		}
		if closeSrcErr != nil {
			return closeSrcErr
		}
	}
	return nil
}

func sanitizedZipFileMode(mode os.FileMode) os.FileMode {
	perm := mode.Perm()
	if perm == 0 {
		return 0o644
	}
	return perm &^ 0o022
}

func copyWithExpandedLimit(dst io.Writer, src io.Reader, expanded *uint64, limit uint64) error {
	if expanded == nil {
		return fmt.Errorf("expanded size counter is required")
	}
	if *expanded > limit {
		return fmt.Errorf("zip expanded size exceeds %d bytes", limit)
	}
	remaining := limit - *expanded
	written, err := io.Copy(dst, io.LimitReader(src, int64(remaining)+1))
	*expanded += uint64(written)
	if err != nil {
		return err
	}
	if *expanded > limit {
		return fmt.Errorf("zip expanded size exceeds %d bytes", limit)
	}
	return nil
}

func redactGitArgs(args []string) string {
	redacted := make([]string, 0, len(args))
	for _, arg := range args {
		redacted = append(redacted, redactSecrets(arg))
	}
	return strings.Join(redacted, " ")
}

func redactSecrets(value string) string {
	return credentialURLPattern.ReplaceAllString(value, "$1//xxxxx@")
}

func resolveSecretRefs(value string, env map[string]string) string {
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return value
	}
	name := strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
	if env != nil {
		return env[name]
	}
	return os.Getenv(name)
}

func gitURLWithCredentials(raw string, spec domain.AgentSkill, env map[string]string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	username := resolveSecretRefs(strings.TrimSpace(spec.Username), env)
	password := resolveSecretRefs(strings.TrimSpace(spec.Password), env)
	token := resolveSecretRefs(strings.TrimSpace(spec.Token), env)
	if token != "" {
		if username == "" {
			username = "oauth2"
		}
		parsed.User = url.UserPassword(username, token)
		return parsed.String()
	}
	if username != "" || password != "" {
		if password != "" {
			parsed.User = url.UserPassword(username, password)
		} else {
			parsed.User = url.User(username)
		}
	}
	return parsed.String()
}
