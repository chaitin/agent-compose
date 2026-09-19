package skills

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/json"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sources"
)

func TestResolverResolvesFileSkill(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeSkill(t, source, "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}

	resolved, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Provider: "file", Path: source}})
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

func TestResolverArtifactManifestOmitsSourcePathAndCredentials(t *testing.T) {
	root := t.TempDir()
	const secret = "token-super-secret"
	source := filepath.Join(root, "source-"+secret)
	writeSkill(t, source, "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}

	resolved, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Provider: "file", Path: source, Token: "${TOKEN}"}})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(resolved[0].LocalDir), artifactManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(source)) {
		t.Fatalf("artifact manifest contains source path or credential: %s", data)
	}
	var manifest artifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || manifest.Source != "file" || manifest.Identity == "" || manifest.CreatedAt.IsZero() || manifest.LastUsedAt.IsZero() {
		t.Fatalf("artifact manifest = %#v", manifest)
	}
}

func TestResolverResolvesZipSkillSubdir(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "skills.zip")
	writeZipSkill(t, archivePath, "", "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}

	resolved, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Provider: "file", Format: "zip", Path: archivePath}})
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

func TestResolverResolvesGitSkillWithSharedClient(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	writeSkill(t, filepath.Join(repository, "skills", "pdf"), "pdf")
	runTestGit(t, repository, "init", "-b", "main")
	runTestGit(t, repository, "config", "user.email", "agent-compose@example.test")
	runTestGit(t, repository, "config", "user.name", "Agent Compose")
	runTestGit(t, repository, "add", ".")
	runTestGit(t, repository, "commit", "-m", "add skill")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}

	resolved, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{
		Name: "pdf", Provider: "git", URL: repository, Ref: "main", Path: "skills/pdf",
	}})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("resolved = %#v", resolved)
	}
	if _, err := os.Stat(filepath.Join(resolved[0].LocalDir, "SKILL.md")); err != nil {
		t.Fatalf("resolved Git SKILL.md: %v", err)
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

	_, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Provider: "git", URL: repo, Path: "../../.."}})
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
			_, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Provider: "git", URL: rawURL}})
			if err == nil {
				t.Fatalf("expected Resolve to reject local git outside allowed roots")
			}
			if !strings.Contains(err.Error(), "is outside allowed roots") {
				t.Fatalf("error = %q, want allowed roots validation", err)
			}
		})
	}
}

// TestResolverAcceptsRemoteGitInternalHosts covers the policy for git skills:
// an internal GitLab on a private or loopback address is a normal source, so
// the URL passes validation and the failure comes from the clone attempt
// itself. Only the scheme and host shape are enforced for remote git URLs.
func TestResolverAcceptsRemoteGitInternalHosts(t *testing.T) {
	resolver := Resolver{CacheRoot: filepath.Join(t.TempDir(), "cache")}

	_, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Provider: "git", URL: "http://127.0.0.1/repo.git"}})
	if err == nil {
		t.Fatal("expected the unreachable internal repository to fail the clone")
	}
	if strings.Contains(err.Error(), "validate git skill pdf url") {
		t.Fatalf("error = %q, want the internal host to pass url validation", err)
	}
	if !strings.Contains(err.Error(), "resolve git skill pdf ref") {
		t.Fatalf("error = %q, want a clone failure", err)
	}
}

func TestResolverRejectsRemoteGitMalformedURL(t *testing.T) {
	resolver := Resolver{CacheRoot: filepath.Join(t.TempDir(), "cache")}

	_, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Provider: "git", URL: "http:///repo.git"}})
	if err == nil || !strings.Contains(err.Error(), "validate git skill pdf url") {
		t.Fatalf("error = %q, want a url shape rejection", err)
	}
}

func TestResolverRejectsZipSubdirTraversal(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "skills.zip")
	writeZipSkill(t, archivePath, "skills/pdf", "pdf")

	_, err := safeArtifactSubdir(archivePath, "../../..")
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

	if _, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "docx", Provider: "file", Path: source}}); err == nil {
		t.Fatalf("expected Resolve to reject mismatched skill name")
	}
}

func TestResolverRejectsLocalSourceOutsideAllowedRoots(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside")
	writeSkill(t, outside, "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{allowed}}

	if _, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Provider: "file", Path: outside}}); err == nil {
		t.Fatalf("expected Resolve to reject outside local source")
	}
}

func TestResolverAllowsComposeSourceRoot(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "project")
	source := filepath.Join(sourceRoot, "skills", "pdf")
	writeSkill(t, source, "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache")}

	resolved, err := resolver.Resolve(context.Background(), []domain.AgentSkill{{Name: "pdf", Provider: "file", Path: source, SourceRoot: sourceRoot}})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Name != "pdf" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestGitCacheURLStripsCredentials(t *testing.T) {
	got := gitCacheURL("https://user:secret@git.example/repo.git")
	if got != "https://git.example/repo.git" {
		t.Fatalf("git cache url = %q", got)
	}
}

