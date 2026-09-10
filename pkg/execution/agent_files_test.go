package execution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestWriteAgentSystemPromptFileClearsGuestOnlyWhenPreviouslyPushed(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{}
	var pushCount int
	writer := func(context.Context, string, []byte) error {
		pushCount++
		return nil
	}

	// Never had a system prompt written: clearing must not push at all.
	fresh := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "fresh")}}
	if err := WriteAgentSystemPromptFile(context.Background(), config, fresh, "", writer); err != nil {
		t.Fatalf("WriteAgentSystemPromptFile (fresh) returned error: %v", err)
	}
	if pushCount != 0 {
		t.Fatalf("push count for a sandbox that never had a system prompt = %d, want 0", pushCount)
	}

	// Reused sandbox transitioning from having a prompt to having none: a
	// no-shared-mount guest (k8s) only learns of the removal via this push,
	// so the guest's stale copy must be cleared, not left behind for the
	// same Pod's next run to pick back up.
	reused := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "reused")}}
	if err := WriteAgentSystemPromptFile(context.Background(), config, reused, "you are an agent", writer); err != nil {
		t.Fatalf("WriteAgentSystemPromptFile (populate) returned error: %v", err)
	}
	pushCount = 0
	if err := WriteAgentSystemPromptFile(context.Background(), config, reused, "", writer); err != nil {
		t.Fatalf("WriteAgentSystemPromptFile (clear) returned error: %v", err)
	}
	if pushCount != 1 {
		t.Fatalf("push count clearing a previously-populated system prompt = %d, want 1", pushCount)
	}
}

func TestWriteAgentSystemPromptFileRetriesGuestClearAfterTransientPushFailure(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "reused")}}
	if err := WriteAgentSystemPromptFile(context.Background(), config, session, "you are an agent", func(context.Context, string, []byte) error { return nil }); err != nil {
		t.Fatalf("WriteAgentSystemPromptFile (populate) returned error: %v", err)
	}

	failing := func(context.Context, string, []byte) error { return fmt.Errorf("transient exec failure") }
	if err := WriteAgentSystemPromptFile(context.Background(), config, session, "", failing); err == nil {
		t.Fatal("WriteAgentSystemPromptFile (clear, guest push fails) returned nil error, want the push failure")
	}
	if _, err := os.Stat(HostAgentSystemPromptPath(session)); err != nil {
		t.Fatalf("host system prompt file after a failed guest push, stat error = %v, want the file to still exist so a retry sees hadExisting=true", err)
	}

	var pushCount int
	retry := func(context.Context, string, []byte) error {
		pushCount++
		return nil
	}
	if err := WriteAgentSystemPromptFile(context.Background(), config, session, "", retry); err != nil {
		t.Fatalf("WriteAgentSystemPromptFile (clear, retry) returned error: %v", err)
	}
	if pushCount != 1 {
		t.Fatalf("push count on retry after a prior transient failure = %d, want 1", pushCount)
	}
}

