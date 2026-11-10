package egress

// Record is the unified account of one egress decision: the request that was
// evaluated and the result it produced. Both decision paths emit this shape, so
// audit and diagnostics never have to reassemble a decision from
// call-site-specific fields.
type Record struct {
	Request Request
	Result  Result
	// Status is the status the enforcing boundary answered with, when it has
	// one. It is transport-neutral because the decision model is shared with
	// paths that have no HTTP status; the forward proxy sets it to the status
	// its decision produced so the audit line can carry it.
	Status int
}

// NewRecord pairs a request with the result of deciding it.
func NewRecord(request Request, result Result) Record {
	return Record{Request: request, Result: result}
}

// WithStatus returns the record stamped with the status the enforcing boundary
// answered with.
func (r Record) WithStatus(status int) Record {
	r.Status = status
	return r
}

// Allowed reports whether the recorded decision permits the request.
func (r Record) Allowed() bool {
	return r.Result.Allowed()
}

// IsStale reports whether current is a different policy snapshot than the one
// the recorded decision was evaluated against.
func (r Record) IsStale(current Policy) bool {
	return r.Result.IsStale(current)
}
