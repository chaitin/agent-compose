package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EgressAuditLogName is the environment variable that enables the persistent
// egress decision log.
const EgressAuditLogName = "EGRESS_AUDIT_LOG"

// loadEgressAuditLogPath returns the configured audit log path. It is empty
// when audit logging is disabled, which keeps the proxy's recorder nil and the
// existing behavior unchanged.
func loadEgressAuditLogPath() (string, error) {
	raw := strings.TrimSpace(os.Getenv(EgressAuditLogName))
	if raw == "" {
		return "", nil
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("%s must be an absolute path, got %q", EgressAuditLogName, raw)
	}
	return filepath.Clean(raw), nil
}
