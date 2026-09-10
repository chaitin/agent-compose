package model

import (
	"errors"
	"testing"
)

func TestWorkspaceDeliveryContract(t *testing.T) {
	for _, provider := range []string{"file", "git", "custom"} {
		for _, mode := range []string{"", "copy", "mount", " MOUNT ", "map", "future"} {
			for _, readOnly := range []bool{false, true} {
				got, err := NormalizeWorkspaceDelivery(provider, mode, readOnly)
				wantMount := mode == "mount" || mode == " MOUNT "
				valid := (mode == "" || mode == "copy") && !readOnly || wantMount && provider == "file"
				if !valid {
					if !errors.Is(err, ErrInvalidArgument) {
						t.Fatalf("delivery(%q,%q,%t) = %q, %v; want invalid argument", provider, mode, readOnly, got, err)
					}
					continue
				}
				want := ""
				if wantMount {
					want = WorkspaceModeMount
				}
				if err != nil || got != want {
					t.Fatalf("delivery(%q,%q,%t) = %q, %v; want %q", provider, mode, readOnly, got, err, want)
				}
			}
		}
	}
}
