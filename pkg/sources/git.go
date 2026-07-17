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
	source = source.Normalized()
	if source.Provider != ProviderGit {
		return ResolvedGit{}, fmt.Errorf("git source provider must be %q", ProviderGit)
	}
	if source.URL == "" {
		return ResolvedGit{}, errors.New("git source url is required")
	}
	if err := validateGitOperand("git url", source.URL); err != nil {
		return ResolvedGit{}, err
	}
	if err := validateGitURLScheme(source.URL); err != nil {
		return ResolvedGit{}, err
	}
	target := source.Ref
	if target == "" {
		target = "HEAD"
	}
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
	source = source.Normalized()
	if source.Provider != ProviderGit {
		return fmt.Errorf("git source provider must be %q", ProviderGit)
	}
	if source.URL == "" {
		return errors.New("git source url is required")
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return errors.New("git checkout commit is required")
	}
	if strings.TrimSpace(destination) == "" {
		return errors.New("git checkout destination is required")
	}
	if err := validateGitOperand("git url", source.URL); err != nil {
		return err
	}
	if err := validateGitURLScheme(source.URL); err != nil {
		return err
	}
	if err := validateGitOperand("git commit", commit); err != nil {
		return err
	}
	if err := c.run(ctx, "", source, "clone", "--no-checkout", "--", source.URL, destination); err != nil {
		return fmt.Errorf("clone git source: %w", err)
	}
	if err := c.run(ctx, destination, source, "checkout", commit); err != nil {
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
