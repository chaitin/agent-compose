package sources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitClientResolveAndCheckout(t *testing.T) {
	repository := t.TempDir()
	runTestGit(t, "", "init", "-b", "main", repository)
	runTestGit(t, repository, "config", "user.email", "agent-compose@example.test")
	runTestGit(t, repository, "config", "user.name", "Agent Compose")
	if err := os.WriteFile(filepath.Join(repository, "script.js"), []byte("scheduler.agent('review');\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repository, "add", "script.js")
	runTestGit(t, repository, "commit", "-m", "add script")
	wantCommit := strings.TrimSpace(runTestGit(t, repository, "rev-parse", "HEAD"))

	source := Source{Provider: ProviderGit, URL: repository, Ref: "main"}
	client := GitClient{}
	resolved, err := client.Resolve(context.Background(), source)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.Commit != wantCommit {
		t.Fatalf("commit = %q, want %q", resolved.Commit, wantCommit)
	}
	destination := filepath.Join(t.TempDir(), "checkout")
	if err := client.CheckoutCommit(context.Background(), source, resolved.Commit, destination); err != nil {
		t.Fatalf("CheckoutCommit returned error: %v", err)
	}
	assertGitRepositoryShallow(t, destination, true)
	data, err := os.ReadFile(filepath.Join(destination, "script.js"))
	if err != nil || !strings.Contains(string(data), "scheduler.agent") {
		t.Fatalf("checked out script = %q, err=%v", data, err)
	}
}

func TestGitClientResolvesBranchTagAndCommit(t *testing.T) {
	repository := t.TempDir()
	runTestGit(t, "", "init", "-b", "main", repository)
	runTestGit(t, repository, "config", "user.email", "agent-compose@example.test")
	runTestGit(t, repository, "config", "user.name", "Agent Compose")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repository, "add", "README.md")
	runTestGit(t, repository, "commit", "-m", "first")
	firstCommit := strings.TrimSpace(runTestGit(t, repository, "rev-parse", "HEAD"))
	runTestGit(t, repository, "tag", "-a", "v1.0.0", "-m", "release")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repository, "commit", "-am", "second")
	branchCommit := strings.TrimSpace(runTestGit(t, repository, "rev-parse", "HEAD"))

	client := GitClient{}
	for _, test := range []struct {
		name        string
		ref         string
		want        string
		wantContent string
	}{
		{name: "branch", ref: "main", want: branchCommit, wantContent: "two\n"},
		{name: "annotated tag", ref: "v1.0.0", want: firstCommit, wantContent: "one\n"},
		{name: "commit", ref: firstCommit, want: firstCommit, wantContent: "one\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := Source{Provider: ProviderGit, URL: repository, Ref: test.ref}
			resolved, err := client.Resolve(context.Background(), source)
			if err != nil {
				t.Fatalf("Resolve returned error: %v", err)
			}
			if resolved.Commit != test.want {
				t.Fatalf("commit = %q, want %q", resolved.Commit, test.want)
			}
			destination := filepath.Join(t.TempDir(), "checkout")
			checkedOut, err := client.Checkout(context.Background(), source, destination)
			if err != nil {
				t.Fatalf("Checkout returned error: %v", err)
			}
			if checkedOut.Commit != test.want {
				t.Fatalf("checked out commit = %q, want %q", checkedOut.Commit, test.want)
			}
			assertGitRepositoryShallow(t, destination, true)
			content, err := os.ReadFile(filepath.Join(destination, "README.md"))
			if err != nil || string(content) != test.wantContent {
				t.Fatalf("checked out README.md = %q, err=%v", content, err)
			}
		})
	}

	t.Run("abbreviated commit full-fetch fallback", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "checkout")
		if err := client.CheckoutCommit(context.Background(), Source{Provider: ProviderGit, URL: repository}, firstCommit[:8], destination); err != nil {
			t.Fatalf("CheckoutCommit returned error: %v", err)
		}
		assertGitRepositoryShallow(t, destination, false)
		content, err := os.ReadFile(filepath.Join(destination, "README.md"))
		if err != nil || string(content) != "one\n" {
			t.Fatalf("fallback README.md = %q, err=%v", content, err)
		}
	})
	if _, err := client.Resolve(context.Background(), Source{Provider: ProviderGit, URL: repository, Ref: "missing"}); err == nil {
		t.Fatal("Resolve missing ref returned nil error")
	}
}

func TestGitClientRejectsUnsafeOperandsAndSchemes(t *testing.T) {
	client := GitClient{}
	for _, source := range []Source{
		{Provider: ProviderGit, URL: "--upload-pack=bad"},
		{Provider: ProviderGit, URL: "ext::sh -c id"},
		{Provider: ProviderGit, URL: "ftp://example.invalid/repo.git"},
		{Provider: ProviderGit, URL: "https://user:secret@example.invalid/repo.git"},
		{Provider: ProviderGit, URL: "https://example.invalid/repo.git", Ref: "--bad"},
	} {
		if _, err := client.Resolve(context.Background(), source); err == nil {
			t.Fatalf("Resolve(%#v) returned nil error", source)
		}
	}
}

func TestGitClientInjectsCredentialsOutsideArguments(t *testing.T) {
	const secret = "super-secret-token"
	client := GitClient{Env: map[string]string{"TOKEN": secret}}
	source := Source{Provider: ProviderGit, URL: "https://example.invalid/repo.git", Token: "${TOKEN}"}
	cmd := client.command(context.Background(), "", source, "ls-remote", "--", source.URL, "HEAD")
	if strings.Contains(strings.Join(cmd.Args, " "), secret) {
		t.Fatalf("git arguments contain credential: %#v", cmd.Args)
	}
	joinedEnv := strings.Join(cmd.Env, "\n")
	if !strings.Contains(joinedEnv, "GIT_CONFIG_VALUE_0=Authorization: Basic ") || strings.Contains(joinedEnv, source.Token) {
		t.Fatalf("git credential environment is not resolved safely")
	}
}

func TestGitClientRedactsCredentialsFromErrors(t *testing.T) {
	const secret = "super-secret-token"
	client := GitClient{Env: map[string]string{"TOKEN": secret}}
	source := Source{Provider: ProviderGit, URL: "https://example.invalid/repo.git", Token: "${TOKEN}"}
	err := client.commandError(
		source,
		[]string{"clone", "https://user:" + secret + "@example.invalid/repo.git"},
		[]byte("fatal: authentication failed for https://user:"+secret+"@example.invalid/repo.git"),
		errors.New("exit status 128"),
	)
	message := err.Error()
	if strings.Contains(message, secret) || strings.Contains(message, "user:") || !strings.Contains(message, "xxxxx") {
		t.Fatalf("redacted error = %q", message)
	}
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func assertGitRepositoryShallow(t *testing.T, repository string, want bool) {
	t.Helper()
	got := strings.TrimSpace(runTestGit(t, repository, "rev-parse", "--is-shallow-repository"))
	if got != fmt.Sprint(want) {
		t.Fatalf("git repository shallow = %q, want %t", got, want)
	}
}
