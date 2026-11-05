package egress

import "time"

// AuditRecord is the flattened, serializable account of one egress decision.
// It is derived from Record so the audit surface cannot describe a decision the
// proxy did not make; the identity fields come from the sandbox-init process
// when the mediation path supplied them.
type AuditRecord struct {
	Time        time.Time `json:"time"`
	SandboxID   string    `json:"sandbox_id"`
	Kind        string    `json:"kind,omitempty"`
	Destination string    `json:"destination,omitempty"`
	ResolvedIPs []string  `json:"resolved_ips,omitempty"`
	// Layer, Method, and Path report how much of the request the engine
	// observed and what it decided on. They are empty for a non-network
	// decision, and Method and Path are empty for an L4 tunnel.
	Layer  string `json:"layer,omitempty"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	// Status is the response status the enforcing boundary answered with.
	Status int `json:"status,omitempty"`
	// Enforcement is the matched rule's mode ("enforce" or "audit"). An audit
	// line whose action is allow and whose enforcement is audit reports a
	// request the declared constraint would have refused.
	Enforcement string `json:"enforcement,omitempty"`
	Action      string `json:"action"`
	RuleID      string `json:"rule_id,omitempty"`
	Target      string `json:"target,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Generation  uint64 `json:"generation,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Marker      string `json:"marker,omitempty"`
	ProcessID   int    `json:"process_id,omitempty"`
	Binary      string `json:"binary,omitempty"`
	// Audit reports that the decision matched an allowance declared in audit
	// mode. The action stays "allow"; Reason explains the observation.
	Audit bool `json:"audit,omitempty"`
}

// AuditRecordFrom flattens one decision for persistence. at is supplied by the
// boundary that owns the clock so the record and its timestamp stay testable.
func AuditRecordFrom(record Record, at time.Time) AuditRecord {
	return AuditRecord{
		Time:        at.UTC(),
		SandboxID:   record.Request.Consumer,
		Kind:        string(record.Request.Kind),
		Destination: record.Request.Name,
		ResolvedIPs: append([]string(nil), record.Request.ResolvedIPs...),
		Layer:       string(record.Request.Layer),
		Method:      record.Request.Method,
		Path:        record.Request.Path,
		Status:      record.Status,
		Enforcement: string(record.Result.Enforcement),
		Action:      string(record.Result.Action),
		RuleID:      record.Result.RuleID,
		Target:      record.Result.Target,
		Reason:      record.Result.Reason,
		Generation:  uint64(record.Result.Generation),
		SessionID:   record.Request.SessionID,
		Marker:      record.Request.Marker,
		ProcessID:   record.Request.ProcessID,
		Binary:      record.Request.Binary,
		Audit:       record.Result.Audit,
	}
}
