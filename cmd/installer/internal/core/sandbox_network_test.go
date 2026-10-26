package core

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSandboxNetworkOverlayMatchesRepoFile keeps the embedded overlay copy in
// sync with the repo-root docker-compose.sandbox-network.yml. Both are used
// independently — the repo file for a manual `docker compose` deployment and
// the embedded copy for the installer — so a silent divergence would change
// behavior between the two install paths.
func TestSandboxNetworkOverlayMatchesRepoFile(t *testing.T) {
	repoPath := filepath.Join("..", "..", "..", "..", "docker-compose.sandbox-network.yml")
	data, err := os.ReadFile(repoPath)
	if err != nil {
		t.Fatalf("read repo overlay %s: %v", repoPath, err)
	}
	if string(data) != string(sandboxNetworkOverlay) {
		t.Fatalf("embedded sandbox-network overlay diverges from %s; update cmd/installer/internal/core/sandbox_network_overlay.yml", repoPath)
	}
}
