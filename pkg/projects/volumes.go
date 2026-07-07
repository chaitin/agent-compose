package projects

import (
	"context"
	"fmt"
	"strings"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

func (c *Controller) ensureProjectVolumes(ctx context.Context, project domain.ProjectRecord, spec *compose.NormalizedProjectSpec) error {
	if spec == nil || len(spec.Volumes) == 0 {
		return nil
	}
	if c.volumes == nil {
		return fmt.Errorf("volume manager is required")
	}
	for key, volumeSpec := range spec.Volumes {
		name := strings.TrimSpace(volumeSpec.Name)
		if name == "" {
			name = fmt.Sprintf("%s_%s", spec.Name, key)
		}
		var record domain.VolumeRecord
		var err error
		if volumeSpec.External {
			record, err = c.volumes.Inspect(ctx, name)
			if err != nil {
				return fmt.Errorf("external volume %s: %w", name, err)
			}
		} else {
			record, _, err = c.volumes.Ensure(ctx, domain.VolumeRecord{
				Name:      name,
				Driver:    volumeSpec.Driver,
				Labels:    volumeSpec.Labels,
				Options:   volumeSpec.Options,
				ProjectID: project.ID,
			})
			if err != nil {
				return fmt.Errorf("ensure volume %s: %w", name, err)
			}
		}
		if err := c.volumes.UpsertProjectVolume(ctx, project.ID, key, record.ID, volumeSpec.External); err != nil {
			return err
		}
	}
	return nil
}
