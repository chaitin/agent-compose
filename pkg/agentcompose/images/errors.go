package images

import (
	"fmt"
	"strings"
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
