package apierror

import (
	"errors"
	"fmt"
	"testing"
)

func TestFailureCarriesContractThroughWrapping(t *testing.T) {
	cause := errors.New("upstream refused the request")
	failure := NewUnknownFailure(ReasonImagePullFailed, cause)
	wrapped := fmt.Errorf("pull image: %w", fmt.Errorf("registry: %w", failure))

	var got *Failure
	if !errors.As(wrapped, &got) {
		t.Fatalf("errors.As(%v, *Failure) = false", wrapped)
	}
	if got.Reason != ReasonImagePullFailed {
		t.Errorf("Reason = %d, want %d", got.Reason, ReasonImagePullFailed)
	}
	if got.Effect != EffectUnknown {
		t.Errorf("Effect = %d, want %d", got.Effect, EffectUnknown)
	}
	if !errors.Is(wrapped, cause) {
		t.Errorf("errors.Is(%v, cause) = false", wrapped)
	}
}

func TestIsNotAppliedHonorsFailureEffect(t *testing.T) {
	cause := errors.New("boom")
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "plain error", err: cause, want: false},
		{name: "marker", err: NotApplied(cause), want: true},
		{name: "typed not applied", err: NewNotAppliedFailure(ReasonProjectSpecInvalid, cause), want: true},
		{name: "typed unknown", err: NewUnknownFailure(ReasonInternal, cause), want: false},
		{
			name: "unspecified failure falls through to marker",
			err:  NewFailure(ReasonInternal, NotApplied(cause)),
			want: true,
		},
		{
			name: "explicit unknown overrides inner marker",
			err:  NewUnknownFailure(ReasonInternal, NotApplied(cause)),
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotApplied(tc.err); got != tc.want {
				t.Errorf("IsNotApplied(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestEffectForTypedFailure(t *testing.T) {
	cause := errors.New("boom")
	if got := EffectFor(NewNotAppliedFailure(ReasonInternal, cause)); got != EffectNotApplied {
		t.Errorf("EffectFor(not applied) = %d, want %d", got, EffectNotApplied)
	}
	if got := EffectFor(NewFailure(ReasonInternal, cause)); got != EffectUnknown {
		t.Errorf("EffectFor(unspecified) = %d, want %d", got, EffectUnknown)
	}
}
