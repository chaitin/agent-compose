package core

import _ "embed"

// sandboxNetworkOverlay is the byte-for-byte content of the repo-root
// docker-compose.sandbox-network.yml.
//
// It is embedded in the installer rather than shipped as a bundle member so
// that an installer released before the overlay existed can still extract a
// newer bundle during an in-place upgrade (its strict member whitelist would
// otherwise reject the whole archive). The installer writes this content during
// install and upgrade, so new deployments get the overlay without the bundle
// carrying an unexpected member.
//
// Keep it in sync with docker-compose.sandbox-network.yml; divergence fails
// TestSandboxNetworkOverlayMatchesRepoFile.
//
//go:embed sandbox_network_overlay.yml
var sandboxNetworkOverlay []byte