func TestWriteAgentSkillsReconcilesManagedProjection(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	skillSource := filepath.Join(root, "source", "pdf")
	if err := os.MkdirAll(skillSource, 0o755); err != nil {
		t.Fatalf("create skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillSource, "SKILL.md"), []byte("---\nname: pdf\ndescription: PDF\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	skillsDir := HostAgentSkillsDir(session)
	if err := os.MkdirAll(filepath.Join(skillsDir, "stale"), 0o755); err != nil {
		t.Fatalf("create stale skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "manual.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write manual file: %v", err)
	}
	if err := writeAgentSkillsManifest(skillsDir, agentSkillsManifest{Names: []string{"stale"}}); err != nil {
		t.Fatalf("write old manifest: %v", err)
	}

	names, err := WriteAgentSkills(context.Background(), &appconfig.Config{}, session, []ResolvedAgentSkill{{Name: "pdf", LocalDir: skillSource}}, nil)
	if err != nil {
		t.Fatalf("WriteAgentSkills returned error: %v", err)
	}
	if len(names) != 1 || names[0] != "pdf" {
		t.Fatalf("names = %#v, want pdf", names)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "pdf", "SKILL.md")); err != nil {
		t.Fatalf("projected skill missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale skill still exists or unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "manual.txt")); err != nil {
		t.Fatalf("manual file should remain: %v", err)
	}
	link := filepath.Join(HostSandboxDir(session), "home", ".claude", "skills")
	if target, err := os.Readlink(link); err != nil || target != "../.agents/skills" {
		t.Fatalf("claude skills link target=%q err=%v", target, err)
	}
}

func TestWriteAgentSkillsIgnoresInvalidManifestNames(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	skillsDir := HostAgentSkillsDir(session)
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("create skills dir: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	if err := writeAgentSkillsManifest(skillsDir, agentSkillsManifest{Names: []string{filepath.Join("..", "..", "..", "outside")}}); err != nil {
		t.Fatalf("write old manifest: %v", err)
	}

	if _, err := WriteAgentSkills(context.Background(), &appconfig.Config{}, session, nil, nil); err != nil {
		t.Fatalf("WriteAgentSkills returned error: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside dir should remain: %v", err)
	}
}

func TestWriteAgentSkillsDoesNotRemoveUserClaudeSkillsWithoutConfiguredSkills(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	userSkill := filepath.Join(HostSandboxDir(session), "home", ".claude", "skills", "user", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(userSkill), 0o755); err != nil {
		t.Fatalf("create user claude skill: %v", err)
	}
	if err := os.WriteFile(userSkill, []byte("user skill"), 0o644); err != nil {
		t.Fatalf("write user claude skill: %v", err)
	}

	names, err := WriteAgentSkills(context.Background(), &appconfig.Config{}, session, nil, nil)
	if err != nil {
		t.Fatalf("WriteAgentSkills returned error: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("names = %#v, want none", names)
	}
	if got, err := os.ReadFile(userSkill); err != nil || string(got) != "user skill" {
		t.Fatalf("user claude skill was modified: %q err=%v", got, err)
	}
}

func TestWriteAgentSkillsRejectsUserClaudeSkillsDirectory(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	skillSource := filepath.Join(root, "source", "pdf")
	if err := os.MkdirAll(skillSource, 0o755); err != nil {
		t.Fatalf("create skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillSource, "SKILL.md"), []byte("---\nname: pdf\ndescription: PDF\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	userSkill := filepath.Join(HostSandboxDir(session), "home", ".claude", "skills", "user", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(userSkill), 0o755); err != nil {
		t.Fatalf("create user claude skill: %v", err)
	}
	if err := os.WriteFile(userSkill, []byte("user skill"), 0o644); err != nil {
		t.Fatalf("write user claude skill: %v", err)
	}

	_, err := WriteAgentSkills(context.Background(), &appconfig.Config{}, session, []ResolvedAgentSkill{{Name: "pdf", LocalDir: skillSource}}, nil)
	if err == nil {
		t.Fatalf("expected WriteAgentSkills to reject user claude skills directory")
	}
	if got, readErr := os.ReadFile(userSkill); readErr != nil || string(got) != "user skill" {
		t.Fatalf("user claude skill was modified: %q err=%v", got, readErr)
	}
}

// No-shared-mount path (k8s - see docs/design/k8s_pod_runtime_driver_design.md
// §2.1): the writer reconciles one canonical directory and its provider aliases.
func TestWriteAgentSkillsPushesToGuestWhenPresent(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	skillSource := filepath.Join(root, "source", "pdf")
	if err := os.MkdirAll(skillSource, 0o755); err != nil {
		t.Fatalf("create skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillSource, "SKILL.md"), []byte("---\nname: pdf\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	config := &appconfig.Config{}

	var pushes []string
	writer := func(_ context.Context, hostSrcDir string) error {
		pushes = append(pushes, hostSrcDir)
		return nil
	}

	names, err := WriteAgentSkills(context.Background(), config, session, []ResolvedAgentSkill{{Name: "pdf", LocalDir: skillSource}}, writer)
	if err != nil {
		t.Fatalf("WriteAgentSkills returned error: %v", err)
	}
	if len(names) != 1 || names[0] != "pdf" {
		t.Fatalf("names = %#v, want pdf", names)
	}
	skillsDir := HostAgentSkillsDir(session)
	wantPushes := []string{skillsDir}
	if len(pushes) != len(wantPushes) || pushes[0] != wantPushes[0] {
		t.Fatalf("pushes = %#v, want %#v", pushes, wantPushes)
	}

	// Reused sandbox transitioning from having a skill to having none: the
	// guest copy from the first call above must still be cleared, not left
	// stale, so this still pushes (now-empty skillsDir, holding just the
	// manifest) even though names is empty this time.
	pushes = nil
	if _, err := WriteAgentSkills(context.Background(), config, session, nil, writer); err != nil {
		t.Fatalf("WriteAgentSkills (clearing) returned error: %v", err)
	}
	if len(pushes) != len(wantPushes) || pushes[0] != wantPushes[0] {
		t.Fatalf("clearing pushes = %#v, want %#v", pushes, wantPushes)
	}
}

func TestWriteAgentSkillsSkipsPushWhenNeverConfigured(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	config := &appconfig.Config{}

	var pushCount int
	writer := func(context.Context, string) error {
		pushCount++
		return nil
	}

	// A sandbox that never had any skills configured must not pay for an
	// exec round trip on every call just to push an empty directory nothing
	// ever populated.
	if _, err := WriteAgentSkills(context.Background(), config, session, nil, writer); err != nil {
		t.Fatalf("WriteAgentSkills returned error: %v", err)
	}
	if pushCount != 0 {
		t.Fatalf("push count = %d, want 0 (nothing was ever configured to clear)", pushCount)
	}
}

func TestWriteAgentSkillsRetriesGuestClearAfterTransientPushFailure(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	skillSource := filepath.Join(root, "source", "pdf")
	if err := os.MkdirAll(skillSource, 0o755); err != nil {
		t.Fatalf("create skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillSource, "SKILL.md"), []byte("---\nname: pdf\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	config := &appconfig.Config{}

	if _, err := WriteAgentSkills(context.Background(), config, session, []ResolvedAgentSkill{{Name: "pdf", LocalDir: skillSource}}, func(context.Context, string) error { return nil }); err != nil {
		t.Fatalf("WriteAgentSkills (populate) returned error: %v", err)
	}

	failing := func(context.Context, string) error { return fmt.Errorf("transient exec failure") }
	if _, err := WriteAgentSkills(context.Background(), config, session, nil, failing); err == nil {
		t.Fatal("WriteAgentSkills (clear, guest push fails) returned nil error, want the push failure")
	}

	var pushCount int
	retry := func(context.Context, string) error {
		pushCount++
		return nil
	}
	if _, err := WriteAgentSkills(context.Background(), config, session, nil, retry); err != nil {
		t.Fatalf("WriteAgentSkills (clear, retry) returned error: %v", err)
	}
	if pushCount != 1 {
		t.Fatalf("push count on retry after a prior transient failure = %d, want 1 canonical reconciliation", pushCount)
	}
}
