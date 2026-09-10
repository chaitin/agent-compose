package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestClaudeSkillsFallbackUpdatesOnlyWhenChanged(t *testing.T) {
	session, skills := skillProjectionFixture(t, 5)
	operations := defaultAgentSkillsFileOperations()
	operations.symlink = func(string, string) error { return errors.New("symlinks unavailable") }
	project := func() {
		t.Helper()
		if _, err := writeAgentSkills(context.Background(), session, skills, nil, operations); err != nil {
			t.Fatal(err)
		}
	}
	project()
	alias := filepath.Join(HostSandboxDir(session), "home", ".claude", "skills")
	assertMatches := func() {
		t.Helper()
		expected, err := AgentSkillsProjectionEntries(context.Background(), HostAgentSkillsDir(session))
		if err != nil {
			t.Fatal(err)
		}
		actual, err := AgentSkillsProjectionEntries(context.Background(), alias)
		if err != nil || !slices.Equal(expected, withoutClaudeSkillsMarker(actual)) {
			t.Fatalf("fallback differs from canonical: %v", err)
		}
	}
	assertMatches()
	before := skillProjectionIdentities(t, alias)
	copy := operations.copy
	operations.copy = func(context.Context, *os.Root, string) error { return errors.New("unchanged fallback recopied") }
	project()
	if !reflect.DeepEqual(before, skillProjectionIdentities(t, alias)) {
		t.Fatal("unchanged fallback inode replaced")
	}
	operations.copy = copy
	writeSkillProjectionFile(t, filepath.Join(alias, "pdf", "SKILL.md"), "guest changed fallback")
	project()
	assertMatches()
	writeSkillProjectionFile(t, filepath.Join(HostAgentSkillsDir(session), "manual.txt"), "preserve manual canonical content")
	writeSkillProjectionFile(t, filepath.Join(skills[0].LocalDir, "SKILL.md"), "source changed")
	project()
	assertMatches()
	if content, _ := os.ReadFile(filepath.Join(alias, "manual.txt")); string(content) != "preserve manual canonical content" {
		t.Fatal("fallback omitted existing unmanaged canonical content")
	}
	skills = nil
	project()
	if _, err := os.Lstat(alias); !os.IsNotExist(err) {
		t.Fatalf("managed fallback was not removed: %v", err)
	}
	assertNoSkillStages(t, session)
}

func TestClaudeSkillsMarkerRequiresRegularFileAndExactOwnership(t *testing.T) {
	for _, marker := range []string{"wrong-content", "directory", "symlink"} {
		t.Run(marker, func(t *testing.T) {
			session, skills := skillProjectionFixture(t, 0)
			alias := filepath.Join(HostSandboxDir(session), "home", ".claude", "skills")
			writeSkillProjectionFile(t, filepath.Join(alias, "user.txt"), "keep")
			path := filepath.Join(alias, claudeSkillsManagedMarkerFileName)
			switch marker {
			case "wrong-content":
				writeSkillProjectionFile(t, path, "user marker")
			case "directory":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				correct := filepath.Join(t.TempDir(), "marker")
				writeSkillProjectionFile(t, correct, "agent-compose\n")
				if err := os.Symlink(correct, path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := WriteAgentSkills(context.Background(), nil, session, skills, nil); err == nil {
				t.Fatal("unowned Claude directory was accepted")
			}
			if data, _ := os.ReadFile(filepath.Join(alias, "user.txt")); string(data) != "keep" {
				t.Fatal("unowned Claude directory was modified")
			}
			if _, err := os.Stat(filepath.Join(HostAgentSkillsDir(session), "pdf")); !os.IsNotExist(err) {
				t.Fatal("Claude conflict was detected after canonical publication")
			}
		})
	}
}
