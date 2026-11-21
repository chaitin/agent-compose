package driver

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sandboxHostErrorDomain scopes the ErrorInfo reasons the sandbox-host
// attaches to a status, so a reason cannot be confused with one another
// service set.
const sandboxHostErrorDomain = "sandbox-host.agent-compose"

// sandboxHostReasonResourceLimitsUnavailable marks a FailedPrecondition that
// carries ErrSandboxResourceLimitsUnavailable rather than
// ErrGuestRuntimeNotReady, which shares the code. Without the reason the daemon
// would classify a refused resource declaration as a guest that is not ready
// yet and retry it.
const sandboxHostReasonResourceLimitsUnavailable = "SANDBOX_RESOURCE_LIMITS_UNAVAILABLE"

// sandboxHostReasonStorageRootUnsafe marks a FailedPrecondition that carries
// ErrSandboxStorageRootUnsafe. Like the resource-limits reason it keeps the
// daemon from reading a deployment misconfiguration as a guest that is not
// ready yet.
const sandboxHostReasonStorageRootUnsafe = "SANDBOX_STORAGE_ROOT_UNSAFE"

func sandboxHostResourceLimitsStatus(err error) error {
	return sandboxHostFailedPreconditionStatus(err, sandboxHostReasonResourceLimitsUnavailable)
}

func sandboxHostStorageRootStatus(err error) error {
	return sandboxHostFailedPreconditionStatus(err, sandboxHostReasonStorageRootUnsafe)
}

func sandboxHostFailedPreconditionStatus(err error, reason string) error {
	st := status.New(codes.FailedPrecondition, err.Error())
	if detailed, detailErr := st.WithDetails(&errdetails.ErrorInfo{
		Reason: reason,
		Domain: sandboxHostErrorDomain,
	}); detailErr == nil {
		st = detailed
	}
	return st.Err()
}

// sandboxHostStatusReason returns the sandbox-host ErrorInfo reason a status
// carries, or "" when it carries none.
func sandboxHostStatusReason(err error) string {
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.GetDomain() == sandboxHostErrorDomain {
			return info.GetReason()
		}
	}
	return ""
}
