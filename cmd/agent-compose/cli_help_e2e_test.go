package main

import (
	"strings"
	"testing"
)

func TestE2ECLIHelpCoversUserWorkflowCommandSurface(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "root",
			args: []string{"--help"},
			want: []string{"agent-compose daemon and CLI", "--host", "--file", "--project-name", "--json", "Available Commands"},
		},
		{
			name: "config",
			args: []string{"config", "--help"},
			want: []string{"Validate and print normalized compose config", "--quiet"},
		},
		{
			name: "list projects",
			args: []string{"project", "ls", "--help"},
			want: []string{"List daemon projects", "--verbose", "--limit", "--offset"},
		},
		{
			name: "list agents",
			args: []string{"agent", "ls", "--help"},
			want: []string{"List current project agents"},
		},
		{
			name: "run",
			args: []string{"run", "--help"},
			want: []string{"Run a project agent", "--prompt", "--command", "--sandbox", "--driver", "--keep-running", "--rm", "--jupyter", "--jupyter-expose", "--detach", "--interactive"},
		},
		{
			name: "scheduler",
			args: []string{"scheduler", "--help"},
			want: []string{"Run, inspect, and operate project schedulers, runs, logs, and triggers", "run", "trigger", "runs", "logs", "stop", "inspect"},
		},
		{
			name: "scheduler run",
			args: []string{"scheduler", "run", "--help"},
			want: []string{"Run a scheduler main function", "--payload", "--detach", "--prompt", "--sandbox"},
		},
		{
			name: "scheduler trigger",
			args: []string{"scheduler", "trigger", "--help"},
			want: []string{"Manually run a scheduler trigger", "--sandbox", "--driver", "--prompt", "--payload", "--keep-running", "--rm", "--jupyter", "--jupyter-expose", "--detach"},
		},
		{
			name: "logs",
			args: []string{"logs", "--help"},
			want: []string{"Print project run logs", "--agent", "--run", "--sandbox", "--follow", "--tail", "--timestamp"},
		},
		{
			name: "ps",
			args: []string{"ps", "--help"},
			want: []string{"List project sandboxes", "--all", "--status", "--verbose"},
		},
		{
			name: "sandbox prune",
			args: []string{"sandbox", "prune", "--help"},
			want: []string{"Prune stopped or failed sandboxes", "--status", "--agent", "--driver", "--older-than", "--include-orphans", "--force"},
		},
		{
			name: "exec",
			args: []string{"exec", "--help"},
			want: []string{"Execute a command in a running sandbox", "--run", "--command", "--prompt", "--interactive", "--tty", "--cwd"},
		},
		{
			name: "images",
			args: []string{"images", "--help"},
			want: []string{"List daemon images", "--query", "--all"},
		},
		{
			name: "cache ls",
			args: []string{"cache", "ls", "--help"},
			want: []string{"List daemon runtime caches", "--driver", "--type", "--status"},
		},
		{
			name: "cache prune",
			args: []string{"cache", "prune", "--help"},
			want: []string{"Prune daemon runtime caches", "--driver", "--type", "--status", "--unused", "--orphaned", "--expired", "--older-than", "--force"},
		},
		{
			name: "cache rm",
			args: []string{"cache", "rm", "--help"},
			want: []string{"Remove a daemon runtime cache item", "--force"},
		},
		{
			name: "pull",
			args: []string{"pull", "--help"},
			want: []string{"Pull an image or all project images", "--platform"},
		},
		{
			name: "build",
			args: []string{"build", "--help"},
			want: []string{"Build project agent images", "--tag", "--dockerfile", "--target", "--build-arg", "--platform", "--no-cache", "--pull"},
		},
		{
			name: "rmi",
			args: []string{"rmi", "--help"},
			want: []string{"Remove an image", "--force", "--prune-children"},
		},
		{
			name: "image command",
			args: []string{"image", "--help"},
			want: []string{"Manage daemon images", "ls", "pull", "build", "rm", "inspect"},
		},
		{
			name: "image pull",
			args: []string{"image", "pull", "--help"},
			want: []string{"Pull an image or all project images", "pull [image]", "--platform"},
		},
		{
			name: "inspect",
			args: []string{"inspect", "--help"},
			want: []string{"Inspect project, agent, run, sandbox, image, cache, or volume details"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, runCount, exitCode := executeCLICommand(tc.args...)
			if exitCode != 0 {
				t.Fatalf("%v exit code = %d, stderr=%q", tc.args, exitCode, stderr)
			}
			if runCount != 0 {
				t.Fatalf("%v started daemon %d time(s)", tc.args, runCount)
			}
			if stderr != "" {
				t.Fatalf("%v stderr = %q, want empty", tc.args, stderr)
			}
			for _, want := range tc.want {
				if !strings.Contains(stdout, want) {
					t.Fatalf("%v help output does not contain %q:\n%s", tc.args, want, stdout)
				}
			}
		})
	}
}

func TestE2ECLILegacyRunIDFlagIsRejected(t *testing.T) {
	for _, args := range [][]string{
		{"logs", "--run-id", "run-1"},
		{"exec", "--run-id", "run-1", "--command", "true"},
	} {
		stdout, stderr, runCount, exitCode := executeCLICommand(args...)
		if exitCode != exitCodeUsage || stdout != "" || runCount != 0 || !strings.Contains(stderr, "unknown flag: --run-id") {
			t.Fatalf("%v code/stdout/stderr/runCount = %d / %q / %q / %d", args, exitCode, stdout, stderr, runCount)
		}
	}
}
