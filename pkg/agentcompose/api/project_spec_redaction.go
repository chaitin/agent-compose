package api

import (
	"github.com/chaitin/agent-compose/pkg/compose"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
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
// explicitly secret environment value, every provider credential the daemon
// keeps off the guest, and every inherently secret source or OctoBus credential.
//
// A provider credential is redacted whether or not the declaration marked it
// secret. The daemon removes it from the guest environment, so the declaration
// is the only place the value still exists, and a view that echoed it would hand
// out a daemon-held credential through an API whose other responses are careful
// not to. The variable name stays visible: the operator needs to see what they
// declared, and the project check already reports it.
//
// The rule is the credential denylist itself rather than a second list, so what
// a view hides and what the guest actually receives cannot disagree. A
// credential-looking name the denylist does not know stays visible on purpose:
// that value really is passed through to the guest, so hiding it here would
// describe an exposed value as protected without changing the exposure.
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
		if value.GetSecret() || (value.GetValue() != "" && driverpkg.LLMProviderCredentialEnvName(value.GetName())) {
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
