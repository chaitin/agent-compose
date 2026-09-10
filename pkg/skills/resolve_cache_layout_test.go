package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/cache"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sources"
)

func TestResolverVersionedFileAndZipLayoutsPreserveLegacyArtifacts(t *testing.T) {
	for _, format := range []string{"directory", "zip"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}
			source := filepath.Join(root, "source")
			writeSkill(t, source, "pdf")
			spec := domain.AgentSkill{Name: "pdf", Provider: "file", Path: source}
			legacyHash := sha256.New()
			_, _ = legacyHash.Write([]byte("SKILL.md\x00"))
			sourceBytes, err := os.ReadFile(filepath.Join(source, "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = legacyHash.Write(sourceBytes)
			legacyKey := "file-" + hex.EncodeToString(legacyHash.Sum(nil))
			if format == "zip" {
				spec.Path, spec.Format = filepath.Join(root, "skill.zip"), "zip"
				writeZipSkill(t, spec.Path, "", "pdf")
				hash, err := fileSHA256(spec.Path)
				if err != nil {
					t.Fatal(err)
				}
				legacyKey = cacheKey("zip", hash, spec.Path)
			}
			legacy := filepath.Join(resolver.CacheRoot, legacyKey)
			writeSkill(t, legacy, "pdf")
			if err := os.WriteFile(filepath.Join(legacy, ".ready"), []byte("legacy ready"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := touchArtifactManifest(legacy, "legacy", legacyKey); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(filepath.Join(legacy, "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := resolver.Resolve(context.Background(), []domain.AgentSkill{spec})
			if err != nil {
				t.Fatal(err)
			}
			if resolved[0].LocalDir == legacy || filepath.Base(resolved[0].LocalDir) != "content" {
				t.Fatalf("new resolver confused legacy root and content layout: %s", resolved[0].LocalDir)
			}
			after, err := os.Stat(filepath.Join(legacy, "SKILL.md"))
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("legacy cache entry was destructively migrated")
			}
			listing, err := (cache.SkillSource{Root: resolver.CacheRoot}).List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			artifacts := 0
			for _, item := range listing.Items {
				if item.Kind == cache.KindSkillArtifact {
					artifacts++
					if item.Path != legacy && item.Path != filepath.Dir(resolved[0].LocalDir) {
						t.Fatalf("cache key pointed below artifact entry: %s", item.Path)
					}
				}
			}
			if artifacts != 2 {
				t.Fatalf("inventory lost old or new artifact: %d", artifacts)
			}
		})
	}
}

func TestResolverReusesLegacyGitContentLayoutWithoutRebuilding(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repository")
	writeSkill(t, filepath.Join(repo, "skills", "pdf"), "pdf")
	runTestGit(t, repo, "init", "-b", "main")
	runTestGit(t, repo, "config", "user.email", "test@example.test")
	runTestGit(t, repo, "config", "user.name", "Test")
	runTestGit(t, repo, "add", ".")
	runTestGit(t, repo, "commit", "-m", "fixture")
	spec := domain.AgentSkill{Name: "pdf", Provider: "git", URL: repo, Ref: "main", Path: "skills/pdf"}
	commit, err := (sources.GitClient{}).Resolve(context.Background(), domain.AgentSkillSource(spec))
	if err != nil {
		t.Fatal(err)
	}
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}
	legacy := filepath.Join(resolver.CacheRoot, cacheKey("git", gitCacheURL(repo), commit.Commit, spec.Path))
	content := filepath.Join(legacy, "content")
	writeSkill(t, content, "pdf")
	if err := os.WriteFile(filepath.Join(legacy, ".ready"), []byte("legacy ready"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := touchArtifactManifest(legacy, "git", commit.Commit); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(filepath.Join(content, "SKILL.md"))
	resolved, err := resolver.Resolve(context.Background(), []domain.AgentSkill{spec})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(content, "SKILL.md"))
	if resolved[0].LocalDir != content || resolved[0].Fingerprint == "" || !os.SameFile(before, after) {
		t.Fatal("legacy Git content cache was missed or incorrectly fingerprinted")
	}
}

func TestResolverHTTPZipPublishesOnlySkillContent(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "skills.zip")
	writeZipSkill(t, archive, "skills/pdf", "pdf")
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header), Request: request}, nil
	})}
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), HTTPClient: client}
	spec := domain.AgentSkill{Name: "pdf", Provider: "http", URL: "https://example.com/skills.zip", Format: "zip", Path: "skills/pdf"}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "sandbox", "workspace")}}
	if err := resolver.WithResolved(context.Background(), []domain.AgentSkill{spec}, func(resolved []ResolvedSkill) error {
		if !strings.Contains(resolved[0].LocalDir, string(filepath.Separator)+"content"+string(filepath.Separator)) || resolved[0].Fingerprint == "" {
			t.Fatalf("HTTP ZIP content identity missing: %+v", resolved)
		}
		_, err := execution.WriteAgentSkills(context.Background(), nil, session, resolver.Projected(resolved), nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(execution.HostAgentSkillsDir(session), "pdf"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		t.Fatalf("HTTP ZIP projection includes bookkeeping or missed contents: %v %v", entries, err)
	}
}
