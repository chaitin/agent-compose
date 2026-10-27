package apierror

import (
	"errors"
	"fmt"
	"testing"
)

func TestNotAppliedMarkerSurvivesWrapping(t *testing.T) {
	cause := errors.New("boom")
	marked := NotApplied(cause)
	if !IsNotApplied(marked) {
		t.Fatal("the marker must be visible on the wrapped error")
	}
	if !errors.Is(marked, cause) {
		t.Fatal("Unwrap must keep the cause reachable")
	}
	if !IsNotApplied(fmt.Errorf("apply project: %w", marked)) {
		t.Fatal("the marker must survive fmt.Errorf wrapping")
	}
	if IsNotApplied(cause) || IsNotApplied(nil) {
		t.Fatal("an unmarked or nil error must not report NOT_APPLIED")
	}
	if NotApplied(nil) != nil {
		t.Fatal("NotApplied(nil) must be nil")
	}
}

func TestEffectForDefaultsToUnknown(t *testing.T) {
	cause := errors.New("boom")
	if got := EffectFor(cause); got != EffectUnknown {
		t.Fatalf("EffectFor(unmarked) = %d, want unknown", got)
	}
	if got := EffectFor(NotApplied(cause)); got != EffectNotApplied {
		t.Fatalf("EffectFor(marked) = %d, want not_applied", got)
	}
	if got := EffectFor(nil); got != EffectUnknown {
		t.Fatalf("EffectFor(nil) = %d, want unknown (fail closed)", got)
	}
}
