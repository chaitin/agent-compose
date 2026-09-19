package compose

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/sources"
)

func TestScriptGitLocalPath(t *testing.T) {
	for _, tc := range []struct {
		location string
		path     string
		local    bool
		invalid  bool
	}{
		{location: "/srv/repo", path: "/srv/repo", local: true},
		{location: "repo.git", path: "repo.git", local: true},
		{location: "./repo", path: "./repo", local: true},
		{location: "../repo", path: "../repo", local: true},
		{location: "nested/repo:one", path: "nested/repo:one", local: true},
		{location: "file:///srv/repo%20one", path: "/srv/repo one", local: true},
		{location: "file://localhost/srv/repo", path: "/srv/repo", local: true},
		{location: "file:///srv/%252e%252e", path: "/srv/%2e%2e", local: true},
		{location: "file:/srv/repo", path: "/srv/repo", local: true},
		{location: "file://remote/srv/repo", invalid: true},
		{location: "file://user@localhost/srv/repo", invalid: true},
		{location: "file:repo", invalid: true},
		{location: "file:///srv/repo?ref=main", invalid: true},
		{location: "file:///srv/repo?", invalid: true},
		{location: "file:///srv/repo#main", invalid: true},
		{location: "file:///srv/%zz", invalid: true},
		{location: "https://example.test/repo.git"},
		{location: "ssh://example.test/repo.git"},
		{location: "git://example.test/repo.git"},
		{location: "git@example.test:repo.git"},
		{location: "example.test:repo.git"},
	} {
		t.Run(tc.location, func(t *testing.T) {
			path, local, err := scriptGitLocalPath(tc.location)
			if (err != nil) != tc.invalid || path != tc.path || local != tc.local {
				t.Fatalf("local path = %q, local = %v, error = %v", path, local, err)
			}
		})
	}
}

func TestNormalizeDaemonGitScriptPreservesRemoteTransports(t *testing.T) {
	for _, location := range []string{
		"https://example.test/repo.git", "http://127.0.0.1/repo.git", "http://10.0.0.1/repo.git",
		"ssh://example.test/repo.git", "git://example.test/repo.git", "git@example.test:repo.git",
	} {
		t.Run(location, func(t *testing.T) {
			source, err := normalizeSchedulerScriptSource("scheduler.script", sources.Source{
				Provider: sources.ProviderGit, URL: location, Path: "scheduler.js",
			}, NormalizeOptions{ScriptSourceBoundary: ScriptSourceBoundaryDaemon})
			if err != nil || source.URL != location {
				t.Fatalf("normalized source = %#v, error = %v", source, err)
			}
		})
	}
}
