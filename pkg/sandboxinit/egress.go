package sandboxinit

import (
	"fmt"
	"strings"
)

// EgressMode selects how the init process watches sandbox egress.
//
// The zero value is off: an init started without an explicit mode behaves as
// it did before egress interception existed. Audit is observational: connect
// calls are reported and then allowed to proceed. Enforce is reserved for the
// mediation step and is rejected until it is implemented, so a deployment that
// asks for enforcement cannot silently receive observation instead.
type EgressMode string

const (
	// EgressModeOff disables egress interception.
	EgressModeOff EgressMode = "off"
	// EgressModeAudit reports every connect without changing its outcome.
	EgressModeAudit EgressMode = "audit"
	// EgressModeEnforce mediates every connect. Reserved; not implemented yet.
	EgressModeEnforce EgressMode = "enforce"
)

// EgressModeEnvVar carries the mode from the daemon into the sandbox. It is an
// environment variable rather than a new init flag so a newer daemon never
// makes an older guest init reject its entrypoint arguments.
const EgressModeEnvVar = "SANDBOX_INIT_EGRESS"

// ParseEgressMode validates the configured mode. Enforce is recognized so the
// error names the missing implementation instead of reporting an unknown value.
func ParseEgressMode(raw string) (EgressMode, error) {
	switch EgressMode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", EgressModeOff:
		return EgressModeOff, nil
	case EgressModeAudit:
		return EgressModeAudit, nil
	case EgressModeEnforce:
		return EgressModeEnforce, nil
	default:
		return "", fmt.Errorf("%s must be off or audit, got %q", EgressModeEnvVar, raw)
	}
}

// EgressAuditEvent is one observed outbound connection. The daemon-facing shape
// stays driver-neutral: init reports what the workload attempted, and the
// policy decision remains the daemon's. Decision is empty in audit mode and
// "allow" or "deny" in mediated mode.
type EgressAuditEvent struct {
	SandboxID string
	SessionID string
	Marker    string
	PID       int
	Family    string
	Host      string
	Port      int
	UnixPath  string
	Decision  string
	Reason    string
}

// EgressAuditSink receives audit events. It is injectable so tests can observe
// events without parsing logs; production leaves it nil and the supervisor logs.
type EgressAuditSink func(EgressAuditEvent)
