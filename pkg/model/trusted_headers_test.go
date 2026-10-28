package model

import (
	"context"
	"testing"
)

func TestTrustedHeadersContextCopiesValues(t *testing.T) {
	original := []TrustedHeader{{Name: "x-mpi-user-id", Value: "user-1"}}
	ctx := NewContextWithTrustedHeaders(context.Background(), original)
	original[0].Value = "mutated-input"

	first := TrustedHeadersFromContext(ctx)
	if len(first) != 1 || first[0].Value != "user-1" {
		t.Fatalf("trusted headers = %#v", first)
	}
	first[0].Value = "mutated-output"
	second := TrustedHeadersFromContext(ctx)
	if len(second) != 1 || second[0].Value != "user-1" {
		t.Fatalf("stored trusted headers were mutated: %#v", second)
	}
}

func TestTrustedHeaderValueMatchesFirstNonEmptyValue(t *testing.T) {
	ctx := NewContextWithTrustedHeaders(context.Background(), []TrustedHeader{
		{Name: "x-mpi-user-id", Value: "user-1"},
		{Name: "x-mpi-username", Value: "alice"},
		{Name: "x-mpi-username", Value: "bob"},
	})
	if got := TrustedHeaderValue(ctx, "X-MPI-Username"); got != "alice" {
		t.Fatalf("username = %q, want alice", got)
	}
	if got := TrustedHeaderValue(ctx, "x-mpi-user-id"); got != "user-1" {
		t.Fatalf("user id = %q, want user-1", got)
	}
	if got := TrustedHeaderValue(ctx, "x-mpi-missing"); got != "" {
		t.Fatalf("missing header = %q, want empty", got)
	}
	if got := TrustedHeaderValue(context.Background(), "x-mpi-username"); got != "" {
		t.Fatalf("absent context = %q, want empty", got)
	}
}
