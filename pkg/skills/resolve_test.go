package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestResolverResolvesFileSkill(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeSkill(t, source, "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}

	resolved, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Source: "file", Path: source}})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Name != "pdf" {
		t.Fatalf("resolved = %#v", resolved)
	}
	if _, err := os.Stat(filepath.Join(resolved[0].LocalDir, "SKILL.md")); err != nil {
		t.Fatalf("resolved SKILL.md missing: %v", err)
	}
}

func TestResolverResolvesZipSkillSubdir(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "skills.zip")
	writeZipSkill(t, archivePath, "skills/pdf", "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}

	resolved, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Source: "zip", URL: archivePath, Path: "skills/pdf"}})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Name != "pdf" {
		t.Fatalf("resolved = %#v", resolved)
	}
	if _, err := os.Stat(filepath.Join(resolved[0].LocalDir, "SKILL.md")); err != nil {
		t.Fatalf("resolved SKILL.md missing: %v", err)
	}
}

func TestResolverRejectsGitSubdirTraversal(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	writeSkill(t, filepath.Join(repo, "skills", "pdf"), "pdf")
	runTestGit(t, repo, "init")
	runTestGit(t, repo, "config", "user.email", "test@example.com")
	runTestGit(t, repo, "config", "user.name", "Test User")
	runTestGit(t, repo, "add", ".")
	runTestGit(t, repo, "commit", "-m", "add skill")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}

	_, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Source: "git", URL: repo, Path: "../../.."}})
	if err == nil {
		t.Fatalf("expected Resolve to reject git subdir traversal")
	}
	if !strings.Contains(err.Error(), "escapes fetched content") {
		t.Fatalf("error = %q, want fetched content escape validation", err)
	}
}

func TestResolverRejectsLocalGitOutsideAllowedRoots(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside.git")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("create outside git dir: %v", err)
	}
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{allowed}}

	for _, rawURL := range []string{outside, "file://" + filepath.ToSlash(outside)} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Source: "git", URL: rawURL}})
			if err == nil {
				t.Fatalf("expected Resolve to reject local git outside allowed roots")
			}
			if !strings.Contains(err.Error(), "is outside allowed roots") {
				t.Fatalf("error = %q, want allowed roots validation", err)
			}
		})
	}
}

func TestResolverRejectsRemoteGitPrivateHosts(t *testing.T) {
	resolver := Resolver{CacheRoot: filepath.Join(t.TempDir(), "cache")}

	for _, rawURL := range []string{
		"http://127.0.0.1/repo.git",
		"http://169.254.169.254/repo.git",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Source: "git", URL: rawURL}})
			if err == nil {
				t.Fatalf("expected Resolve to reject private git host")
			}
			if !strings.Contains(err.Error(), "validate git skill pdf url") {
				t.Fatalf("error = %q, want git url validation", err)
			}
		})
	}
}

func TestResolverRejectsZipSubdirTraversal(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "skills.zip")
	writeZipSkill(t, archivePath, "skills/pdf", "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}

	_, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Source: "zip", URL: archivePath, Path: "../../.."}})
	if err == nil {
		t.Fatalf("expected Resolve to reject zip subdir traversal")
	}
	if !strings.Contains(err.Error(), "escapes fetched content") {
		t.Fatalf("error = %q, want fetched content escape validation", err)
	}
}

func TestResolverRejectsSkillNameMismatch(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeSkill(t, source, "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}

	if _, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "docx", Source: "file", Path: source}}); err == nil {
		t.Fatalf("expected Resolve to reject mismatched skill name")
	}
}

func TestResolverRejectsLocalSourceOutsideAllowedRoots(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside")
	writeSkill(t, outside, "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{allowed}}

	if _, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Source: "file", Path: outside}}); err == nil {
		t.Fatalf("expected Resolve to reject outside local source")
	}
}

