package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	"github.com/chaitin/agent-compose/pkg/compose"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func normalizeProjectRequest(ctx context.Context, spec *agentcomposev2.ProjectSpec, source *agentcomposev2.ProjectSource, submittedHash string) (projects.NormalizedProject, []projects.ValidationIssue, error) {
	parsed, issues, err := parseProjectRequest(spec)
	if err != nil || len(issues) > 0 {
		return projects.NormalizedProject{}, issues, err
	}
	sourcePath := api.ProjectServiceSourcePath(source)
	projectDir := ""
	if source != nil {
		projectDir = strings.TrimSpace(source.GetProjectDir())
	}
	normalized, err := compose.Normalize(parsed, compose.NormalizeOptions{
		ComposePath:          sourcePath,
		ProjectDir:           projectDir,
		LiteralValues:        true,
		SourceCredentials:    compose.SourceCredentialsResolved,
		ScriptSourceBoundary: compose.ScriptSourceBoundaryDaemon,
		ResolveScriptURLs:    true,
		Context:              ctx,
	})
	if ctx.Err() != nil {
		return projects.NormalizedProject{}, nil, ctx.Err()
	}
	if err != nil {
		return projects.NormalizedProject{}, []projects.ValidationIssue{validationIssueFromProto(api.IssueFromComposeError(err))}, nil
	}
	hash, err := normalized.Hash()
	if err != nil {
		return projects.NormalizedProject{}, nil, fmt.Errorf("hash project spec: %w", err)
	}
	result := projects.NormalizedProject{
		Spec:       normalized,
		SpecHash:   hash,
		SourcePath: sourcePath,
	}
	submittedHash = strings.TrimSpace(submittedHash)
	if submittedHash != "" && submittedHash != hash {
		return result, []projects.ValidationIssue{{Path: "submitted_spec_hash", Message: fmt.Sprintf("submitted spec hash %s does not match normalized spec hash %s", submittedHash, hash)}}, nil
	}
	return result, nil, nil
}
