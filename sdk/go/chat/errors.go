package chat

import (
	"cmp"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
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
	// ErrConflict reports a request that lost a race for something the daemon
	// hands to one holder at a time, such as a run's input. Retrying shortly
	// is usually the right response: the holder is normally on its way out.
	ErrConflict = errors.New("chat: conflicting request")
	// ErrBusy reports a Send issued while an earlier Reply is still streaming.
	ErrBusy = errors.New("chat: a reply is already in progress")
	// ErrClosed reports use of a Conversation after Close.
	ErrClosed = errors.New("chat: conversation is closed")
	// ErrIncomplete reports an enumeration that stopped at the caller's own
	// budget while matches were still unread. What was found is returned
	// alongside it and is usable; it is just not the whole answer. An
	// enumeration must never quietly stand in for a complete one, so a
	// [Search.Limit] that runs out says so rather than returning a subset.
	ErrIncomplete = errors.New("chat: result is incomplete")
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
	if e.conflict() {
		return ErrConflict
	}
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

// conflict reports whether this is the daemon losing a race for something it
// hands to one holder at a time.
//
// The daemon renders its conflicts as FAILED_PRECONDITION with a "conflict:"
// prefix rather than as ABORTED, so the prefix is what separates them from a
// genuine precondition failure. Both spellings are accepted, so this keeps
// working once the daemon uses the code the condition deserves.
func (e *Error) conflict() bool {
	switch strings.ToLower(strings.TrimSpace(e.Code)) {
	case "aborted", "already_exists":
		return true
	case "failed_precondition":
		return strings.HasPrefix(strings.TrimSpace(e.Message), "conflict:")
	}
	return false
}

// fromConnect names a Connect failure in this package's terms. A failure that
// never reached the daemon - a dial that did not connect, a context that
// ended - carries no code, and is reported as unavailable.
func fromConnect(op string, err error) error {
	if err == nil {
		return nil
	}
	var already *Error
	if errors.As(err, &already) {
		return already
	}
	var failure *connect.Error
	if !errors.As(err, &failure) {
		return unavailable(op, err)
	}
	return &Error{Op: op, Code: failure.Code().String(), Message: failure.Message()}
}
