package egress

// Record is the unified account of one egress decision: the request that was
// evaluated and the result it produced. Both decision paths emit this shape, so
// audit and diagnostics never have to reassemble a decision from
// call-site-specific fields.
type Record struct {
	Request Request
	Result  Result
}

// NewRecord pairs a request with the result of deciding it.
func NewRecord(request Request, result Result) Record {
	return Record{Request: request, Result: result}
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
