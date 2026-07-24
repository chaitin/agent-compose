package app

import (
	"context"
	"fmt"

	"agent-compose/pkg/projects"
)

func syncLegacyDefaultProject(ctx context.Context, controller *projects.Controller) error {
	if controller == nil {
		return fmt.Errorf("project controller is required")
	}
	result, err := controller.SyncLegacyDefaultProject(ctx)
	if err != nil {
		return err
	}
	if projects.HasValidationErrors(result.Issues) {
		return fmt.Errorf("legacy default project validation failed: %s: %s", result.Issues[0].Path, result.Issues[0].Message)
	}
	return nil
}
