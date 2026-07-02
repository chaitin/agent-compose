package agentcompose

import (
	"testing"
)

func TestNormalizeImagePullPolicy(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"always", "always"},
		{"ALWAYS", "always"},
		{"Always", "always"},
		{"missing", "missing"},
		{"Missing", "missing"},
		{"MISSING", "missing"},
		{"never", "never"},
		{"NEVER", "never"},
		{"Never", "never"},
		{"", ""},
		{"invalid", ""},
		{"  always  ", "always"},
		{"  never  ", "never"},
	}
	for _, tc := range cases {
		got := normalizeImagePullPolicy(tc.input)
		if got != tc.want {
			t.Errorf("normalizeImagePullPolicy(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