func TestResolverAllowsComposeSourceRoot(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "project")
	source := filepath.Join(sourceRoot, "skills", "pdf")
	writeSkill(t, source, "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache")}

	resolved, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Source: "file", Path: source, SourceRoot: sourceRoot}})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Name != "pdf" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestGitURLWithCredentials(t *testing.T) {
	got := gitURLWithCredentials("https://git.example/repo.git", domain.AgentSkill{
		Username: "user",
		Token:    "${GIT_TOKEN}",
	}, map[string]string{"GIT_TOKEN": "secret"})
	if got != "https://user:secret@git.example/repo.git" {
		t.Fatalf("git url = %q", got)
	}
}

func TestResolveSecretRefsUsesScopedEnvWhenProvided(t *testing.T) {
	t.Setenv("GIT_TOKEN", "daemon-token")
	if got := resolveSecretRefs("${GIT_TOKEN}", map[string]string{}); got != "" {
		t.Fatalf("resolveSecretRefs with scoped empty env = %q, want empty", got)
	}
	if got := resolveSecretRefs("${GIT_TOKEN}", map[string]string{"GIT_TOKEN": "agent-token"}); got != "agent-token" {
		t.Fatalf("resolveSecretRefs with scoped env = %q, want agent-token", got)
	}
	if got := resolveSecretRefs("${GIT_TOKEN}", nil); got != "daemon-token" {
		t.Fatalf("resolveSecretRefs with nil env = %q, want daemon-token", got)
	}
}

func TestGitCacheURLStripsCredentials(t *testing.T) {
	got := gitCacheURL("https://user:secret@git.example/repo.git")
	if got != "https://git.example/repo.git" {
		t.Fatalf("git cache url = %q", got)
	}
}

func TestValidateGitOperandRejectsOptionLikeValue(t *testing.T) {
	for _, value := range []string{"--upload-pack=touch /tmp/pwned", "-c core.sshCommand=bad"} {
		t.Run(value, func(t *testing.T) {
			if err := validateGitOperand("git url", value); err == nil {
				t.Fatalf("expected option-like git operand to be rejected")
			}
		})
	}
}

func TestValidateGitURLSchemeRejectsRemoteHelpers(t *testing.T) {
	for _, value := range []string{"ext::sh -c id", "hg::https://example.invalid/repo"} {
		t.Run(value, func(t *testing.T) {
			if err := validateGitURLScheme(value); err == nil {
				t.Fatalf("expected git remote helper URL to be rejected")
			}
		})
	}
}

func TestValidateGitURLSchemeRejectsUnsupportedSchemes(t *testing.T) {
	if err := validateGitURLScheme("ftp://example.invalid/repo.git"); err == nil {
		t.Fatalf("expected unsupported git URL scheme to be rejected")
	}
	for _, value := range []string{
		"https://example.invalid/repo.git",
		"https://[2001:db8::1]/repo.git",
		"ssh://user@[2001:db8::1]/repo.git",
		"https://example.invalid/repo.git?note=a::b",
		"ssh://git@example.invalid/repo.git",
		"git@example.invalid:org/repo.git",
		"/tmp/repo.git",
	} {
		t.Run(value, func(t *testing.T) {
			if err := validateGitURLScheme(value); err != nil {
				t.Fatalf("validateGitURLScheme returned error: %v", err)
			}
		})
	}
}

func TestValidateDownloadURLRejectsPrivateHosts(t *testing.T) {
	if err := validateDownloadURL("file:///tmp/skill.zip"); err == nil {
		t.Fatalf("expected non-http scheme to be rejected")
	}
	if err := validateDownloadURL("http://127.0.0.1/skill.zip"); err == nil {
		t.Fatalf("expected loopback host to be rejected")
	}
}

func TestValidatedDialContextRejectsPrivateAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1:80", "169.254.169.254:80"} {
		t.Run(address, func(t *testing.T) {
			conn, err := validatedDialContext(context.Background(), "tcp", address)
			if conn != nil {
				_ = conn.Close()
			}
			if err == nil {
				t.Fatalf("expected private address dial to be rejected")
			}
			if !strings.Contains(err.Error(), "no allowed public address") {
				t.Fatalf("error = %q, want public address validation", err)
			}
		})
	}
}

func TestDownloadRejectsRedirectToPrivateHost(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Status:     "302 Found",
			Header:     http.Header{"Location": []string{"http://127.0.0.1/skill.zip"}},
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})}
	resolver := Resolver{HTTPClient: client}
	if _, _, err := resolver.download(context.Background(), "http://93.184.216.34/skill.zip"); err == nil {
		t.Fatalf("expected redirect to private host to be rejected")
	}
}

