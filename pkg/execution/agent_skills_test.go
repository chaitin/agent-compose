package execution

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func skillProjectionFixture(t *testing.T, count int) (*domain.Sandbox, []ResolvedAgentSkill) {
	t.Helper()
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "sandbox", "workspace")}}
	source := filepath.Join(root, "source")
	writeSkillProjectionFile(t, filepath.Join(source, "SKILL.md"), "skill version one")
	for i := range count {
		writeSkillProjectionFile(t, filepath.Join(source, "scripts", fmt.Sprintf("%04d.txt", i)), fmt.Sprintf("content %d", i))
	}
	if err := os.MkdirAll(filepath.Join(source, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	return session, []ResolvedAgentSkill{{Name: "pdf", LocalDir: source}}
}

func writeSkillProjectionFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func projectSkills(t *testing.T, session *domain.Sandbox, skills []ResolvedAgentSkill) {
	t.Helper()
	if _, err := WriteAgentSkills(context.Background(), nil, session, skills, nil); err != nil {
		t.Fatal(err)
	}
}

func skillProjectionIdentities(t *testing.T, root string) map[string]agentSkillsFileIdentity {
	t.Helper()
	result := make(map[string]agentSkillsFileIdentity)
	if err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		identity, err := agentSkillsPathIdentity(path)
		result[path] = identity
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertNoSkillStages(t *testing.T, session *domain.Sandbox) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(HostAgentSkillsDir(session)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), agentSkillsUpdatePrefix) || strings.HasPrefix(entry.Name(), ".skills-metadata-") {
			t.Errorf("leaked staging entry %s", entry.Name())
		}
	}
}

func assertSkillProjectionMatches(t *testing.T, session *domain.Sandbox, skills []ResolvedAgentSkill) {
	t.Helper()
	for _, skill := range skills {
		expected, err := FingerprintAgentSkill(context.Background(), skill.LocalDir)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := FingerprintAgentSkill(context.Background(), filepath.Join(HostAgentSkillsDir(session), skill.Name))
		if err != nil || expected != actual {
			t.Fatalf("skill %s fingerprint mismatch: expected=%s actual=%s error=%v", skill.Name, expected, actual, err)
		}
		if got := readAgentSkillsManifest(HostAgentSkillsDir(session)).Fingerprints[skill.Name]; got != expected {
			t.Fatalf("manifest %s = %s, want %s", skill.Name, got, expected)
		}
	}
	assertNoSkillStages(t, session)
}

func TestAgentSkillsUnchangedTreeKeepsEveryInode(t *testing.T) {
	session, skills := skillProjectionFixture(t, 256)
	projectSkills(t, session, skills)
	home := filepath.Join(HostSandboxDir(session), "home")
	before := skillProjectionIdentities(t, home)
	operations := defaultAgentSkillsFileOperations()
	operations.copy = func(context.Context, *os.Root, string) error { return errors.New("unchanged tree must not be copied") }
	for range 8 {
		if _, err := writeAgentSkills(context.Background(), session, skills, nil, operations); err != nil {
			t.Fatal(err)
		}
		if after := skillProjectionIdentities(t, home); !reflect.DeepEqual(before, after) {
			t.Fatal("unchanged preparation replaced an inode or accumulated files")
		}
	}
	t.Logf("preserved all %d home entries over 8 preparations (257 skill files, empty directory, manifest and Claude alias)", len(before))
	assertSkillProjectionMatches(t, session, skills)
}

func TestAgentSkillsRepairsGuestMutationsAndIsolatesSandboxes(t *testing.T) {
	session, skills := skillProjectionFixture(t, 2)
	second := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(t.TempDir(), "workspace")}}
	projectSkills(t, session, skills)
	projectSkills(t, second, skills)
	sourceFingerprint, _ := FingerprintAgentSkill(context.Background(), skills[0].LocalDir)
	firstDir := filepath.Join(HostAgentSkillsDir(session), "pdf")
	secondDir := filepath.Join(HostAgentSkillsDir(second), "pdf")
	writeSkillProjectionFile(t, filepath.Join(firstDir, "SKILL.md"), "guest mutation")
	writeSkillProjectionFile(t, filepath.Join(firstDir, "extra.txt"), "unexpected")
	if err := os.Remove(filepath.Join(firstDir, "scripts", "0000.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(firstDir, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(firstDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{skills[0].LocalDir, secondDir} {
		if actual, err := FingerprintAgentSkill(context.Background(), root); err != nil || actual != sourceFingerprint {
			t.Fatalf("guest mutation escaped private sandbox: %s, %v", root, err)
		}
	}
	projectSkills(t, session, skills)
	assertSkillProjectionMatches(t, session, skills)
	writeSkillProjectionFile(t, filepath.Join(skills[0].LocalDir, "SKILL.md"), "source version two")
	for _, root := range []string{firstDir, secondDir} {
		if actual, _ := FingerprintAgentSkill(context.Background(), root); actual != sourceFingerprint {
			t.Fatal("source mutation changed an already published private copy")
		}
	}
	projectSkills(t, session, skills)
	assertSkillProjectionMatches(t, session, skills)
	if actual, _ := FingerprintAgentSkill(context.Background(), secondDir); actual != sourceFingerprint {
		t.Fatal("refreshing the first sandbox changed the second sandbox")
	}
	projectSkills(t, second, skills)
	assertSkillProjectionMatches(t, second, skills)
}

func TestAgentSkillsRestoresWholeDirectoryAfterFileTypeChange(t *testing.T) {
	session, skills := skillProjectionFixture(t, 1)
	projectSkills(t, session, skills)
	target := filepath.Join(HostAgentSkillsDir(session), "pdf", "scripts", "0000.txt")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	projectSkills(t, session, skills)
	assertSkillProjectionMatches(t, session, skills)
	if err := os.Chmod(filepath.Join(skills[0].LocalDir, "SKILL.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	projectSkills(t, session, skills)
	assertSkillProjectionMatches(t, session, skills)
}

func TestAgentSkillsChecksGuestDespiteUnchangedHost(t *testing.T) {
	session, skills := skillProjectionFixture(t, 1)
	projectSkills(t, session, skills)
	guest := t.TempDir()
	writeSkillProjectionFile(t, filepath.Join(guest, "SKILL.md"), "guest drift")
	writer := func(ctx context.Context, host string) error {
		if got := readAgentSkillsManifest(host).Fingerprints["pdf"]; got == "" {
			t.Fatal("guest received unpublished manifest")
		}
		src, err := os.OpenRoot(filepath.Join(host, "pdf"))
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()
		return defaultAgentSkillsFileOperations().copy(ctx, src, guest)
	}
	if _, err := WriteAgentSkills(context.Background(), nil, session, skills, writer); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(guest, "SKILL.md")); string(got) != "skill version one" {
		t.Fatalf("unchanged host failed to reconcile mutated guest: %s", got)
	}
}

func TestAgentSkillsConcurrentPreparationSerializesPublication(t *testing.T) {
	session, skills := skillProjectionFixture(t, 20)
	projectSkills(t, session, skills)
	writeSkillProjectionFile(t, filepath.Join(skills[0].LocalDir, "SKILL.md"), "concurrent version")
	start := make(chan struct{})
	errors := make(chan error, 6)
	var workers sync.WaitGroup
	for range 6 {
		workers.Go(func() {
			<-start
			_, err := WriteAgentSkills(context.Background(), nil, session, skills, nil)
			errors <- err
		})
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertSkillProjectionMatches(t, session, skills)
}
