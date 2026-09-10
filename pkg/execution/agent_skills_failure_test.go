package execution

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestAgentSkillsFailedPreparationPreservesPublishedTree(t *testing.T) {
	for _, failure := range []string{"copy", "cancel-copy", "exchange", "guest", "cancel-guest", "source-drift"} {
		t.Run(failure, func(t *testing.T) {
			session, skills := skillProjectionFixture(t, 2)
			projectSkills(t, session, skills)
			skillsDir := HostAgentSkillsDir(session)
			before, err := FingerprintAgentSkill(context.Background(), skillsDir)
			if err != nil {
				t.Fatal(err)
			}
			oldManifest, _ := os.ReadFile(filepath.Join(skillsDir, agentSkillsManifestFileName))
			writeSkillProjectionFile(t, filepath.Join(skills[0].LocalDir, "SKILL.md"), "version two")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			operations := defaultAgentSkillsFileOperations()
			var writer GuestSkillsWriterFunc
			sentinel := errors.New("injected failure")
			switch failure {
			case "copy":
				operations.copy = func(_ context.Context, _ *os.Root, dst string) error {
					writeSkillProjectionFile(t, filepath.Join(dst, "partial"), "incomplete")
					return sentinel
				}
			case "cancel-copy":
				copy := operations.copy
				operations.copy = func(ctx context.Context, src *os.Root, dst string) error {
					err := copy(ctx, src, dst)
					cancel()
					return err
				}
			case "exchange":
				operations.exchange = func(string, string) error { return syscall.ENOTSUP }
			case "guest", "cancel-guest":
				writer = func(_ context.Context, host string) error {
					manifest := readAgentSkillsManifest(host)
					if got, _ := FingerprintAgentSkill(context.Background(), filepath.Join(host, "pdf")); got != manifest.Fingerprints["pdf"] {
						t.Fatal("guest callback saw manifest inconsistent with published host")
					}
					if failure == "cancel-guest" {
						cancel()
						return nil
					}
					return sentinel
				}
			case "source-drift":
				skills[0].Fingerprint = "stale resolved fingerprint"
			}
			if _, err := writeAgentSkills(ctx, session, skills, writer, operations); err == nil {
				t.Fatal("expected preparation failure")
			}
			after, err := FingerprintAgentSkill(context.Background(), skillsDir)
			if err != nil || after != before {
				t.Fatalf("failure damaged published tree: before=%s after=%s error=%v", before, after, err)
			}
			if manifest, _ := os.ReadFile(filepath.Join(skillsDir, agentSkillsManifestFileName)); !bytes.Equal(manifest, oldManifest) {
				t.Fatal("failure changed the previous manifest")
			}
			assertNoSkillStages(t, session)
		})
	}
}

func TestAgentSkillsLaterPublicationFailureRollsBackEarlierDirectory(t *testing.T) {
	session, skills := skillProjectionFixture(t, 1)
	second := filepath.Join(t.TempDir(), "other")
	writeSkillProjectionFile(t, filepath.Join(second, "SKILL.md"), "second old")
	skills = append(skills, ResolvedAgentSkill{Name: "other", LocalDir: second})
	projectSkills(t, session, skills)
	before, _ := FingerprintAgentSkill(context.Background(), HostAgentSkillsDir(session))
	for _, skill := range skills {
		writeSkillProjectionFile(t, filepath.Join(skill.LocalDir, "SKILL.md"), "new version")
	}
	operations := defaultAgentSkillsFileOperations()
	exchange := operations.exchange
	count := 0
	operations.exchange = func(a, b string) error {
		count++
		if count == 2 {
			return syscall.ENOTSUP
		}
		return exchange(a, b)
	}
	if _, err := writeAgentSkills(context.Background(), session, skills, nil, operations); err == nil {
		t.Fatal("expected second publication to fail")
	}
	if actual, _ := FingerprintAgentSkill(context.Background(), HostAgentSkillsDir(session)); actual != before {
		t.Fatal("earlier directory or manifest was not rolled back")
	}
	assertNoSkillStages(t, session)
	projectSkills(t, session, skills)
	assertSkillProjectionMatches(t, session, skills)
}

