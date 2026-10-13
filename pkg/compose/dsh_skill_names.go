package compose

import (
	"fmt"
	"regexp"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// dshSkillNamePattern is the skill-name rule DSH enforces in its own skill
// registry. It is narrower than stableIdentifierPattern: no underscores, and
// hyphens only singly between alphanumeric runs. It mirrors SKILL_NAME in
// @deepseek-ai/dsh-skill at the DSH_VERSION pinned by
// guest-images/Dockerfile.agent-compose-guest (0.2.0-rc.1); recheck it when
// that version is bumped.
var dshSkillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// validateDshSkillNames rejects skill names DSH would drop. DSH discards a
// skill whose name fails its rule with nothing but an internal warning, so the
// run succeeds while the agent never sees the skill; failing here turns that
// into an error at apply time. Other providers accept the wider identifier.
// A name inferred from the source path can fail too, so the message asks for
// an explicit one rather than pointing only at a field the author never wrote.
func validateDshSkillNames(path, provider string, skills []NormalizedSkillSpec) error {
	if domain.NormalizeAgentKind(provider) != "dsh" {
		return nil
	}
	for index, skill := range skills {
		if dshSkillNamePattern.MatchString(skill.Name) {
			continue
		}
		return &ValidationError{
			Path:    fmt.Sprintf("%s[%d].name", path, index),
			Message: fmt.Sprintf("skill name %q is not loadable by the dsh provider; it must match %s (lowercase letters and digits joined by single hyphens); declare such a name explicitly, and keep it equal to the name in SKILL.md", skill.Name, dshSkillNamePattern.String()),
		}
	}
	return nil
}
