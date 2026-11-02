package schedulers

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

type schedulerBindingClaimStoreStub struct {
	called      bool
	expected    *domain.SchedulerBinding
	replacement domain.SchedulerBinding
	claimed     bool
	err         error
}

func (s *schedulerBindingClaimStoreStub) CompareAndSwapSchedulerBinding(_ context.Context, expected *domain.SchedulerBinding, replacement domain.SchedulerBinding) (bool, error) {
	s.called = true
	s.expected = expected
	s.replacement = replacement
	return s.claimed, s.err
}

func TestSchedulerBindingsMatchComparesStickyState(t *testing.T) {
	current := domain.SchedulerBinding{
		SchedulerID:         "scheduler-1",
		TriggerID:           "trigger-1",
		SandboxID:           "sandbox-1",
		SandboxConfigHash:   "sha256:current",
		SchedulerConfigHash: "sha256:base-current",
		CreatedAt:           time.Unix(1, 0),
		UpdatedAt:           time.Unix(2, 0),
	}
	expected := current
	expected.SchedulerID = " scheduler-1 "
	expected.TriggerID = " trigger-1 "
	expected.SandboxID = " sandbox-1 "
	expected.SandboxConfigHash = " sha256:current "
	expected.SchedulerConfigHash = " sha256:base-current "
	expected.CreatedAt = time.Unix(3, 0)
	expected.UpdatedAt = time.Unix(4, 0)
	if !SchedulerBindingsMatch(current, expected) {
		t.Fatal("equivalent sticky binding state did not match")
	}

	for name, mutate := range map[string]func(*domain.SchedulerBinding){
		"scheduler":             func(binding *domain.SchedulerBinding) { binding.SchedulerID = "scheduler-2" },
		"trigger":               func(binding *domain.SchedulerBinding) { binding.TriggerID = "trigger-2" },
		"sandbox":               func(binding *domain.SchedulerBinding) { binding.SandboxID = "sandbox-2" },
		"config hash":           func(binding *domain.SchedulerBinding) { binding.SandboxConfigHash = "sha256:other" },
		"scheduler config hash": func(binding *domain.SchedulerBinding) { binding.SchedulerConfigHash = "sha256:base-other" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := current
			mutate(&changed)
			if SchedulerBindingsMatch(current, changed) {
				t.Fatalf("binding with changed %s matched", name)
			}
		})
	}
}

func TestRetiringSchedulerBindingPreservesSandboxAndTracksDesiredConfig(t *testing.T) {
	binding := domain.SchedulerBinding{
		SchedulerID:       "scheduler-1",
		TriggerID:         "trigger-1",
		SandboxID:         "sandbox-1",
		SandboxConfigHash: "sha256:old",
	}
	retiring := RetiringSchedulerBinding(binding, " sha256:new ")
	if retiring.SchedulerID != binding.SchedulerID || retiring.TriggerID != binding.TriggerID || retiring.SandboxID != binding.SandboxID {
		t.Fatalf("retiring binding changed identity: got %#v want %#v", retiring, binding)
	}
	if desiredHash, ok := RetiringSchedulerBindingConfigHash(retiring); !ok || desiredHash != "sha256:new" {
		t.Fatalf("RetiringSchedulerBindingConfigHash = %q/%v, want sha256:new/true", desiredHash, ok)
	}
	if _, ok := RetiringSchedulerBindingConfigHash(binding); ok {
		t.Fatal("ordinary binding reported as retiring")
	}
}

func TestAdoptLegacySchedulerBindingConfigHash(t *testing.T) {
	binding := domain.SchedulerBinding{
		SchedulerID: "scheduler-1",
		TriggerID:   "trigger-1",
		SandboxID:   "sandbox-1",
	}
	adopted, ok := AdoptLegacySchedulerBindingConfigHash(binding, " sha256:current ")
	if !ok {
		t.Fatal("legacy binding was not adopted")
	}
	if adopted.SchedulerID != binding.SchedulerID || adopted.TriggerID != binding.TriggerID || adopted.SandboxID != binding.SandboxID {
		t.Fatalf("adopted binding changed identity: got %#v want %#v", adopted, binding)
	}
	if adopted.SandboxConfigHash != "sha256:current" {
		t.Fatalf("adopted config hash = %q, want sha256:current", adopted.SandboxConfigHash)
	}
	if binding.SandboxConfigHash != "" {
		t.Fatalf("AdoptLegacySchedulerBindingConfigHash mutated its input: %#v", binding)
	}

	for name, test := range map[string]struct {
		binding domain.SchedulerBinding
		desired string
	}{
		"current binding": {binding: adopted, desired: "sha256:other"},
		"empty desired":   {binding: binding, desired: ""},
		"retiring binding": {
			binding: RetiringSchedulerBinding(binding, "sha256:current"),
			desired: "sha256:current",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := AdoptLegacySchedulerBindingConfigHash(test.binding, test.desired)
			if ok || got != test.binding {
				t.Fatalf("AdoptLegacySchedulerBindingConfigHash = %#v/%v, want unchanged/false", got, ok)
			}
		})
	}
}

func TestClaimLegacySchedulerBindingConfigHash(t *testing.T) {
	legacy := domain.SchedulerBinding{SchedulerID: "scheduler-1", TriggerID: "trigger-1", SandboxID: "sandbox-1"}
	storeErr := errors.New("store unavailable")
	for name, test := range map[string]struct {
		binding       domain.SchedulerBinding
		claimed       bool
		storeErr      error
		wantHash      string
		wantCurrent   bool
		wantStoreCall bool
		wantErr       error
	}{
		"CAS winner":      {binding: legacy, claimed: true, wantHash: "sha256:current", wantCurrent: true, wantStoreCall: true},
		"CAS loser":       {binding: legacy, claimed: false, wantHash: "sha256:current", wantStoreCall: true},
		"current binding": {binding: domain.SchedulerBinding{SchedulerID: "scheduler-1", SandboxConfigHash: "sha256:existing"}, wantHash: "sha256:existing", wantCurrent: true},
		"store error":     {binding: legacy, storeErr: storeErr, wantStoreCall: true, wantErr: storeErr},
	} {
		t.Run(name, func(t *testing.T) {
			store := &schedulerBindingClaimStoreStub{claimed: test.claimed, err: test.storeErr}
			got, current, err := ClaimLegacySchedulerBindingConfigHash(context.Background(), store, test.binding, " sha256:current ")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ClaimLegacySchedulerBindingConfigHash error = %v, want %v", err, test.wantErr)
			}
			if current != test.wantCurrent {
				t.Fatalf("ClaimLegacySchedulerBindingConfigHash current = %v, want %v", current, test.wantCurrent)
			}
			if got.SandboxConfigHash != test.wantHash {
				t.Fatalf("ClaimLegacySchedulerBindingConfigHash hash = %q, want %q", got.SandboxConfigHash, test.wantHash)
			}
			if store.called != test.wantStoreCall {
				t.Fatalf("CompareAndSwapSchedulerBinding called = %v, want %v", store.called, test.wantStoreCall)
			}
			if store.called && (*store.expected != test.binding || store.replacement.SandboxConfigHash != "sha256:current") {
				t.Fatalf("CompareAndSwapSchedulerBinding arguments = %#v/%#v", store.expected, store.replacement)
			}
		})
	}
}