func TestAgentSkillsRecoveryRetainsCompletePriorOrCommittedTree(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted", true: "committed"}[committed], func(t *testing.T) {
			session, skills := skillProjectionFixture(t, 2)
			projectSkills(t, session, skills)
			old, _ := FingerprintAgentSkill(context.Background(), HostAgentSkillsDir(session))
			writeSkillProjectionFile(t, filepath.Join(skills[0].LocalDir, "SKILL.md"), "recoverable new version")
			current, names, err := normalizeResolvedAgentSkills(context.Background(), skills)
			if err != nil {
				t.Fatal(err)
			}
			operations := defaultAgentSkillsFileOperations()
			update, err := prepareAgentSkillsUpdate(context.Background(), session, agentSkillsProjection{current: current, names: names, previous: readAgentSkillsManifest(HostAgentSkillsDir(session))}, operations)
			if err != nil {
				t.Fatal(err)
			}
			if err := update.publish(context.Background(), operations); err != nil {
				t.Fatal(err)
			}
			published, _ := FingerprintAgentSkill(context.Background(), HostAgentSkillsDir(session))
			if published == old {
				t.Fatal("fixture did not publish a changed tree")
			}
			if committed {
				if err := update.commit(); err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverAgentSkillsUpdates(session, operations); err != nil {
				t.Fatal(err)
			}
			want := old
			if committed {
				want = published
			}
			if actual, _ := FingerprintAgentSkill(context.Background(), HostAgentSkillsDir(session)); actual != want {
				t.Fatal("interruption recovery selected an incomplete or incorrect tree")
			}
			assertNoSkillStages(t, session)
			projectSkills(t, session, skills)
			assertSkillProjectionMatches(t, session, skills)
		})
	}
}

func TestAgentSkillsCancellationWhileWaitingForLock(t *testing.T) {
	session, skills := skillProjectionFixture(t, 1)
	unlocked, err := lockAgentSkills(context.Background(), filepath.Dir(HostAgentSkillsDir(session)))
	if err != nil {
		t.Fatal(err)
	}
	defer unlocked()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WriteAgentSkills(ctx, nil, session, skills, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("lock cancellation returned %v", err)
	}
}

func TestAgentSkillsRejectsUnmanagedCanonicalEvenWhenIdentical(t *testing.T) {
	session, skills := skillProjectionFixture(t, 0)
	writeSkillProjectionFile(t, filepath.Join(HostAgentSkillsDir(session), "pdf", "SKILL.md"), "skill version one")
	if _, err := WriteAgentSkills(context.Background(), nil, session, skills, nil); err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("unmanaged directory was adopted: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(HostAgentSkillsDir(session), "pdf", "SKILL.md")); string(data) != "skill version one" {
		t.Fatal("unmanaged directory changed")
	}
}

func TestAgentSkillsDoesNotOverwriteUnreadableManagedFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the ordinary file permission failure being tested")
	}
	session, skills := skillProjectionFixture(t, 0)
	projectSkills(t, session, skills)
	target := filepath.Join(HostAgentSkillsDir(session), "pdf", "SKILL.md")
	before, err := agentSkillsPathIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o644) })
	writeSkillProjectionFile(t, filepath.Join(skills[0].LocalDir, "SKILL.md"), "source update")
	if _, err := WriteAgentSkills(context.Background(), nil, session, skills, nil); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unreadable target was overwritten or incorrectly classified: %v", err)
	}
	if after, err := agentSkillsPathIdentity(target); err != nil || before != after {
		t.Fatal("unreadable target inode was replaced")
	}
	assertNoSkillStages(t, session)
}

func TestAgentSkillsRejectsPrivateParentAndControlSymlinksBeforeWriting(t *testing.T) {
	for _, relative := range []string{"home", "home/.agents", "home/.claude", "home/.agents/skills", "home/.agents/.skills.lock", "home/.agents/skills/" + agentSkillsManifestFileName} {
		t.Run(relative, func(t *testing.T) {
			session, skills := skillProjectionFixture(t, 0)
			outside := t.TempDir()
			writeSkillProjectionFile(t, filepath.Join(outside, "sentinel"), "keep")
			target := outside
			if strings.HasSuffix(relative, ".lock") || strings.HasSuffix(relative, ".json") {
				target = filepath.Join(outside, "sentinel")
			}
			link := filepath.Join(HostSandboxDir(session), filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			before := skillProjectionIdentities(t, outside)
			if _, err := WriteAgentSkills(context.Background(), nil, session, skills, nil); err == nil {
				t.Fatal("private parent/control symlink was accepted")
			}
			after := skillProjectionIdentities(t, outside)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected skills path wrote into the external target")
			}
			if data, err := os.ReadFile(filepath.Join(outside, "sentinel")); err != nil || string(data) != "keep" {
				t.Fatal("external sentinel changed")
			}
			if len(before) != 2 {
				t.Fatal("external fixture inventory is incomplete")
			}
		})
	}
}
