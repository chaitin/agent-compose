package chat

import (
	"cmp"
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors reported through [Error.Unwrap], so callers can classify a
// failure with errors.Is without matching on message text.
var (
	// ErrInvalidArgument reports a request the server or this package rejected.
	ErrInvalidArgument = errors.New("chat: invalid argument")
	// ErrNotFound reports a conversation or agent that does not exist.
	ErrNotFound = errors.New("chat: not found")
	// ErrPermission reports a request the caller is not authorized to make.
	ErrPermission = errors.New("chat: permission denied")
	// ErrUnavailable reports a daemon that could not be reached or is failing.
	ErrUnavailable = errors.New("chat: service unavailable")
	// ErrBusy reports a Send issued while an earlier Reply is still streaming.
	ErrBusy = errors.New("chat: a reply is already in progress")
	// ErrClosed reports use of a Conversation after Close or Delete.
	ErrClosed = errors.New("chat: conversation is closed")
)

// Error reports a failed agent-compose request.
type Error struct {
	// Op names the package operation that failed, such as "Send".
	Op string
	// Code is the server's error code, empty when the failure was local.
	Code string
	// Status is the HTTP status, zero when the failure was local.
	Status int
	// Message is the server's human-readable description.
	Message string
}

func (e *Error) Error() string {
	message := cmp.Or(e.Message, "request failed")
	switch {
	case e.Code != "":
		return fmt.Sprintf("chat: %s: %s: %s", e.Op, e.Code, message)
	case e.Status != 0:
		return fmt.Sprintf("chat: %s: HTTP %d: %s", e.Op, e.Status, message)
	default:
		return fmt.Sprintf("chat: %s: %s", e.Op, message)
	}
}

// Unwrap maps the server's error code onto one of this package's sentinels.
func (e *Error) Unwrap() error {
	switch strings.ToLower(strings.TrimSpace(e.Code)) {
	case "invalid_argument", "out_of_range", "failed_precondition":
		return ErrInvalidArgument
	case "not_found":
		return ErrNotFound
	case "permission_denied", "unauthenticated":
		return ErrPermission
	case "unavailable", "deadline_exceeded", "resource_exhausted", "internal", "unknown":
		return ErrUnavailable
	}
	switch e.Status {
	case 400, 412, 416:
		return ErrInvalidArgument
	case 401, 403:
		return ErrPermission
	case 404:
		return ErrNotFound
	case 429, 500, 502, 503, 504:
		return ErrUnavailable
	}
	return nil
}

func invalidArgument(op, format string, args ...any) error {
	return &Error{Op: op, Code: "invalid_argument", Message: fmt.Sprintf(format, args...)}
}

func unavailable(op string, err error) error {
	return &Error{Op: op, Code: "unavailable", Message: err.Error()}
}
