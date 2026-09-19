package compose

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/sources"
)

func TestIntegrationNormalizeDaemonGitScriptSourceRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	repository := filepath.Join(root, "repo with spaces")
	outside := filepath.Join(base, "project-other")
	for _, dir := range []string{repository, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"inside-link": repository, "outside-link": outside} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	rootLink := filepath.Join(base, "project-link")
	if err := os.Symlink(root, rootLink); err != nil {
		t.Fatal(err)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		location   string
		compose    string
		projectDir string
		wantError  string
	}{
		{name: "absolute", location: repository, compose: filepath.Join(root, "compose.yml")},
		{name: "file URL", location: (&url.URL{Scheme: "file", Path: repository}).String(), projectDir: root},
		{name: "relative", location: "repo with spaces", projectDir: root},
		{name: "dot relative", location: "./repo with spaces", projectDir: root},
		{name: "inside symlink", location: "inside-link", projectDir: root},
		{name: "root symlink", location: "repo with spaces", projectDir: rootLink},
		{name: "directory source", location: "repo with spaces", compose: root},
		{name: "outside absolute", location: outside, projectDir: root, wantError: "within the project source directory"},
		{name: "outside file URL", location: (&url.URL{Scheme: "file", Path: outside}).String(), projectDir: root, wantError: "within the project source directory"},
		{name: "traversal", location: "../project-other", projectDir: root, wantError: "within the project source directory"},
		{name: "escaping symlink", location: "outside-link", projectDir: root, wantError: "within the project source directory"},
		{name: "missing source", location: repository, wantError: "project source directory is required"},
		{name: "missing source directory", location: repository, projectDir: filepath.Join(base, "missing"), wantError: "project source directory"},
		{name: "relative source directory", location: repository, projectDir: "project", wantError: "absolute"},
		{name: "compose path takes precedence", location: outside, compose: filepath.Join(root, "compose.yml"), projectDir: outside, wantError: "within the project source directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := mustParseCompose(t, "name: local-git\nagents:\n  worker:\n    scheduler:\n      script:\n        provider: git\n        url: placeholder\n        path: scheduler.js\n")
			spec.Agents["worker"].Scheduler.Script.Source.URL = tc.location
			resolved := false
			normalized, err := Normalize(spec, NormalizeOptions{
				ComposePath: tc.compose, ProjectDir: tc.projectDir,
				ScriptSourceBoundary: ScriptSourceBoundaryDaemon, ResolveScriptURLs: true, Context: t.Context(),
				ScriptSourceResolver: ScriptSourceResolverFunc(func(_ context.Context, source sources.Source) ([]byte, error) {
					resolved = true
					if source.URL != canonicalRepository {
						t.Errorf("resolved URL = %q, want %q", source.URL, canonicalRepository)
					}
					return []byte("scheduler.agent('review');"), nil
				}),
			})
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || resolved {
					t.Fatalf("normalization error = %v, source fetched = %v", err, resolved)
				}
				return
			}
			if err != nil || !resolved || normalized.Agents[0].Scheduler.Script != "scheduler.agent('review');" {
				t.Fatalf("normalize: %v, source fetched = %v", err, resolved)
			}
			if spec.Agents["worker"].Scheduler.Script.Source.URL != tc.location {
				t.Fatal("normalization mutated the caller's source")
			}
		})
	}
}
