package imagecache

import (
	"errors"
	"fmt"
)

type ErrorKind string

const (
	ErrorKindNotFound         ErrorKind = "not_found"
	ErrorKindInvalidReference ErrorKind = "invalid_reference"
	ErrorKindUnavailable      ErrorKind = "unavailable"
	ErrorKindConflict         ErrorKind = "conflict"
	ErrorKindInternal         ErrorKind = "internal"
)

type Error struct {
	Kind      ErrorKind
	Operation string
	Reference string
	Err       error
}

func NewError(kind ErrorKind, operation, reference string, err error) *Error {
	return &Error{Kind: kind, Operation: operation, Reference: reference, Err: err}
}

func (e *Error) Error() string {
	message := string(e.Kind)
	if e.Operation != "" {
		message = e.Operation + ": " + message
	}
	if e.Reference != "" {
		message += " " + e.Reference
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *Error) Unwrap() error {
	return e.Err
}

func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	return other.Kind == "" || e.Kind == other.Kind
}

func Kind(err error) ErrorKind {
	var cacheErr *Error
	if errors.As(err, &cacheErr) {
		return cacheErr.Kind
	}
	return ""
}

func IsKind(err error, kind ErrorKind) bool {
	return errors.Is(err, &Error{Kind: kind})
}

func newMetadataError(operation, path string, err error) *Error {
	return NewError(ErrorKindInternal, operation, path, fmt.Errorf("metadata: %w", err))
}
