package compose

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// normalizeDaemonGitScriptURL applies the project source-directory contract to
// local repositories. Remote Git transports retain their existing validation.
func normalizeDaemonGitScriptURL(raw string, options NormalizeOptions) (string, error) {
	localPath, local, err := scriptGitLocalPath(raw)
	if err != nil || !local {
		return raw, err
	}
	root, err := scriptGitProjectRoot(options)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(localPath) {
		localPath = filepath.Join(root, localPath)
	}
	resolved, err := filepath.EvalSymlinks(localPath)
	if err != nil {
		return "", fmt.Errorf("resolve local git script repository: %w", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", fmt.Errorf("locate git script repository within project: %w", err)
	}
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("local git script repository must stay within the project source directory")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect local git script repository: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("local git script repository must be a directory")
	}
	// Resolve relative paths and symlinks before passing the location to Git,
	// whose subprocess working directory is unrelated to the project directory.
	return resolved, nil
}

func scriptGitLocalPath(raw string) (string, bool, error) {
	colon := strings.IndexByte(raw, ':')
	slash := strings.IndexByte(raw, '/')
	// Git treats a colon before the first slash as a transport or SCP-style
	// remote. Bare relative paths, including names without ./, are local.
	if colon < 0 || (slash >= 0 && slash < colon) {
		return raw, true, nil
	}
	if !strings.EqualFold(raw[:colon], "file") {
		return "", false, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false, errors.New("invalid local git script URL")
	}
	if parsed.User != nil || (parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost")) {
		return "", false, errors.New("local git script URL authority must be local and have no userinfo")
	}
	if parsed.Opaque != "" || !filepath.IsAbs(parsed.Path) || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", false, errors.New("local git script URL requires an absolute path without a query or fragment")
	}
	// url.Parse has already decoded the path; decoding it again changes literal
	// percent sequences in repository names and can change the selected path.
	return parsed.Path, true, nil
}

func scriptGitProjectRoot(options NormalizeOptions) (string, error) {
	root := strings.TrimSpace(options.ProjectDir)
	if sourcePath := strings.TrimSpace(options.ComposePath); sourcePath != "" {
		root = filepath.Dir(sourcePath)
		info, err := os.Stat(sourcePath)
		if err == nil && info.IsDir() {
			root = sourcePath
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect project source path: %w", err)
		}
	}
	if root == "" {
		return "", errors.New("project source directory is required for local git script sources")
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("local git script sources require an absolute project source directory")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve project source directory: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("inspect project source directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("project source directory must be a directory")
	}
	return root, nil
}
