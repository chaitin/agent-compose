package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/projectdef"
)

func (c *Controller) ensureProjectVolumes(ctx context.Context, project domain.ProjectRecord, spec *projectdef.NormalizedProjectSpec) error {
	if c.volumes == nil {
		return fmt.Errorf("volume manager is required")
	}
	if spec == nil || len(spec.Volumes) == 0 {
		return c.volumes.ReplaceProjectVolumes(ctx, project.ID, nil)
	}
	existing, err := c.volumes.ListProjectVolumes(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("list project volumes: %w", err)
	}
	links := make(map[string]domain.ProjectVolumeLink, len(spec.Volumes))
	for key, volumeSpec := range spec.Volumes {
		name := strings.TrimSpace(volumeSpec.Name)
		if name == "" {
			name = strings.TrimSpace(existing[key].Name)
			if name == "" {
				name = fmt.Sprintf("%s_%s", spec.Name, key)
			}
		}
		var record domain.VolumeRecord
		var err error
		if volumeSpec.External {
			record, err = c.volumes.Inspect(ctx, name)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return fmt.Errorf("%w: external volume %s: %w", ErrVolumeNotFound, name, err)
				}
				return fmt.Errorf("%w: external volume %s: %w", ErrVolumeUnavailable, name, err)
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
				return fmt.Errorf("%w: ensure volume %s: %w", ErrVolumeUnavailable, name, err)
			}
		}
		links[key] = domain.ProjectVolumeLink{VolumeID: record.ID, External: volumeSpec.External}
	}
	return c.volumes.ReplaceProjectVolumes(ctx, project.ID, links)
}
