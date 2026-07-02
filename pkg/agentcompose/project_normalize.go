package agentcompose

import (
	projectdomain "agent-compose/internal/agentcompose/project"
	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type normalizedV2Project struct {
	spec       *compose.NormalizedProjectSpec
	specProto  *agentcomposev2.ProjectSpec
	specHash   string
	sourcePath string
}

func normalizeProjectServiceSpec(spec *agentcomposev2.ProjectSpec, source *agentcomposev2.ProjectSource, expectedHash string) (normalizedV2Project, []*agentcomposev2.ProjectValidationIssue, error) {
	normalized, issues, err := projectdomain.NormalizeProjectServiceSpec(spec, source, expectedHash)
	return normalizedV2Project{
		spec:       normalized.Spec,
		specProto:  normalized.SpecProto,
		specHash:   normalized.SpecHash,
		sourcePath: normalized.SourcePath,
	}, issues, err
}

func projectSpecYAMLShape(spec *agentcomposev2.ProjectSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	return projectdomain.ProjectSpecYAMLShape(spec)
}

func projectServiceSourcePath(source *agentcomposev2.ProjectSource) string {
	return projectdomain.ProjectServiceSourcePath(source)
}

func issueFromComposeError(err error) *agentcomposev2.ProjectValidationIssue {
	return projectdomain.IssueFromComposeError(err)
}

func projectValidationIssue(path, message string) *agentcomposev2.ProjectValidationIssue {
	return projectdomain.ProjectValidationIssue(path, message)
}

func specHashOrEmpty(normalized normalizedV2Project) string {
	return normalized.specHash
}
