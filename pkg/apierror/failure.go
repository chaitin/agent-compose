package apierror

// Failure carries the external RPC failure contract from the layer that knows
// why an operation failed to the transport boundary. It is deliberately an
// error value rather than a field on a response: producers return it through
// the ordinary error path, and %w wrapping keeps the contract intact.
//
// The transport boundary owns the defaults. A producer that does not know the
// effect leaves Effect unspecified; a write RPC then reports UNKNOWN, which is
// the fail-closed answer. Only NotApplied may claim that nothing was written.
type Failure struct {
	Reason Reason
	// Effect is meaningful for write RPCs. EffectUnspecified means the
	// producer made no claim; the boundary reports UNKNOWN for a write and
	// UNSPECIFIED for a read.
	Effect Effect
	Err    error
}

// NewFailure records the failure reason without claiming an effect.
func NewFailure(reason Reason, cause error) *Failure {
	return &Failure{Reason: reason, Err: cause}
}

// NewNotAppliedFailure records a failure that provably changed none of the
// state the RPC's contract covers. Only code that failed before its first
// mutation may call it.
func NewNotAppliedFailure(reason Reason, cause error) *Failure {
	return &Failure{Reason: reason, Effect: EffectNotApplied, Err: cause}
}

// NewUnknownFailure records a failure whose side effects cannot be proven
// absent.
func NewUnknownFailure(reason Reason, cause error) *Failure {
	return &Failure{Reason: reason, Effect: EffectUnknown, Err: cause}
}

func (f *Failure) Error() string {
	if f == nil {
		return "<nil>"
	}
	if f.Err != nil {
		return f.Err.Error()
	}
	return "operation failed"
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Err
}
