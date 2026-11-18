package configstore

import (
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// Isolation directories are private cleanup state, never runtime artifacts.
// All project/scheduler history writers share this check, including completion.
func validateHistoryArtifactPaths(paths ...string) error {
	for _, path := range paths {
		parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
		for i := 3; i < len(parts); i++ {
			runID, pending := strings.CutPrefix(parts[i], ".history-removing-")
			if parts[i-3] == "schedulers" && identity.IsID(parts[i-2]) && parts[i-1] == "runs" && pending && identity.IsID(runID) {
				return domain.ClassifyError(domain.ErrInvalidArgument, "run artifact path uses a reserved history cleanup directory", nil)
			}
		}
	}
	return nil
}
