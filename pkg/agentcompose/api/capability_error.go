package api

import (
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/capability"
)

type CapabilityRuntimeConfig interface {
	CapProxyListen() string
}

func CapabilityConnectError(err error) error {
	var upstream *capability.OctoBusError
	switch {
	case errors.Is(err, capability.ErrNotConfigured):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, capability.ErrInvalidCatalog):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, capability.ErrInvalidInvoke):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.As(err, &upstream):
		return connect.NewError(octoBusConnectCode(upstream), err)
	default:
		return connect.NewError(connect.CodeUnavailable, err)
	}
}

func octoBusConnectCode(err *capability.OctoBusError) connect.Code {
	if err == nil {
		return connect.CodeUnavailable
	}
	switch strings.ToUpper(strings.TrimSpace(err.Code)) {
	case "INVALID_ARGUMENT":
		return connect.CodeInvalidArgument
	case "UNAUTHENTICATED":
		return connect.CodeUnauthenticated
	case "PERMISSION_DENIED":
		return connect.CodePermissionDenied
	case "NOT_FOUND":
		return connect.CodeNotFound
	case "UNIMPLEMENTED":
		return connect.CodeUnimplemented
	case "RESOURCE_EXHAUSTED":
		return connect.CodeResourceExhausted
	case "DEADLINE_EXCEEDED":
		return connect.CodeDeadlineExceeded
	case "FAILED_PRECONDITION":
		return connect.CodeFailedPrecondition
	case "ABORTED":
		return connect.CodeAborted
	case "ALREADY_EXISTS":
		return connect.CodeAlreadyExists
	case "CANCELED", "CANCELLED":
		return connect.CodeCanceled
	case "UNAVAILABLE":
		return connect.CodeUnavailable
	case "INTERNAL":
		return connect.CodeInternal
	case "OUT_OF_RANGE":
		return connect.CodeOutOfRange
	case "DATA_LOSS":
		return connect.CodeDataLoss
	case "UNKNOWN":
		return connect.CodeUnknown
	}
	switch err.HTTPStatus {
	case http.StatusBadRequest:
		return connect.CodeInvalidArgument
	case http.StatusUnauthorized:
		return connect.CodeUnauthenticated
	case http.StatusForbidden:
		return connect.CodePermissionDenied
	case http.StatusNotFound:
		return connect.CodeNotFound
	case http.StatusConflict:
		return connect.CodeAborted
	case http.StatusPreconditionFailed, http.StatusUnprocessableEntity:
		return connect.CodeFailedPrecondition
	case http.StatusTooManyRequests:
		return connect.CodeResourceExhausted
	case http.StatusInternalServerError:
		return connect.CodeInternal
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return connect.CodeUnavailable
	case http.StatusNotImplemented:
		return connect.CodeUnimplemented
	case http.StatusGatewayTimeout:
		return connect.CodeDeadlineExceeded
	}
	return connect.CodeUnknown
}