func TestExtractZipRejectsBackslashTraversal(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "escape.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("..\\escape")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := entry.Write([]byte("escape")); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}
	if err := extractZip(archivePath, filepath.Join(root, "out")); err == nil {
		t.Fatalf("expected backslash traversal to be rejected")
	}
}

func TestExtractZipSanitizesEntryModes(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "modes.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(file)
	dirHeader := &zip.FileHeader{Name: "bin/"}
	dirHeader.SetMode(os.ModeDir | os.ModeSetgid | os.ModeSticky | 0o777)
	if _, err := writer.CreateHeader(dirHeader); err != nil {
		t.Fatalf("create zip dir entry: %v", err)
	}
	fileHeader := &zip.FileHeader{Name: "bin/run.sh"}
	fileHeader.SetMode(os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0o755)
	entry, err := writer.CreateHeader(fileHeader)
	if err != nil {
		t.Fatalf("create zip file entry: %v", err)
	}
	if _, err := entry.Write([]byte("#!/bin/sh\n")); err != nil {
		t.Fatalf("write zip file entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	out := filepath.Join(root, "out")
	if err := extractZip(archivePath, out); err != nil {
		t.Fatalf("extractZip returned error: %v", err)
	}
	dirInfo, err := os.Stat(filepath.Join(out, "bin"))
	if err != nil {
		t.Fatalf("stat extracted dir: %v", err)
	}
	if dirInfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("dir mode kept special bits: %v", dirInfo.Mode())
	}
	if dirInfo.Mode().Perm()&0o022 != 0 {
		t.Fatalf("dir mode kept group/world writable bits: %v", dirInfo.Mode().Perm())
	}
	fileInfo, err := os.Stat(filepath.Join(out, "bin", "run.sh"))
	if err != nil {
		t.Fatalf("stat extracted file: %v", err)
	}
	if fileInfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("file mode kept special bits: %v", fileInfo.Mode())
	}
	if fileInfo.Mode().Perm()&0o022 != 0 {
		t.Fatalf("file mode kept group/world writable bits: %v", fileInfo.Mode().Perm())
	}
	if fileInfo.Mode().Perm() != 0o755 {
		t.Fatalf("file perm = %v, want 0755", fileInfo.Mode().Perm())
	}
}

func TestSanitizedZipFileMode(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
		want os.FileMode
	}{
		{name: "default", mode: 0, want: 0o644},
		{name: "world writable file", mode: 0o666, want: 0o644},
		{name: "world writable executable", mode: 0o777, want: 0o755},
		{name: "special bits", mode: os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0o777, want: 0o755},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizedZipFileMode(tt.mode); got != tt.want {
				t.Fatalf("sanitizedZipFileMode(%v) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

func TestCopyWithExpandedLimitTracksActualBytes(t *testing.T) {
	var expanded uint64
	var out bytes.Buffer
	err := copyWithExpandedLimit(&out, strings.NewReader("123456"), &expanded, 5)
	if err == nil {
		t.Fatalf("expected actual expanded bytes to be limited")
	}
	if expanded != 6 {
		t.Fatalf("expanded = %d, want 6", expanded)
	}
}

func TestRedactSecrets(t *testing.T) {
	got := redactSecrets("fatal: https://user:secret@git.example/repo.git failed")
	if strings.Contains(got, "secret") || !strings.Contains(got, "https://xxxxx@git.example") {
		t.Fatalf("redacted = %q", got)
	}
}

func writeSkill(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	data := []byte("---\nname: " + name + "\ndescription: Test skill\n---\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), data, 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func writeZipSkill(t *testing.T, archivePath, skillDir, name string) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer func() { _ = file.Close() }()
	writer := zip.NewWriter(file)
	defer func() { _ = writer.Close() }()
	entry, err := writer.Create(filepath.ToSlash(filepath.Join(skillDir, "SKILL.md")))
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := entry.Write([]byte("---\nname: " + name + "\ndescription: Test skill\n---\n")); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
