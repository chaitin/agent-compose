package capability

import (
	"fmt"
	"path/filepath"
	"strings"
)

// GuidePreamble describes how the guest reaches the capability proxy: the gRPC
// endpoint, per-session auth metadata, and per-method OctoBus routing metadata.
// Returns "" when no proxy target is configured.
func GuidePreamble(target, sessionTokenMetadata string) string {
	target = strings.TrimSpace(target)
	sessionTokenMetadata = strings.TrimSpace(sessionTokenMetadata)
	if target == "" || sessionTokenMetadata == "" {
		return ""
	}
	return fmt.Sprintf(`# Capability Gateway Access

Capabilities are reachable over gRPC through the local capability proxy. To call
any method in the catalog below:

- Endpoint: %s (plaintext HTTP/2 gRPC; also in env CAP_GRPC_TARGET)
- On every call, send metadata `+"`%s: $CAP_TOKEN`"+` (token value is in env CAP_TOKEN)
- Also send the per-method `+"`x-octobus-capset` / `x-octobus-instance`"+`
  metadata shown in the table below
- Schemas can be discovered via gRPC server reflection using the same
  `+"`x-octobus-capset`"+` metadata

`, target, sessionTokenMetadata)
}

// RuntimeDir is the local session runtime directory, sibling to the workspace
// directory under the session root. Returns "" when the workspace path is
// unknown.
func RuntimeDir(workspacePath string) string {
	workspace := strings.TrimSpace(workspacePath)
	if workspace == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(workspace), "runtime")
}

// GuidePath is the session MPI catalog file the capability guide is written to
// (guest /data/runtime/mpi/catalog.md). Returns "" when the session runtime dir
// is unknown.
func GuidePath(workspacePath string) string {
	dir := RuntimeDir(workspacePath)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "mpi", "catalog.md")
}
