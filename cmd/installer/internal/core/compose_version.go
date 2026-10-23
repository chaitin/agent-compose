package core

import (
	"fmt"
	"strconv"
	"strings"
)

// minimumComposeVersion is the lowest Docker Compose release the KVM overlay
// can deploy. The `ports: !reset []` override used to drop the daemon's
// published port when it joins the sandbox-host network namespace landed in
// Compose v2.24.4; older plugins reject the tag and fail `up -d`.
const minimumComposeVersion = "2.24.4"

// parseComposeVersion parses a `docker compose version --short` value such as
// "v2.24.4" or "2.24.4-desktop.1" into its numeric major, minor, and patch
// parts. Missing patch components read as zero.
func parseComposeVersion(version string) ([3]int, error) {
	var parsed [3]int
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	// Some distributions suffix the numeric release (e.g. "2.24.4-desktop.1");
	// keep only the numeric core.
	if cut := strings.IndexAny(version, "+-"); cut >= 0 {
		version = version[:cut]
	}
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return parsed, fmt.Errorf("unrecognized Docker Compose version %q", version)
	}
	for i := 0; i < len(parsed); i++ {
		if i >= len(parts) {
			parsed[i] = 0
			continue
		}
		number, err := strconv.Atoi(parts[i])
		if err != nil {
			return parsed, fmt.Errorf("unrecognized Docker Compose version %q", version)
		}
		parsed[i] = number
	}
	return parsed, nil
}

// composeVersionAtLeast reports whether the reported Compose version is equal
// to or newer than the minimum, comparing major, minor, and patch in order.
func composeVersionAtLeast(version, minimum string) (bool, error) {
	parsed, err := parseComposeVersion(version)
	if err != nil {
		return false, err
	}
	parsedMinimum, err := parseComposeVersion(minimum)
	if err != nil {
		return false, err
	}
	for i := 0; i < len(parsed); i++ {
		if parsed[i] != parsedMinimum[i] {
			return parsed[i] > parsedMinimum[i], nil
		}
	}
	return true, nil
}

// checkComposeVersion rejects a Docker Compose plugin older than the KVM
// overlay's minimum before any deployment files are written.
func checkComposeVersion(version string) error {
	atLeast, err := composeVersionAtLeast(version, minimumComposeVersion)
	if err != nil {
		return fmt.Errorf("unrecognized Docker Compose version %q: %w", strings.TrimSpace(version), err)
	}
	if !atLeast {
		return fmt.Errorf(
			"docker compose %s or later is required (found %s); upgrade the Docker Compose plugin and retry",
			minimumComposeVersion, strings.TrimSpace(version),
		)
	}
	return nil
}

// composeFileUsesKVM reports whether the effective Compose file list includes
// the KVM overlay. The `ports: !reset []` override lives in that overlay, so
// its minimum Compose version is only enforced when the overlay is selected; a
// pure Docker deployment works on older plugins.
func composeFileUsesKVM(composeFiles string) bool {
	return strings.Contains(composeFiles, "docker-compose.kvm.yml")
}
