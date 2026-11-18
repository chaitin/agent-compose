package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runinputs"
)

// UploadResolver resolves an UploadRunInput reference to its staged content.
// Resolving extends the upload's expiry so the copy into the workspace still
// finds it.
type UploadResolver interface {
	Resolve(projectID, uploadID string) (runinputs.Object, error)
}

// admitAgentInput normalizes and admits a text/file turn before the run record
// exists, so a rejected request never leaves a half-created run behind.
//
// Admission is provider-neutral: every file type is accepted and only the
// daemon-wide limits are applied. Materialization happens in the execution
// layer, which writes the files into the sandbox workspace.
func (c *Controller) admitAgentInput(ctx context.Context, req RunAgentRequest) ([]agentinput.Part, []string, error) {
	if len(req.Input) == 0 {
		return nil, nil, nil
	}
	if strings.TrimSpace(req.Command) != "" {
		return nil, nil, fmt.Errorf("%w: input cannot be combined with command", ErrInvalidRequest)
	}
	parts, warnings, err := agentinput.Normalize(req.Prompt, req.Input)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if err := c.resolveAgentInputUploads(ctx, req.ProjectID, parts); err != nil {
		return nil, nil, err
	}
	if err := agentinput.Validate(parts, c.agentInputLimits()); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return parts, warnings, nil
}

// resolveAgentInputUploads fills each upload-sourced file with its staged
// content in place. A missing reference is NOT_FOUND rather than an invalid
// request, and it is raised before any run state exists.
func (c *Controller) resolveAgentInputUploads(ctx context.Context, projectID string, parts []agentinput.Part) error {
	var project *domain.ProjectRecord
	for index := range parts {
		file := parts[index].File
		if file == nil || file.Source != agentinput.SourceUpload || strings.TrimSpace(file.UploadID) == "" {
			continue
		}
		if c.uploads == nil {
			return fmt.Errorf("%w: part %d: upload input is unavailable on this daemon", ErrInvalidRequest, index)
		}
		if project == nil {
			if c.configDB == nil {
				return fmt.Errorf("config store is required to resolve upload input")
			}
			record, err := c.configDB.GetProject(ctx, projectID)
			if err != nil {
				return err
			}
			project = &record
		}
		object, err := c.uploads.Resolve(project.ID, file.UploadID)
		if err != nil {
			if errors.Is(err, runinputs.ErrNotFound) {
				return apierror.NotApplied(domain.ResourceError(domain.ErrNotFound, "run input upload", file.UploadID,
					fmt.Sprintf("part %d: %v", index, err), err))
			}
			return fmt.Errorf("resolve upload for part %d: %w", index, err)
		}
		file.UploadID = object.UploadID
		file.UploadPath = object.Path
		file.UploadSize = object.SizeBytes
		file.SHA256 = object.SHA256
	}
	return nil
}

// agentInputLimits reads the daemon-wide limits once per admitted turn. The
// upload source is unavailable when no staging area is wired.
func (c *Controller) agentInputLimits() agentinput.Limits {
	limits := c.config.AgentInputLimits()
	if c.uploads == nil {
		limits.MaxUploadFileBytes = 0
		limits.MaxUploadTurnBytes = 0
	}
	return limits
}
