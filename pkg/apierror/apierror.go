// Package apierror carries the server-side pieces of the external RPC failure
// contract: the not-applied marker and the transport-neutral failure
// vocabulary shared by the domain controllers and the API handlers.
//
// The vocabulary is deliberately defined here rather than aliased to the
// protobuf enums: domain packages import this package, and AGENTS.md forbids
// domain code from depending on transport representations. The numeric values
// match OperationErrorReason and OperationEffect in
// proto/agentcompose/v2/errors.proto; the API boundary converts by type cast,
// and contract_test.go pins each constant against its protobuf counterpart so
// the two cannot drift.
//
// A NOT_APPLIED claim requires either the marker or an explicit Failure
// effect. Anything else is reported as UNKNOWN, which is the safe direction: a
// forgotten annotation can never tell a caller that nothing was written when
// something may have been.
package apierror

// Reason is the transport-neutral copy of OperationErrorReason.
type Reason int32

// Effect is the transport-neutral copy of OperationEffect.
type Effect int32

const (
	ReasonUnspecified Reason = 0

	ReasonProjectSpecInvalid       Reason = 1
	ReasonAgentDefinitionInvalid   Reason = 2
	ReasonSchedulerInvalid         Reason = 3
	ReasonSchedulerScriptInvalid   Reason = 4
	ReasonSchedulerReconcileFailed Reason = 5

	ReasonNetworkPolicyRejected Reason = 6
	ReasonProjectNotFound       Reason = 7
	ReasonRevisionConflict      Reason = 8
	ReasonInvalidRequest        Reason = 9
	ReasonUnsupported           Reason = 10
	ReasonOperationBusy         Reason = 11
	ReasonPreconditionFailed    Reason = 12

	ReasonSchedulerNotFound    Reason = 13
	ReasonSchedulerRunNotFound Reason = 14
	ReasonRunNotFound          Reason = 15
	ReasonSandboxNotFound      Reason = 16

	ReasonImageNotFound           Reason = 17
	ReasonImagePullFailed         Reason = 18
	ReasonImageReferenceInvalid   Reason = 19
	ReasonImageBackendUnavailable Reason = 20
	ReasonImageBuildFailed        Reason = 21
	ReasonImageInUse              Reason = 22

	ReasonVolumeNotFound        Reason = 23
	ReasonVolumeUnavailable     Reason = 24
	ReasonSandboxCreationFailed Reason = 25
	ReasonSandboxStopFailed     Reason = 26
	ReasonSandboxRuntimeUnavail Reason = 27
	ReasonStorageUnavailable    Reason = 28
	ReasonInternal              Reason = 29
	ReasonOperationCanceled     Reason = 30
	ReasonOperationTimeout      Reason = 31
	ReasonResourceNotFound      Reason = 32
)

const (
	EffectUnspecified Effect = 0
	EffectNotApplied  Effect = 1
	EffectUnknown     Effect = 2
)

type notAppliedError struct {
	err error
}

func (e notAppliedError) Error() string {
	return e.err.Error()
}

func (e notAppliedError) Unwrap() error {
	return e.err
}

// NotApplied marks err as a failure that provably changed none of the state
// the RPC's contract covers. Only code that knows it failed before its first
// mutation may call it.
func NotApplied(err error) error {
	if err == nil {
		return nil
	}
	return notAppliedError{err: err}
}

// IsNotApplied reports whether err carries a NOT_APPLIED claim at any depth.
// The chain is read from the outside in so that the first explicit effect
// wins: an UNKNOWN Failure overrides an inner marker, a NOT_APPLIED Failure
// needs no marker, and a Failure that makes no claim defers to its cause. A
// marker is a claim in the same sense, so an outer marker is not hidden by an
// inner unspecified Failure.
func IsNotApplied(err error) bool {
	for err != nil {
		switch current := err.(type) {
		case *Failure:
			if current == nil {
				return false
			}
			if current.Effect != EffectUnspecified {
				return current.Effect == EffectNotApplied
			}
			err = current.Err
		case notAppliedError:
			return true
		case interface{ Unwrap() []error }:
			for _, inner := range current.Unwrap() {
				if IsNotApplied(inner) {
					return true
				}
			}
			return false
		case interface{ Unwrap() error }:
			err = current.Unwrap()
		default:
			return false
		}
	}
	return false
}

// EffectFor derives the effect a write RPC reports for err. Every error that
// is not explicitly marked NOT_APPLIED is UNKNOWN.
func EffectFor(err error) Effect {
	if IsNotApplied(err) {
		return EffectNotApplied
	}
	return EffectUnknown
}
