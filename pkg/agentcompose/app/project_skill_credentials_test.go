package app

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	"github.com/chaitin/agent-compose/pkg/compose"
)

func TestNormalizeProjectRequestAcceptsCLIResolvedSkillCredentials(t *testing.T) {
	parsed, err := compose.Parse([]byte(`
name: private-skills
agents:
  worker:
    skills:
      - name: private
        provider: git
        url: https://git.example/skills.git
        username: ${SKILL_USER}
        password: ${SKILL_PASSWORD}
        token: ${SKILL_TOKEN}
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	cliSpec, err := compose.Normalize(parsed, compose.NormalizeOptions{Env: map[string]string{
		"SKILL_USER":     "skill-user",
		"SKILL_PASSWORD": "skill-password",
		"SKILL_TOKEN":    "skill-token",
	}})
	if err != nil {
		t.Fatalf("CLI Normalize returned error: %v", err)
	}
	submittedHash, err := cliSpec.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	wireSpec, err := api.ProjectSpecToProtoChecked(cliSpec)
	if err != nil {
		t.Fatalf("ProjectSpecToProtoChecked returned error: %v", err)
	}

	normalized, issues, err := normalizeProjectRequest(t.Context(), wireSpec, nil, submittedHash)
	if err != nil {
		t.Fatalf("normalizeProjectRequest returned error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("normalizeProjectRequest issues = %#v", issues)
	}
	if normalized.SpecHash != submittedHash {
		t.Fatalf("daemon spec hash = %q, want %q", normalized.SpecHash, submittedHash)
	}
	skill := normalized.Spec.Agents[0].Skills[0]
	if skill.Username != "skill-user" || skill.Password != "skill-password" || skill.Token != "skill-token" {
		t.Fatalf("daemon skill credentials = %#v", skill)
	}

	redacted := normalizedSpecToProto(normalized.Spec)
	redactedSkill := redacted.GetAgents()[0].GetSkills()[0]
	if redactedSkill.GetUsername() != "********" || redactedSkill.GetPassword() != "********" || redactedSkill.GetToken() != "********" {
		t.Fatalf("redacted skill credentials = %#v", redactedSkill)
	}
}

func TestNormalizeProjectRequestAcceptsLegacySkillCredentialReferences(t *testing.T) {
	wireSpec, err := api.ProjectSpecToProtoChecked(&compose.NormalizedProjectSpec{
		Name: "private-skills",
		Agents: []compose.NormalizedAgentSpec{{
			Name:    "worker",
			Enabled: true,
			Skills: []compose.NormalizedSkillSpec{{
				Name:     "private",
				Provider: "git",
				URL:      "https://git.example/skills.git",
				Token:    "${SKILL_TOKEN}",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("ProjectSpecToProtoChecked returned error: %v", err)
	}

	// A legacy persisted reference arriving at the daemon boundary must be
	// accepted so unrelated patches keep working; it is used literally at clone time.
	normalized, issues, err := normalizeProjectRequest(t.Context(), wireSpec, nil, "")
	if err != nil {
		t.Fatalf("normalizeProjectRequest returned error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("normalizeProjectRequest issues = %#v, want none for legacy reference", issues)
	}
	if got := normalized.Spec.Agents[0].Skills[0].Token; got != "${SKILL_TOKEN}" {
		t.Fatalf("daemon skill token = %q, want legacy reference preserved", got)
	}
}
