package api

import (
	"github.com/chaitin/agent-compose/pkg/compose"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"

	"google.golang.org/protobuf/proto"
)

// ProjectSpecToProtoRedacted converts a project spec for user-facing API
// responses without exposing secret values.
func ProjectSpecToProtoRedacted(spec *compose.NormalizedProjectSpec) *agentcomposev2.ProjectSpec {
	if spec == nil {
		return nil
	}
	return RedactProjectSpecSecrets(ProjectSpecToProto(spec))
}

// RedactProjectSpecSecrets returns a user-facing copy of a project spec. It
// leaves the persisted/runtime representation untouched while hiding every
// explicitly secret environment value and every inherently secret source or
// OctoBus credential.
func RedactProjectSpecSecrets(spec *agentcomposev2.ProjectSpec) *agentcomposev2.ProjectSpec {
	if spec == nil {
		return nil
	}
	redacted := proto.Clone(spec).(*agentcomposev2.ProjectSpec)
	redactEnvVarSpecs(redacted.Variables)
	redactMCPServerSpecs(redacted.McpServers)
	redactOctoBusServerSpecs(redacted.OctobusServers)
	redactNamedWorkspaceSpecs(redacted.Workspaces)
	for _, agent := range redacted.Agents {
		if agent == nil {
			continue
		}
		redactEnvVarSpecs(agent.Env)
		redactMCPServerSpecs(agent.McpServers)
		redactWorkspaceSpec(agent.Workspace)
		redactSkillSpecs(agent.Skills)
		redactSchedulerScriptSource(agent.GetScheduler().GetScriptSource())
	}
	return redacted
}

func redactSkillSpecs(values []*agentcomposev2.SkillSpec) {
	for _, value := range values {
		if value == nil {
			continue
		}
		if value.GetUsername() != "" {
			value.Username = secretRedactedValue
		}
		if value.GetPassword() != "" {
			value.Password = secretRedactedValue
		}
		if value.GetToken() != "" {
			value.Token = secretRedactedValue
		}
	}
}

func redactNamedWorkspaceSpecs(values []*agentcomposev2.NamedWorkspaceSpec) {
	for _, value := range values {
		if value != nil {
			redactWorkspaceSpec(value.Workspace)
		}
	}
}

func redactWorkspaceSpec(value *agentcomposev2.WorkspaceSpec) {
	if value == nil {
		return
	}
	if value.GetUsername() != "" {
		value.Username = secretRedactedValue
	}
	if value.GetPassword() != "" {
		value.Password = secretRedactedValue
	}
	if value.GetToken() != "" {
		value.Token = secretRedactedValue
	}
}

func redactEnvVarSpecs(values []*agentcomposev2.EnvVarSpec) {
	for _, value := range values {
		if value != nil && value.GetSecret() {
			value.Value = secretRedactedValue
		}
	}
}

func redactMCPServerSpecs(values []*agentcomposev2.MCPServerSpec) {
	for _, value := range values {
		if value == nil {
			continue
		}
		redactEnvVarSpecs(value.Env)
		redactEnvVarSpecs(value.Headers)
	}
}
