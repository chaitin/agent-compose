package cli

import (
	"errors"

	"connectrpc.com/connect"
)

type commandExitError struct {
	Code int
	Err  error
}

func (e commandExitError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e commandExitError) Unwrap() error {
	return e.Err
}

const (
	exitCodeGeneral     = 1
	exitCodeUsage       = 2
	exitCodeUnavailable = 3
)

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr commandExitError
	if errors.As(err, &exitErr) && exitErr.Code > 0 {
		return exitErr.Code
	}
	return exitCodeGeneral
}

func commandExitErrorForConnect(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable:
		return commandExitError{Code: exitCodeUnavailable, Err: err}
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeNotFound:
		return commandExitError{Code: exitCodeUsage, Err: err}
	default:
		return err
	}
}
