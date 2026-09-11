package chat

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestModuleVersion(t *testing.T) {
	sdk := func(version string, replace *debug.Module) *debug.Module {
		return &debug.Module{Path: modulePath, Version: version, Replace: replace}
	}
	other := &debug.Module{Path: "example.com/other", Version: "v1.2.3"}

	for _, tc := range []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{name: "no build info", info: nil, want: develVersion},
		{
			name: "tagged dependency",
			info: &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}, Deps: []*debug.Module{other, sdk("v0.1.0", nil)}},
			want: "v0.1.0",
		},
		{
			name: "pseudo-version dependency",
			info: &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}, Deps: []*debug.Module{sdk("v0.0.0-20260911074743-142bf085afdd", nil)}},
			want: "v0.0.0-20260911074743-142bf085afdd",
		},
		{
			name: "replaced by a directory",
			info: &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}, Deps: []*debug.Module{sdk("v0.1.0", &debug.Module{Path: "../sdk/go"})}},
			want: develVersion,
		},
		{
			name: "replaced by another version",
			info: &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}, Deps: []*debug.Module{sdk("v0.1.0", &debug.Module{Path: modulePath, Version: "v0.1.1"})}},
			want: "v0.1.1",
		},
		{
			name: "SDK is the main module",
			info: &debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "(devel)"}},
			want: develVersion,
		},
		{
			name: "SDK is the main module with a stamped version",
			info: &debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "v0.2.0"}},
			want: "v0.2.0",
		},
		{
			name: "SDK absent",
			info: &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}, Deps: []*debug.Module{other}},
			want: develVersion,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := moduleVersion(tc.info); got != tc.want {
				t.Fatalf("moduleVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUserAgentNamesTheSDK(t *testing.T) {
	agent := userAgent()
	version, ok := strings.CutPrefix(agent, "agent-compose-chat-go/")
	if !ok || version == "" {
		t.Fatalf("userAgent() = %q, want agent-compose-chat-go/<version>", agent)
	}
}
