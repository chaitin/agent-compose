package compose

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func normalizeSkillNameFixture(t *testing.T, provider, skillName string) error {
	t.Helper()
	spec := mustParseCompose(t, fmt.Sprintf(`
name: skills-project
agents:
  reviewer:
    provider: %s
    skills:
      - name: %s
        provider: file
        path: ./skills/%s
`, provider, skillName, skillName))
	_, err := Normalize(spec, NormalizeOptions{ComposePath: filepath.Join(t.TempDir(), "agent-compose.yml")})
	return err
}

// DSH drops a skill whose name fails its own rule without failing the run, so
// the name must be rejected before the project is applied.
func TestNormalizeRejectsSkillNamesDshCannotLoad(t *testing.T) {
	for _, name := range []string{"needs_underscore", "double--hyphen", "trailing-"} {
		err := normalizeSkillNameFixture(t, "dsh", name)
		if err == nil {
			t.Fatalf("skill %q on dsh: expected Normalize to fail", name)
		}
		if got := err.Error(); !strings.Contains(got, "agents.reviewer.skills[0].name") || !strings.Contains(got, "dsh provider") {
			t.Fatalf("skill %q on dsh: error = %q, want the skill name path and the dsh rule", name, got)
		}
	}
	// A provider alias reaches the same runner, so it gets the same rule.
	if err := normalizeSkillNameFixture(t, "deepseek", "needs_underscore"); err == nil {
		t.Fatal("skill needs_underscore on the deepseek alias: expected Normalize to fail")
	}
}

// An omitted name is inferred from the source path and keeps underscores, so
// the error has to tell the author to declare one.
func TestNormalizeRejectsInferredSkillNamesDshCannotLoad(t *testing.T) {
	spec := mustParseCompose(t, `
name: skills-project
agents:
  reviewer:
    provider: dsh
    skills:
      - provider: file
        path: ./skills/my_skill
`)
	_, err := Normalize(spec, NormalizeOptions{ComposePath: filepath.Join(t.TempDir(), "agent-compose.yml")})
	if err == nil {
		t.Fatal("inferred skill name my_skill on dsh: expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, `"my_skill"`) || !strings.Contains(got, "declare such a name explicitly") {
		t.Fatalf("error = %q, want the inferred name and the request to declare one", got)
	}
}

func TestNormalizeKeepsWiderSkillNamesForOtherProviders(t *testing.T) {
	for _, name := range []string{"review", "code-review", "v2"} {
		if err := normalizeSkillNameFixture(t, "dsh", name); err != nil {
			t.Fatalf("skill %q on dsh: %v", name, err)
		}
	}
	for _, provider := range []string{"codex", "claude", "opencode", "pi"} {
		if err := normalizeSkillNameFixture(t, provider, "needs_underscore"); err != nil {
			t.Fatalf("skill needs_underscore on %s: %v", provider, err)
		}
	}
}
