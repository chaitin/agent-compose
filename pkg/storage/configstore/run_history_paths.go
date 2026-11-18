package configstore

import (
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// Recorded artifact paths are derived from identities: a scheduler run owns
// exactly <root>/schedulers/<scheduler>/runs/<run>, and project runs live in
// their sandbox. Every history writer enforces that ownership here, so exact
// removal never scans other records' paths before deleting a run directory.
// A nil owner means the path may not be inside any scheduler run.
// Isolation directories (.history-removing-*) are private cleanup state and
// never valid runtime artifacts.
func validateHistoryArtifactPaths(owner *schedulerRunOwner, paths ...string) error {
	for _, path := range paths {
		if path == "" {
			continue
		}
		parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
		for i := 3; i < len(parts); i++ {
			if parts[i-3] != "schedulers" || !identity.IsID(parts[i-2]) || parts[i-1] != "runs" {
				continue
			}
			if strings.HasPrefix(parts[i], ".history-removing-") {
				return domain.ClassifyError(domain.ErrInvalidArgument, "run artifact path uses a reserved history cleanup directory", nil)
			}
			if !identity.IsID(parts[i]) {
				continue
			}
			if owner == nil || owner.SchedulerID != parts[i-2] || owner.RunID != parts[i] {
				return domain.ClassifyError(domain.ErrInvalidArgument, "run artifact path is inside another scheduler run's directory", nil)
			}
		}
	}
	return nil
}

type schedulerRunOwner struct {
	SchedulerID string
	RunID       string
}
