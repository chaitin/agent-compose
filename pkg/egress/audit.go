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
	Action      string    `json:"action"`
	RuleID      string    `json:"rule_id,omitempty"`
	Target      string    `json:"target,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Generation  uint64    `json:"generation,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	Marker      string    `json:"marker,omitempty"`
	ProcessID   int       `json:"process_id,omitempty"`
	Binary      string    `json:"binary,omitempty"`
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
