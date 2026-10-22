package core

import (
	"context"
	"strings"
	"testing"
)

func TestParseComposeVersion(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		version string
		want    [3]int
	}{
		{name: "plain", version: "2.24.4", want: [3]int{2, 24, 4}},
		{name: "v prefix", version: "v2.24.4", want: [3]int{2, 24, 4}},
		{name: "missing patch", version: "2.24", want: [3]int{2, 24, 0}},
		{name: "desktop suffix", version: "2.24.4-desktop.1", want: [3]int{2, 24, 4}},
		{name: "surrounding whitespace", version: "  v2.30.0  ", want: [3]int{2, 30, 0}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseComposeVersion(testCase.version)
			if err != nil {
				t.Fatalf("parseComposeVersion(%q) = %v", testCase.version, err)
			}
			if got != testCase.want {
				t.Fatalf("parseComposeVersion(%q) = %v, want %v", testCase.version, got, testCase.want)
			}
		})
	}
	for _, version := range []string{"", "v", "two.twenty.four", "2.x.4"} {
		if _, err := parseComposeVersion(version); err == nil {
			t.Fatalf("parseComposeVersion(%q) expected an error", version)
		}
	}
}

func TestComposeVersionAtLeast(t *testing.T) {
	for _, testCase := range []struct {
		version string
		minimum string
		want    bool
	}{
		{"2.24.4", "2.24.4", true},
		{"2.25.0", "2.24.4", true},
		{"v2.30.0", "2.24.4", true},
		{"2.24.3", "2.24.4", false},
		{"2.23.9", "2.24.4", false},
		{"2.24", "2.24.4", false},
	} {
		got, err := composeVersionAtLeast(testCase.version, testCase.minimum)
		if err != nil {
			t.Fatalf("composeVersionAtLeast(%q, %q) = %v", testCase.version, testCase.minimum, err)
		}
		if got != testCase.want {
			t.Fatalf("composeVersionAtLeast(%q, %q) = %v, want %v", testCase.version, testCase.minimum, got, testCase.want)
		}
	}
}

func TestCheckComposeVersion(t *testing.T) {
	if err := checkComposeVersion("v2.30.0"); err != nil {
		t.Fatalf("checkComposeVersion(v2.30.0) = %v", err)
	}
	if err := checkComposeVersion("v2.20.0"); err == nil || !strings.Contains(err.Error(), "2.24.4 or later is required") {
		t.Fatalf("checkComposeVersion(v2.20.0) = %v", err)
	}
	if err := checkComposeVersion("not-a-version"); err == nil || !strings.Contains(err.Error(), "unrecognized") {
		t.Fatalf("checkComposeVersion(not-a-version) = %v", err)
	}
}

func TestCheckComposeRejectsOldPlugin(t *testing.T) {
	runner := &fakeRunner{composeVersion: "v2.20.0"}
	err := (Service{Runner: runner}).checkCompose(context.Background())
	if err == nil || !strings.Contains(err.Error(), "2.24.4 or later is required") {
		t.Fatalf("checkCompose with v2.20.0 = %v", err)
	}
	if err := (Service{Runner: &fakeRunner{composeVersion: "v2.30.0"}}).checkCompose(context.Background()); err != nil {
		t.Fatalf("checkCompose with v2.30.0 = %v", err)
	}
}
