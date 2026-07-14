package api

import (
	"errors"

	"connectrpc.com/connect"

	"agent-compose/pkg/capability"
)

type CapabilityRuntimeConfig interface {
	CapProxyListen() string
}

func CapabilityConnectError(err error) error {
	switch {
	case errors.Is(err, capability.ErrNotConfigured):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, capability.ErrInvalidCatalog):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeUnavailable, err)
	}
}
