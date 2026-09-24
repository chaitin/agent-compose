package api

import (
	"github.com/chaitin/agent-compose/pkg/compose"
	"github.com/chaitin/agent-compose/pkg/llms"
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
// explicitly secret environment value, every first-party LLM credential the
// daemon absorbs into its own connection, and every inherently secret source or
// OctoBus credential.
//
// An absorbed credential is redacted whether or not the declaration marked it
// secret. Its value never reaches the sandbox, so a view that echoed it would
// hand out a daemon-held credential through an API whose other responses are
// careful not to. The variable name stays visible: the operator needs to see
// what they declared, and the project check already reports it.
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
		if value == nil {
			continue
		}
		if value.GetSecret() || (value.GetValue() != "" && llms.AbsorbedCredentialEnvName(value.GetName())) {
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

// ProjectSpecToProtoChecked prevents an unresolved CLI-only script URL from
// being mistaken for inline scheduler source on the wire.
