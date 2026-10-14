package model

import (
	"context"
	"testing"
)

type detachedContextRequestKey struct{}

// An execution that outlives its request keeps the caller's metadata but not the
// request's lifetime or its other values.
func TestDetachedContextFromRequestCarriesOnlyRequestMetadata(t *testing.T) {
	headers := []TrustedHeader{{Name: "x-mpi-username", Value: "bob@example.com"}}
	trace := TraceContext{Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}

	requestCtx := NewContextWithRequestMetadata(context.Background(), RequestMetadata{TrustedHeaders: headers, TraceContext: trace})
	requestCtx = context.WithValue(requestCtx, detachedContextRequestKey{}, "request-only")
	requestCtx, cancelRequest := context.WithCancel(requestCtx)

	execCtx := DetachedContextFromRequest(context.Background(), requestCtx)
	cancelRequest()

	if cause := context.Cause(execCtx); cause != nil {
		t.Fatalf("detached context inherited the request's cancellation: %v", cause)
	}
	got := TrustedHeadersFromContext(execCtx)
	if len(got) != 1 || got[0] != headers[0] {
		t.Fatalf("detached trusted headers = %#v, want %#v", got, headers)
	}
	if gotTrace := TraceContextFromContext(execCtx); gotTrace != trace {
		t.Fatalf("detached trace context = %#v, want %#v", gotTrace, trace)
	}
	if value := execCtx.Value(detachedContextRequestKey{}); value != nil {
		t.Fatalf("detached context inherited an unrelated request value: %#v", value)
	}
}

// A request that carries no identity yields a detached execution with no
// identity, which is how unattended runs stay anonymous.
func TestDetachedContextFromRequestWithoutMetadataIsAnonymous(t *testing.T) {
	execCtx := DetachedContextFromRequest(context.Background(), context.Background())

	if got := TrustedHeadersFromContext(execCtx); len(got) != 0 {
		t.Fatalf("detached trusted headers = %#v, want none", got)
	}
	if got := TraceContextFromContext(execCtx); got != (TraceContext{}) {
		t.Fatalf("detached trace context = %#v, want the zero value", got)
	}
}
