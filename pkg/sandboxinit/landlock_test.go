package sandboxinit

import (
	"strings"
	"testing"
)

func TestParseLandlockMode(t *testing.T) {
	for _, test := range []struct {
		raw       string
		want      LandlockMode
		wantError bool
	}{
		{raw: "", want: LandlockModeOff},
		{raw: "off", want: LandlockModeOff},
		{raw: " OFF ", want: LandlockModeOff},
		{raw: "enforce", want: LandlockModeEnforce},
		{raw: "ENFORCE", want: LandlockModeEnforce},
		{raw: "audit", wantError: true},
		{raw: "true", wantError: true},
	} {
		t.Run(test.raw, func(t *testing.T) {
			got, err := ParseLandlockMode(test.raw)
			if (err != nil) != test.wantError {
				t.Fatalf("ParseLandlockMode(%q) error = %v, wantError %v", test.raw, err, test.wantError)
			}
			if !test.wantError && got != test.want {
				t.Fatalf("ParseLandlockMode(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestParseLandlockFallbackDefaultsToFail(t *testing.T) {
	for _, test := range []struct {
		raw       string
		want      LandlockFallback
		wantError bool
	}{
		{raw: "", want: LandlockFallbackFail},
		{raw: "fail", want: LandlockFallbackFail},
		{raw: "warn", want: LandlockFallbackWarn},
		{raw: " WARN ", want: LandlockFallbackWarn},
		{raw: "ignore", wantError: true},
	} {
		t.Run(test.raw, func(t *testing.T) {
			got, err := ParseLandlockFallback(test.raw)
			if (err != nil) != test.wantError {
				t.Fatalf("ParseLandlockFallback(%q) error = %v, wantError %v", test.raw, err, test.wantError)
			}
			if !test.wantError && got != test.want {
				t.Fatalf("ParseLandlockFallback(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestDefaultLandlockPolicyIncludesGuestWritablePaths(t *testing.T) {
	policy, err := DefaultLandlockPolicy([]string{
		"WORKSPACE=/work",
		"HOME=/home/agent",
		"STATE_ROOT=/state",
		"RUNTIME_ROOT=/runtime",
		"LOG_ROOT=/logs",
		"TMPDIR=/scratch",
	})
	if err != nil {
		t.Fatalf("DefaultLandlockPolicy() error = %v", err)
	}
	want := map[string]LandlockScope{
		"/work":       LandlockScopeReadWrite,
		"/home/agent": LandlockScopeReadWrite,
		"/state":      LandlockScopeReadWrite,
		"/runtime":    LandlockScopeReadWrite,
		"/logs":       LandlockScopeReadWrite,
		"/scratch":    LandlockScopeReadWrite,
		"/var/tmp":    LandlockScopeReadWrite,
		"/usr":        LandlockScopeReadExecute,
		"/etc":        LandlockScopeRead,
		"/proc":       LandlockScopeReadWrite,
		"/dev":        LandlockScopeReadWrite,
	}
	got := make(map[string]LandlockScope, len(policy.Rules))
	for _, rule := range policy.Rules {
		got[rule.Path] = rule.Scope
	}
	for path, scope := range want {
		if got[path] != scope {
			t.Fatalf("policy rule for %q = %q, want %q (rules: %#v)", path, got[path], scope, policy.Rules)
		}
	}
	for _, denied := range []string{"/", "/.agent-compose", "/run/agent-compose", "/data"} {
		if _, ok := got[denied]; ok {
			t.Fatalf("default policy grants %q; the baseline must deny it", denied)
		}
	}
}

func TestDefaultLandlockPolicyUsesDefaultsWhenEnvironmentIsEmpty(t *testing.T) {
	policy, err := DefaultLandlockPolicy(nil)
	if err != nil {
		t.Fatalf("DefaultLandlockPolicy() error = %v", err)
	}
	got := make(map[string]LandlockScope, len(policy.Rules))
	for _, rule := range policy.Rules {
		got[rule.Path] = rule.Scope
	}
	for _, path := range []string{"/workspace", "/root", "/data/state", "/data/runtime", "/data/logs", "/tmp"} {
		if got[path] != LandlockScopeReadWrite {
			t.Fatalf("default writable path %q = %q, want read-write", path, got[path])
		}
	}
}

func TestDefaultLandlockPolicyRejectsRelativePaths(t *testing.T) {
	for _, name := range []string{"WORKSPACE", "HOME", "STATE_ROOT", "RUNTIME_ROOT", "LOG_ROOT", "TMPDIR"} {
		t.Run(name, func(t *testing.T) {
			_, err := DefaultLandlockPolicy([]string{name + "=relative/dir"})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("DefaultLandlockPolicy() error = %v, want a %s path error", err, name)
			}
		})
	}
}

func TestLandlockPolicyValidateRejectsMalformedRules(t *testing.T) {
	for _, test := range []struct {
		name    string
		policy  LandlockPolicy
		wantErr string
	}{
		{name: "empty", policy: LandlockPolicy{}, wantErr: "no rules"},
		{name: "relative", policy: LandlockPolicy{Rules: []LandlockRule{{Path: "tmp", Scope: LandlockScopeRead}}}, wantErr: "absolute"},
		{name: "unclean", policy: LandlockPolicy{Rules: []LandlockRule{{Path: "/tmp/../etc", Scope: LandlockScopeRead}}}, wantErr: "clean"},
		{name: "scope", policy: LandlockPolicy{Rules: []LandlockRule{{Path: "/tmp", Scope: "write-only"}}}, wantErr: "unknown scope"},
		{name: "duplicate", policy: LandlockPolicy{Rules: []LandlockRule{{Path: "/tmp", Scope: LandlockScopeRead}, {Path: "/tmp", Scope: LandlockScopeRead}}}, wantErr: "duplicated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.policy.Validate()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
