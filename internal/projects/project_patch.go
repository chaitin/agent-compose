package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/projectdef"
)

type PatchRequest struct {
	Project                 ProjectRef
	ExpectedCurrentSpecHash string
	Spec                    *projectdef.ProjectSpec
	Issues                  []ValidationIssue
	DryRun                  bool
}

func (c *Controller) PatchProject(ctx context.Context, req PatchRequest) (ApplyResult, error) {
	if HasValidationErrors(req.Issues) {
		return ApplyResult{Issues: req.Issues}, nil
	}
	if c.store == nil {
		return ApplyResult{}, fmt.Errorf("patch project: config store is required")
	}
	expectedHash := strings.TrimSpace(req.ExpectedCurrentSpecHash)
	if expectedHash == "" {
		return ApplyResult{}, fmt.Errorf("%w: expected current spec hash is required", ErrInvalidRequest)
	}
	project, revision, err := c.patchProjectRevision(ctx, req.Project, expectedHash)
	if err != nil {
		return ApplyResult{}, err
	}
	current, err := projectdef.ParseCanonicalJSON([]byte(revision.SpecJSON))
	if err != nil {
		return ApplyResult{}, fmt.Errorf("patch project %s: parse current revision: %w", project.Name, err)
	}
	if req.Spec == nil {
		return ApplyResult{Issues: []ValidationIssue{{Path: "spec", Message: "project spec is required"}}, RevisionSpec: current}, nil
	}
	if strings.TrimSpace(req.Spec.Name) != project.Name {
		return ApplyResult{Issues: []ValidationIssue{{Path: "spec.name", Message: "project name cannot be changed by PatchProject"}}, RevisionSpec: current}, nil
	}
	restored, restoreIssues, err := RestoreProjectSecrets(current, req.Spec)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("patch project %s: restore secrets: %w", project.Name, err)
	}
	if HasValidationErrors(restoreIssues) {
		return ApplyResult{Issues: restoreIssues, RevisionSpec: current}, nil
	}
	normalizedSpec, err := projectdef.Normalize(restored, projectdef.NormalizeOptions{
		ComposePath:       project.SourcePath,
		SourceCredentials: projectdef.SourceCredentialsResolved,
		ResolveScriptURLs: true,
		Context:           ctx,
	})
	if ctx.Err() != nil {
		return ApplyResult{}, ctx.Err()
	}
	if err != nil {
		var validationErr *projectdef.ValidationError
		if errors.As(err, &validationErr) {
			return ApplyResult{Issues: []ValidationIssue{{Path: validationErr.Path, Message: validationErr.Message}}, RevisionSpec: current}, nil
		}
		return ApplyResult{}, fmt.Errorf("patch project %s: normalize candidate: %w", project.Name, err)
	}
	specHash, err := normalizedSpec.Hash()
	if err != nil {
		return ApplyResult{}, fmt.Errorf("patch project %s: hash candidate: %w", project.Name, err)
	}
	// Source retrieval happens before taking the lifecycle token. Recheck the
	// expected revision afterward so concurrent updates cannot be overwritten.
	release, err := c.lifecycle.acquire(ctx, project.Name)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("patch project %s: wait for lifecycle operation: %w", project.Name, err)
	}
	defer release()
	if _, _, err := c.patchProjectRevision(ctx, ProjectRefByID(project.ID), expectedHash); err != nil {
		return ApplyResult{}, err
	}
	return c.applyProject(ctx, ApplyRequest{
		Normalized: NormalizedProject{Spec: normalizedSpec, SpecHash: specHash, SourcePath: project.SourcePath},
		Issues:     req.Issues,
		DryRun:     req.DryRun,
	}, true)
}

func (c *Controller) patchProjectRevision(ctx context.Context, ref ProjectRef, expectedHash string) (domain.ProjectRecord, domain.ProjectRevisionRecord, error) {
	project, err := c.resolveProjectRef(ctx, ref, false)
	if err != nil {
		return domain.ProjectRecord{}, domain.ProjectRevisionRecord{}, err
	}
	if project.CurrentRevision <= 0 {
		return project, domain.ProjectRevisionRecord{}, fmt.Errorf("%w: project %s has no current revision", ErrInvalidRequest, project.Name)
	}
	revision, err := c.store.GetProjectRevision(ctx, project.ID, project.CurrentRevision)
	if err != nil {
		return project, revision, fmt.Errorf("patch project %s: load current revision: %w", project.Name, err)
	}
	if expectedHash != revision.SpecHash {
		return project, revision, fmt.Errorf("%w: expected spec hash %s does not match current spec hash %s", ErrRevisionConflict, expectedHash, revision.SpecHash)
	}
	return project, revision, nil
}
