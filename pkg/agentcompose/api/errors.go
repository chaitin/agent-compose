package api

import (
	"context"
	"database/sql"
	"errors"

	"connectrpc.com/connect"

	domain "agent-compose/pkg/model"
)

func ConnectErrorForDomain(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	case errors.Is(err, domain.ErrUnsupported):
		return connect.NewError(connect.CodeUnimplemented, err)
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrInvalidArgument), errors.Is(err, domain.ErrRequired), errors.Is(err, domain.ErrAmbiguous):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, domain.ErrFailedPrecondition), errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrReferenced):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, domain.ErrAlreadyExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
