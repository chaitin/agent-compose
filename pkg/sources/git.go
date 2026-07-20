package sources

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	credentialURLPattern      = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*:)//[^@\s]+@`)
	gitRemoteHelperURLPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*::`)
)

type GitClient struct {
	Env map[string]string
}

type ResolvedGit struct {
	Commit string
}

func (c GitClient) Resolve(ctx context.Context, source Source) (ResolvedGit, error) {
	source, err := validateGitSource(source)
	if err != nil {
		return ResolvedGit{}, err
	}
	target := gitSourceTarget(source)
	if err := validateGitOperand("git ref", target); err != nil {
		return ResolvedGit{}, err
	}
	tempRoot, err := os.MkdirTemp("", "agent-compose-git-resolve-*")
	if err != nil {
		return ResolvedGit{}, fmt.Errorf("create git ref resolver: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempRoot) }()
	repository := filepath.Join(tempRoot, "repository.git")
	if err := c.run(ctx, "", source, "init", "--bare", "--", repository); err != nil {
		return ResolvedGit{}, fmt.Errorf("initialize git ref resolver: %w", err)
	}
	if err := c.run(ctx, repository, source, "fetch", "--depth=1", "--no-tags", "--", source.URL, target); err != nil {
		return ResolvedGit{}, fmt.Errorf("resolve git ref %q: %w", target, err)
	}
	output, err := c.runOutput(ctx, repository, source, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return ResolvedGit{}, fmt.Errorf("resolve git ref %q to commit: %w", target, err)
	}
	commit := strings.TrimSpace(string(output))
	if commit == "" {
		return ResolvedGit{}, fmt.Errorf("resolve git ref %q: no commit found", target)
	}
	return ResolvedGit{Commit: commit}, nil
}

func (c GitClient) Checkout(ctx context.Context, source Source, destination string) (ResolvedGit, error) {
	resolved, err := c.Resolve(ctx, source)
	if err != nil {
		return ResolvedGit{}, err
	}
	if err := c.CheckoutCommit(ctx, source, resolved.Commit, destination); err != nil {
		return ResolvedGit{}, err
	}
	return resolved, nil
}

func (c GitClient) CheckoutCommit(ctx context.Context, source Source, commit, destination string) error {
	source, err := validateGitSource(source)
	if err != nil {
		return err
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return errors.New("git checkout commit is required")
	}
	if err := validateGitOperand("git commit", commit); err != nil {
		return err
	}
	if err := validateGitCheckoutDestination(destination); err != nil {
		return err
	}
	if err := c.cloneShallowGitRepository(ctx, source, destination); err != nil {
		return err
	}
	if c.hasLocalGitCommit(ctx, source, commit, destination) {
		return c.checkoutLocalCommit(ctx, source, commit, destination)
	}
	// Fetching the resolved commit directly keeps branch, tag, and full-SHA
	// checkouts shallow. Some servers reject fetching a raw or abbreviated SHA;
	// only those repositories use the full-ref fallback needed for correctness.
	if shallowErr := c.run(ctx, destination, source, "fetch", "--depth=1", "--no-tags", "--", "origin", commit); shallowErr != nil {
		if fallbackErr := c.fetchAllGitRefs(ctx, source, destination); fallbackErr != nil {
			return fmt.Errorf("fetch git commit %s: shallow fetch failed: %v; full fetch fallback failed: %w", commit, shallowErr, fallbackErr)
		}
	}
	return c.checkoutLocalCommit(ctx, source, commit, destination)
}

func validateGitSource(source Source) (Source, error) {
	source = source.Normalized()
	if source.Provider != ProviderGit {
		return Source{}, fmt.Errorf("git source provider must be %q", ProviderGit)
	}
	if source.URL == "" {
		return Source{}, errors.New("git source url is required")
	}
	if err := validateGitOperand("git url", source.URL); err != nil {
		return Source{}, err
	}
	if err := validateGitURLScheme(source.URL); err != nil {
		return Source{}, err
	}
	return source, nil
}

func gitSourceTarget(source Source) string {
	if source.Ref != "" {
		return source.Ref
	}
	return "HEAD"
}

func validateGitCheckoutDestination(destination string) error {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return errors.New("git checkout destination is required")
	}
	entries, err := os.ReadDir(destination)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("inspect git checkout destination: %w", err)
	case len(entries) != 0:
		return fmt.Errorf("git checkout destination %s is not empty", destination)
	default:
		return nil
	}
}

func (c GitClient) cloneShallowGitRepository(ctx context.Context, source Source, destination string) error {
	// Start from a real clone rather than git init + fetch so workspaces retain
	// the origin remote and its default remote-tracking branch. --no-local makes
	// depth effective for local-path sources as well as network transports.
	if err := c.run(ctx, "", source, "clone", "--depth=1", "--no-local", "--no-checkout", "--", source.URL, destination); err != nil {
		return fmt.Errorf("clone shallow git source: %w", err)
	}
	return nil
}

func (c GitClient) hasLocalGitCommit(ctx context.Context, source Source, commit, repository string) bool {
	return c.run(ctx, repository, source, "cat-file", "-e", commit+"^{commit}") == nil
}

func (c GitClient) fetchAllGitRefs(ctx context.Context, source Source, repository string) error {
	refspec := "+refs/heads/*:refs/remotes/origin/*"
	args := []string{"fetch", "--unshallow", "--tags", "--", "origin", refspec}
	if err := c.run(ctx, repository, source, args...); err == nil {
		return nil
	}
	return c.run(ctx, repository, source, "fetch", "--tags", "--", "origin", refspec)
}

func (c GitClient) checkoutLocalCommit(ctx context.Context, source Source, commit, repository string) error {
	if err := c.run(ctx, repository, source, "checkout", "--detach", commit); err != nil {
		return fmt.Errorf("checkout git source commit %s: %w", commit, err)
	}
	return nil
}

func (c GitClient) run(ctx context.Context, dir string, source Source, args ...string) error {
	_, err := c.runCombinedOutput(ctx, dir, source, args...)
	return err
}

func (c GitClient) runOutput(ctx context.Context, dir string, source Source, args ...string) ([]byte, error) {
	cmd := c.command(ctx, dir, source, args...)
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	return nil, c.commandError(source, args, output, err)
}

func (c GitClient) runCombinedOutput(ctx context.Context, dir string, source Source, args ...string) ([]byte, error) {
	cmd := c.command(ctx, dir, source, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, nil
	}
	return nil, c.commandError(source, args, output, err)
}

func (c GitClient) command(ctx context.Context, dir string, source Source, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if header := c.authorizationHeader(source); header != "" {
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0="+header,
		)
	}
	return cmd
}

func (c GitClient) authorizationHeader(source Source) string {
	username := ResolveEnvReference(source.Username, c.Env)
	password := ResolveEnvReference(source.Password, c.Env)
	token := ResolveEnvReference(source.Token, c.Env)
	if token != "" {
		if username == "" {
			username = "oauth2"
		}
		password = token
	}
	if username == "" && password == "" {
		return ""
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return "Authorization: Basic " + encoded
}

func (c GitClient) commandError(source Source, args []string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	for _, secret := range []string{
		ResolveEnvReference(source.Password, c.Env),
		ResolveEnvReference(source.Token, c.Env),
	} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "xxxxx")
		}
	}
	message = credentialURLPattern.ReplaceAllString(message, "$1//xxxxx@")
	return fmt.Errorf("git %s failed: %s", strings.Join(redactGitArgs(args), " "), message)
}

func redactGitArgs(args []string) []string {
	redacted := make([]string, len(args))
	for i, arg := range args {
		redacted[i] = credentialURLPattern.ReplaceAllString(arg, "$1//xxxxx@")
	}
	return redacted
}

func validateGitOperand(label, value string) error {
	if strings.HasPrefix(strings.TrimSpace(value), "-") {
		return fmt.Errorf("%s must not start with '-'", label)
	}
	return nil
}

func validateGitURLScheme(value string) error {
	value = strings.TrimSpace(value)
	if gitRemoteHelperURLPattern.MatchString(value) {
		return errors.New("git remote helper URLs are not supported")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return nil
	}
	if parsed.User != nil {
		return errors.New("git URL userinfo is not supported")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ssh", "git", "file":
		return nil
	default:
		return fmt.Errorf("git url scheme %q is not supported", parsed.Scheme)
	}
}