// TestResolverRejectsNonHTTPSkillURL covers what still has to hold now that
// private hosts are accepted: only http and https reach the wire. Any other
// URL is treated as a local source, so it must live under an allowed root and
// never reaches the HTTP fetcher.
func TestResolverRejectsNonHTTPSkillURL(t *testing.T) {
	for _, rawURL := range []string{"file:///tmp/skill.zip", "ftp://downloads.example.com/skill.zip"} {
		t.Run(rawURL, func(t *testing.T) {
			resolver := Resolver{CacheRoot: t.TempDir()}
			_, err := resolver.Resolve(context.Background(), []domain.AgentSkill{
				{Name: "pdf", Provider: "http", Format: "zip", URL: rawURL},
			})
			if err == nil || !strings.Contains(err.Error(), "is not allowed") {
				t.Fatalf("error = %v, want a local source rejection", err)
			}
		})
	}

	// A caller that hands the URL straight to the fetcher still gets a scheme
	// rejection, so the wire stays http/https only.
	resolver := Resolver{CacheRoot: t.TempDir()}
	if _, _, err := resolver.download(context.Background(), "file:///tmp/skill.zip", sources.Source{}); err == nil || !strings.Contains(err.Error(), "unsupported download scheme") {
		t.Fatalf("download error = %v, want a scheme rejection", err)
	}
}

// TestResolverIntegrationFetchesZipSkillFromInternalHost covers the intended
// internal-artifact-server path end to end: the resolver downloads from a
// loopback host, expands the archive, and resolves the skill from it.
func TestResolverIntegrationFetchesZipSkillFromInternalHost(t *testing.T) {
	payload := zipSkillArchive(t, "pdf")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	resolver := Resolver{CacheRoot: t.TempDir()}
	resolved, err := resolver.Resolve(context.Background(), []domain.AgentSkill{
		{Name: "pdf", Provider: "http", Format: "zip", URL: server.URL + "/skills.zip"},
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v, want the internal host to be fetched", err)
	}
	if len(resolved) != 1 || resolved[0].Name != "pdf" {
		t.Fatalf("resolved = %#v", resolved)
	}
	if _, err := os.Stat(filepath.Join(resolved[0].LocalDir, "SKILL.md")); err != nil {
		t.Fatalf("resolved skill directory is missing SKILL.md: %v", err)
	}
}

// zipSkillArchive builds a skill archive in memory for HTTP fixtures.
func zipSkillArchive(t *testing.T, name string) []byte {
	t.Helper()
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	header := &zip.FileHeader{Name: "SKILL.md"}
	header.SetMode(0o644)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("---\nname: " + name + "\ndescription: Test skill\n---\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return payload.Bytes()
}

// TestDownloadFollowsRedirectToInternalHost covers the redirect policy for the
// shared fetcher through download: a redirect to an internal host is followed,
// and the archive is staged for the caller.
func TestDownloadFollowsRedirectToInternalHost(t *testing.T) {
	var requests int
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{
				StatusCode: http.StatusFound,
				Status:     "302 Found",
				Header:     http.Header{"Location": []string{"http://127.0.0.1/skill.zip"}},
				Body:       http.NoBody,
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/zip"}},
			Body:       io.NopCloser(strings.NewReader("not-a-zip")),
			Request:    req,
		}, nil
	})}
	resolver := Resolver{HTTPClient: client}
	path, cleanup, err := resolver.download(context.Background(), "http://93.184.216.34/skill.zip", sources.Source{})
	if err != nil {
		t.Fatalf("download returned error: %v, want the internal redirect to be followed", err)
	}
	defer cleanup()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("staged archive %s is missing: %v", path, err)
	}
}

func TestDownloadAppliesLiteralSourceAuthentication(t *testing.T) {
	t.Setenv("TOKEN", "process-value-must-not-be-used")
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Authorization"); got != "Bearer ${TOKEN}" {
			t.Errorf("Authorization = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/zip"}},
			Body:       io.NopCloser(strings.NewReader("not-a-zip")),
			Request:    request,
		}, nil
	})}
	resolver := Resolver{HTTPClient: client}
	path, cleanup, err := resolver.download(context.Background(), "https://example.com/skill.zip", sources.Source{Token: "${TOKEN}"})
	if err != nil {
		t.Fatalf("download returned error: %v", err)
	}
	cleanup()
	if path == "" {
		t.Fatal("download path is empty")
	}
}

func TestDownloadUsesResolvedSourceAuthenticationWithoutEnvironmentLookup(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Authorization"); got != "Bearer resolved-skill-secret" {
			t.Errorf("Authorization = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/zip"}},
			Body:       io.NopCloser(strings.NewReader("not-a-zip")),
			Request:    request,
		}, nil
	})}
	resolver := Resolver{HTTPClient: client}
	path, cleanup, err := resolver.download(context.Background(), "https://example.com/skill.zip", sources.Source{Token: "resolved-skill-secret"})
	if err != nil {
		t.Fatalf("download returned error: %v", err)
	}
	cleanup()
	if path == "" {
		t.Fatal("download path is empty")
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

// TestExtractZipRejectsDeclaredOversizeArchive proves the skill limits are
// bound to the shared extractor: an archive declaring more than
// MaxZipExpandedBytes is rejected before any content is written.
func TestExtractZipRejectsDeclaredOversizeArchive(t *testing.T) {
	payload := []byte("small")
	var compressed bytes.Buffer
	deflater, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deflater.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := deflater.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	archivePath := filepath.Join(root, "declared.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	header := &zip.FileHeader{Name: "entry.bin", Method: zip.Deflate}
	header.UncompressedSize64 = uint64(MaxZipExpandedBytes) + 1
	header.CompressedSize64 = uint64(compressed.Len())
	header.CRC32 = crc32.ChecksumIEEE(payload)
	entry, err := writer.CreateRaw(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(compressed.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if err := extractZip(archivePath, filepath.Join(root, "out")); err == nil || !strings.Contains(err.Error(), "expanded size exceeds") {
		t.Fatalf("extractZip error = %v, want expanded size limit", err)
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
