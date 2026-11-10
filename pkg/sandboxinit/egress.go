package sandboxinit

import (
	"fmt"
	"strings"
)

// EgressMode selects how the init process watches sandbox egress.
//
// The zero value is off: an init started without an explicit mode behaves as
// it did before egress interception existed. Audit is observational: connect
// calls are reported and then allowed to proceed. Enforce mediates every
// connect, through the daemon proxy directly or, when a relay socket is
// configured, through the independent sandbox supervisor.
type EgressMode string

const (
	// EgressModeOff disables egress interception.
	EgressModeOff EgressMode = "off"
	// EgressModeAudit reports every connect without changing its outcome.
	EgressModeAudit EgressMode = "audit"
	// EgressModeEnforce mediates every connect.
	EgressModeEnforce EgressMode = "enforce"
)

// EgressModeEnvVar carries the mode from the daemon into the sandbox. It is an
// environment variable rather than a new init flag so a newer daemon never
// makes an older guest init reject its entrypoint arguments.
const EgressModeEnvVar = "SANDBOX_INIT_EGRESS"

// EgressAuditLogEnvVar carries the guest path of the init-side audit JSONL sink.
// It is read by ac-init; an empty value keeps the existing behavior of logging
// each observed connection to stderr. The daemon injects it only when the
// deployment opted in, because Docker and Kubernetes deployments already
// capture the init's stderr.
const EgressAuditLogEnvVar = "SANDBOX_INIT_AUDIT_LOG"

// EgressGenerationEnvVar carries the egress policy generation the runtime was
// created under, so an audit line written before any proxy request can name the
// policy that governed the launch. It is advisory: init never decides policy.
const EgressGenerationEnvVar = "SANDBOX_INIT_EGRESS_GENERATION"

// EgressRelaySocketEnvVar names the guest path of the independent sandbox
// supervisor's Unix domain socket. It is set only for the supervisor isolation
// mode, where the workload container has no external network interface.
const EgressRelaySocketEnvVar = "SANDBOX_INIT_EGRESS_RELAY_SOCKET"

// EgressEngineHostsEnvVar carries daemon-resolved engine hostnames as a JSON
// object of host to address list. The workload has no external interface in
// supervisor mode, so init cannot resolve those names itself and must answer
// the DNS stub from this map instead.
const EgressEngineHostsEnvVar = "SANDBOX_INIT_EGRESS_ENGINE_HOSTS"

// ParseEgressMode validates the configured mode. Unknown values are rejected
// rather than silently falling back to off.
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

	// Binary names the executable that attempted the connection when the
	// platform can attribute it. It stays empty unless a caller fills it.
	Binary     string
	Family     string
	Host       string
	Port       int
	UnixPath   string
	Decision   string
	Reason     string
	Generation uint64
}

// EgressAuditSink receives audit events. It is injectable so tests can observe
// events without parsing logs; production leaves it nil and the supervisor logs.
type EgressAuditSink func(EgressAuditEvent)
