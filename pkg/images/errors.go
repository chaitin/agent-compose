package images

import (
	"errors"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"

	"agent-compose/pkg/imagecache"
)

var ErrBuildUnsupported = errors.New("image build is not supported by selected image backend")

type ErrorKind int

const (
	ErrorKindUnknown ErrorKind = iota
	ErrorKindNotFound
	ErrorKindInvalidReference
	ErrorKindConflict
	ErrorKindInternal
	ErrorKindUnavailable
)

type OpError struct {
	Op       string
	Endpoint string
	ImageRef string
	Err      error
}

func (e OpError) Error() string {
	parts := []string{strings.TrimSpace(e.Op)}
	if e.ImageRef != "" {
		parts = append(parts, fmt.Sprintf("image %s", e.ImageRef))
	}
	if e.Endpoint != "" {
		parts = append(parts, fmt.Sprintf("endpoint %s", e.Endpoint))
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	return strings.Join(parts, ": ")
}

func (e OpError) Unwrap() error {
	return e.Err
}

func ClassifyBackendError(err error) (OpError, ErrorKind, bool) {
	var backendErr OpError
	if !errors.As(err, &backendErr) {
		return OpError{}, ErrorKindUnknown, false
	}
	kind := ErrorKindUnavailable
	if cerrdefs.IsNotFound(backendErr.Err) {
		kind = ErrorKindNotFound
	}
	switch imagecache.Kind(backendErr.Err) {
	case imagecache.ErrorKindNotFound:
		kind = ErrorKindNotFound
	case imagecache.ErrorKindInvalidReference:
		kind = ErrorKindInvalidReference
	case imagecache.ErrorKindConflict:
		kind = ErrorKindConflict
	case imagecache.ErrorKindInternal:
		kind = ErrorKindInternal
	case imagecache.ErrorKindUnavailable:
		kind = ErrorKindUnavailable
	}
	return backendErr, kind, true
}

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var backendErr OpError
	if errors.As(err, &backendErr) {
		return cerrdefs.IsNotFound(backendErr.Err) || imagecache.IsKind(backendErr.Err, imagecache.ErrorKindNotFound)
	}
	return cerrdefs.IsNotFound(err) || imagecache.IsKind(err, imagecache.ErrorKindNotFound)
}
