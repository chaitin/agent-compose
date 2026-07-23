package model

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrNotFound                   = errors.New("not found")
	ErrInvalidArgument            = errors.New("invalid argument")
	ErrUnsupported                = errors.New("unsupported")
	ErrRequired                   = errors.New("required")
	ErrAmbiguous                  = errors.New("ambiguous")
	ErrConflict                   = errors.New("conflict")
	ErrAlreadyExists              = errors.New("already exists")
	ErrReferenced                 = errors.New("referenced")
	ErrFailedPrecondition         = errors.New("failed precondition")
	ErrBodyTooLarge               = errors.New("body too large")
	ErrExecTerminationUnconfirmed = errors.New("guest execution termination unconfirmed")
)

type ClassifiedError struct {
	Kind     error
	Resource string
	ID       string
	Reason   string
	Cause    error
}

func (e ClassifiedError) Error() string {
	message := strings.TrimSpace(e.Reason)
	if message == "" && e.Cause != nil {
		message = e.Cause.Error()
	}
	if message == "" {
		parts := make([]string, 0, 3)
		if e.Resource != "" {
			parts = append(parts, e.Resource)
		}
		if e.ID != "" {
			parts = append(parts, e.ID)
		}
		if e.Kind != nil {
			parts = append(parts, e.Kind.Error())
		}
		message = strings.Join(parts, " ")
	}
	if e.Cause != nil && message != e.Cause.Error() {
		return fmt.Sprintf("%s: %v", message, e.Cause)
	}
	return message
}

func (e ClassifiedError) Unwrap() error {
	return e.Cause
}

func (e ClassifiedError) Is(target error) bool {
	return target != nil && e.Kind != nil && errors.Is(e.Kind, target)
}

func ClassifyError(kind error, reason string, cause error) error {
	return ClassifiedError{Kind: kind, Reason: reason, Cause: cause}
}

func ResourceError(kind error, resource, id, reason string, cause error) error {
	return ClassifiedError{Kind: kind, Resource: resource, ID: id, Reason: reason, Cause: cause}
}
