package driver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	testStorageRootDir  = 0o040000
	testStorageRootReg  = 0o100000
	testStorageRootEUID = 1000
)

func dirEntry(perm uint32, uid uint32) storageRootEntry {
	return storageRootEntry{isDir: true, mode: testStorageRootDir | perm, uid: uid}
}

func fakeStorageRootLstat(entries map[string]storageRootEntry) storageRootLstat {
	return func(path string) (storageRootEntry, error) {
		entry, ok := entries[path]
		if !ok {
			return storageRootEntry{}, fmt.Errorf("lstat %s: %w", path, os.ErrNotExist)
		}
		return entry, nil
	}
}

// The cases mirror the branches of Microsandbox v0.7.14 verify_ancestors so
// the preflight accepts and refuses exactly what the SDK will.
func TestCheckStorageRootAncestors(t *testing.T) {
	cases := []struct {
		name        string
		entries     map[string]storageRootEntry
		wantUnsafe  bool
		wantInError string
	}{
		{
			name: "root owned 0755 chain is accepted",
			entries: map[string]storageRootEntry{
				"/":                  dirEntry(0o755, 0),
				"/data":              dirEntry(0o755, 0),
				"/data/microsandbox": dirEntry(0o755, 0),
			},
		},
		{
			// The devboard failure: the bind-mounted data root was 0775.
			name: "group-writable ancestor is refused",
			entries: map[string]storageRootEntry{
				"/":                  dirEntry(0o755, 0),
				"/data":              dirEntry(0o775, 0),
				"/data/microsandbox": dirEntry(0o755, 0),
			},
			wantUnsafe:  true,
			wantInError: "ancestor /data of /data/microsandbox has mode 0775",
		},
		{
			name: "other-writable storage root itself is refused",
			entries: map[string]storageRootEntry{
				"/":                  dirEntry(0o755, 0),
				"/data":              dirEntry(0o755, 0),
				"/data/microsandbox": dirEntry(0o757, 0),
			},
			wantUnsafe:  true,
			wantInError: "ancestor /data/microsandbox",
		},
		{
			name: "sticky world-writable ancestor is accepted",
			entries: map[string]storageRootEntry{
				"/":         dirEntry(0o755, 0),
				"/tmp":      dirEntry(0o1777, 0),
				"/tmp/home": dirEntry(0o755, testStorageRootEUID),
			},
		},
		{
			name: "private boundary owned by the driver shields permissive descendants",
			entries: map[string]storageRootEntry{
				"/":               dirEntry(0o755, 0),
				"/srv":            dirEntry(0o700, testStorageRootEUID),
				"/srv/shared":     dirEntry(0o777, testStorageRootEUID),
				"/srv/shared/msb": dirEntry(0o775, 0),
			},
		},
		{
			name: "private directory owned by root is not a boundary for another uid",
			entries: map[string]storageRootEntry{
				"/":           dirEntry(0o755, 0),
				"/srv":        dirEntry(0o700, 0),
				"/srv/shared": dirEntry(0o777, testStorageRootEUID),
			},
			wantUnsafe:  true,
			wantInError: "ancestor /srv/shared",
		},
		{
			name: "untrusted owner is refused even past a private boundary",
			entries: map[string]storageRootEntry{
				"/":        dirEntry(0o755, 0),
				"/srv":     dirEntry(0o700, testStorageRootEUID),
				"/srv/msb": dirEntry(0o755, 4242),
			},
			wantUnsafe:  true,
			wantInError: "owned by uid 4242",
		},
		{
			name: "non-directory ancestor is refused",
			entries: map[string]storageRootEntry{
				"/":     dirEntry(0o755, 0),
				"/data": {isDir: false, mode: testStorageRootReg | 0o644, uid: 0},
			},
			wantUnsafe:  true,
			wantInError: "is not a real directory",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var deepest string
			for path := range tc.entries {
				if len(path) > len(deepest) {
					deepest = path
				}
			}
			err := checkStorageRootAncestors(deepest, testStorageRootEUID, fakeStorageRootLstat(tc.entries))
			if !tc.wantUnsafe {
				if err != nil {
					t.Fatalf("checkStorageRootAncestors(%s) = %v, want nil", deepest, err)
				}
				return
			}
			if !errors.Is(err, ErrSandboxStorageRootUnsafe) {
				t.Fatalf("checkStorageRootAncestors(%s) = %v, want ErrSandboxStorageRootUnsafe", deepest, err)
			}
			if !strings.Contains(err.Error(), tc.wantInError) {
				t.Fatalf("error %q does not contain %q", err, tc.wantInError)
			}
		})
	}
}

func TestCheckStorageRootAncestorsReportsLstatFailure(t *testing.T) {
	err := checkStorageRootAncestors("/data/msb", testStorageRootEUID, fakeStorageRootLstat(map[string]storageRootEntry{
		"/": dirEntry(0o755, 0),
	}))
	if err == nil || errors.Is(err, ErrSandboxStorageRootUnsafe) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want a non-sentinel inspection error wrapping ErrNotExist", err)
	}
}

func TestStorageRootAncestorsFromRoot(t *testing.T) {
	got := storageRootAncestorsFromRoot("/data/agent-compose/microsandbox/")
	want := []string{"/", "/data", "/data/agent-compose", "/data/agent-compose/microsandbox"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ancestors = %v, want %v", got, want)
	}
	if got := storageRootAncestorsFromRoot("/"); !reflect.DeepEqual(got, []string{"/"}) {
		t.Fatalf("ancestors of / = %v", got)
	}
}

// A real temporary directory sits under the test binary's private 0700
// directory, so the host check must accept it, including a home whose tail
// does not exist yet and is judged by its deepest existing ancestor.
func TestCheckMicrosandboxStorageRootOnHost(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not", "created", "yet")
	if err := CheckMicrosandboxStorageRoot(home); err != nil {
		t.Fatalf("CheckMicrosandboxStorageRoot(%s) = %v, want nil", home, err)
	}
	if err := CheckMicrosandboxStorageRoot(""); err != nil {
		t.Fatalf("CheckMicrosandboxStorageRoot(\"\") = %v, want nil", err)
	}
}

// A storage-root refusal must cross the sandbox-host boundary as its own
// sentinel, not as the guest-not-ready sentinel that shares FailedPrecondition,
// or the daemon would retry an operation that cannot succeed until an operator
// fixes the directory permissions.
func TestSandboxHostStatusCarriesStorageRootRefusal(t *testing.T) {
	refusal := fmt.Errorf("%w: ancestor /data of /data/microsandbox has mode 0775", ErrSandboxStorageRootUnsafe)
	got := fromGRPCError(sandboxHostStatus(refusal))
	if !errors.Is(got, ErrSandboxStorageRootUnsafe) || errors.Is(got, ErrGuestRuntimeNotReady) || errors.Is(got, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("decoded refusal = %v, want ErrSandboxStorageRootUnsafe only", got)
	}
	if !strings.Contains(got.Error(), "mode 0775") {
		t.Fatalf("decoded refusal %q lost the offending ancestor", got)
	}
}
