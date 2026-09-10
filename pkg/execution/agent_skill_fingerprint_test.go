package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAgentSkillsProjectionEntriesGolden(t *testing.T) {
	root := t.TempDir()
	writeSkillProjectionFile(t, filepath.Join(root, "pdf", "SKILL.md"), "hello")
	writeSkillProjectionFile(t, filepath.Join(root, "pdf", ".agent-compose-skills.json"), "nested")
	writeSkillProjectionFile(t, filepath.Join(root, agentSkillsManifestFileName), "bookkeeping")
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "pdf", "SKILL.md"), 0o751); err != nil {
		t.Fatal(err)
	}
	entries, err := AgentSkillsProjectionEntries(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	const golden = `[{"path":"empty","kind":"directory","executable":73,"size":0,"sha256":""},{"path":"pdf","kind":"directory","executable":73,"size":0,"sha256":""},{"path":"pdf/.agent-compose-skills.json","kind":"file","executable":0,"size":6,"sha256":"233562de1a0288b139c4fa40b7d189f806e906eeb048517aeb67f34ac0e2faf1"},{"path":"pdf/SKILL.md","kind":"file","executable":73,"size":5,"sha256":"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}]`
	if string(encoded) != golden {
		t.Fatalf("guest fingerprint wire contract changed:\n%s\nwant:\n%s", encoded, golden)
	}
	if reflect.TypeFor[AgentSkillFileEntry]().NumField() != 5 {
		t.Fatal("every fingerprint field must be deliberately included in the guest golden contract")
	}
	if reflect.TypeFor[ResolvedAgentSkill]().NumField() != 3 || reflect.TypeFor[agentSkillsManifest]().NumField() != 3 {
		t.Fatal("review new projected or persisted skill fields and their compatibility")
	}
}

func TestAgentSkillFingerprintIncludesTreeAndExecutablePermissions(t *testing.T) {
	_, skills := skillProjectionFixture(t, 0)
	root := skills[0].LocalDir
	fingerprint := func() string {
		t.Helper()
		value, err := FingerprintAgentSkill(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	initial := fingerprint()
	file := filepath.Join(root, "SKILL.md")
	if err := os.Chtimes(file, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fingerprint(); got != initial {
		t.Fatal("mtime or non-executable permission change altered content identity")
	}
	for _, mutation := range []func() error{
		func() error { return os.Chmod(file, 0o700) },
		func() error { return os.Chmod(root, 0o700) },
		func() error { return os.Rename(file, filepath.Join(root, "renamed.md")) },
		func() error { return os.Remove(filepath.Join(root, "empty")) },
	} {
		before := fingerprint()
		if err := mutation(); err != nil {
			t.Fatal(err)
		}
		if fingerprint() == before {
			t.Fatal("tree path, directory or executable mutation did not change fingerprint")
		}
	}
}

func TestAgentSkillFingerprintRejectsAllLinksAndSpecialFiles(t *testing.T) {
	for _, kind := range []string{"internal", "absolute", "dangling", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			writeSkillProjectionFile(t, filepath.Join(root, "file"), "content")
			var err error
			switch kind {
			case "fifo":
				err = syscall.Mkfifo(filepath.Join(root, "unsupported"), 0o600)
			case "absolute":
				err = os.Symlink(filepath.Join(root, "file"), filepath.Join(root, "unsupported"))
			case "dangling":
				err = os.Symlink("missing", filepath.Join(root, "unsupported"))
			default:
				err = os.Symlink("file", filepath.Join(root, "unsupported"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := FingerprintAgentSkill(context.Background(), root); !errors.Is(err, errInvalidAgentSkillEntry) {
				t.Fatalf("unsupported source accepted or incorrectly classified: %v", err)
			}
		})
	}
}

func TestAgentSkillsRecoveryDoesNotClaimSimilarUserPaths(t *testing.T) {
	session, skills := skillProjectionFixture(t, 0)
	projectSkills(t, session, skills)
	parent := filepath.Dir(HostAgentSkillsDir(session))
	for _, name := range []string{"unmarked", "bad-marker", "symlink-marker", "directory-marker"} {
		root := filepath.Join(parent, agentSkillsUpdatePrefix+name)
		writeSkillProjectionFile(t, filepath.Join(root, "user.txt"), "keep")
		marker := filepath.Join(root, agentSkillsUpdateOwnerName)
		switch name {
		case "bad-marker":
			writeSkillProjectionFile(t, marker, "user")
		case "symlink-marker":
			correct := filepath.Join(t.TempDir(), "marker")
			writeSkillProjectionFile(t, correct, agentSkillsUpdateOwner)
			if err := os.Symlink(correct, marker); err != nil {
				t.Fatal(err)
			}
		case "directory-marker":
			if err := os.Mkdir(marker, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	owned := filepath.Join(parent, agentSkillsUpdatePrefix+"owned-incomplete")
	writeSkillProjectionFile(t, filepath.Join(owned, agentSkillsUpdateOwnerName), agentSkillsUpdateOwner)
	if err := recoverAgentSkillsUpdates(session, defaultAgentSkillsFileOperations()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), agentSkillsUpdatePrefix) {
			count++
			if data, err := os.ReadFile(filepath.Join(parent, entry.Name(), "user.txt")); err != nil || string(data) != "keep" {
				t.Fatal("unowned user content changed")
			}
		}
	}
	if count != 4 {
		t.Fatalf("recovery retained %d directories, want four user paths and no owned partial stage", count)
	}
}
