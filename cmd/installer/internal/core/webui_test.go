package core

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func installForWebUITest(t *testing.T, mutate func(*Options)) (*fakeRunner, Result, *envFile) {
	t.Helper()
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	options := DefaultOptions()
	options.InstallDir = installDir
	options.BundleDir = makeTestBundle(t, "v1")
	options.KVMPath = filepath.Join(root, "missing-kvm")
	if mutate != nil {
		mutate(&options)
	}
	runner := &fakeRunner{}
	result, err := Service{Runner: runner}.Apply(context.Background(), OperationInstall, options)
	if err != nil {
		t.Fatal(err)
	}
	return runner, result, readTestEnv(t, filepath.Join(installDir, ".env"))
}

func TestInstallOmitsWebUIProfileByDefault(t *testing.T) {
	_, result, env := installForWebUITest(t, nil)

	assertTestEnv(t, env, "COMPOSE_PROFILES", "")
	if result.WithUI() {
		t.Fatalf("default install enabled the web UI: %#v", result)
	}
	// Nothing listens on the published port, so a URL would be a dead address.
	if result.URL != "" {
		t.Fatalf("URL = %q, want empty without the web UI", result.URL)
	}
}

func TestInstallPersistsWebUIProfileWhenRequested(t *testing.T) {
	_, result, env := installForWebUITest(t, func(options *Options) {
		options.WithUI = true
		options.WithUISet = true
	})

	assertTestEnv(t, env, "COMPOSE_PROFILES", "with-ui")
	if !result.WithUI() {
		t.Fatalf("requested web UI was not recorded: %#v", result)
	}
	// Port 80 is implicit in an http:// URL.
	if !strings.HasPrefix(result.URL, "http://") || strings.HasSuffix(result.URL, ":80") {
		t.Fatalf("URL = %q, want a bare http:// host for port 80", result.URL)
	}
}

func TestUpgradeKeepsExistingProfileUnlessExplicitlySet(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	options := DefaultOptions()
	options.InstallDir = installDir
	options.BundleDir = makeTestBundle(t, "v1")
	options.KVMPath = filepath.Join(root, "missing-kvm")
	options.NoStart = true
	options.WithUI = true
	options.WithUISet = true
	service := Service{Runner: &fakeRunner{}}
	if _, err := service.Apply(context.Background(), OperationInstall, options); err != nil {
		t.Fatal(err)
	}

	// An upgrade that does not mention the UI must not silently remove it.
	options.BundleDir = makeTestBundle(t, "v2")
	options.WithUI = false
	options.WithUISet = false
	preserved, err := service.Apply(context.Background(), OperationUpgrade, options)
	if err != nil {
		t.Fatal(err)
	}
	assertTestEnv(t, readTestEnv(t, filepath.Join(installDir, ".env")), "COMPOSE_PROFILES", "with-ui")
	if !preserved.WithUI() {
		t.Fatalf("upgrade dropped the installed web UI: %#v", preserved)
	}

	options.WithUISet = true
	removed, err := service.Apply(context.Background(), OperationUpgrade, options)
	if err != nil {
		t.Fatal(err)
	}
	assertTestEnv(t, readTestEnv(t, filepath.Join(installDir, ".env")), "COMPOSE_PROFILES", "")
	if removed.WithUI() {
		t.Fatalf("explicit opt-out did not remove the web UI: %#v", removed)
	}
}

func TestInstallPullsGuestImageUnlessSkipped(t *testing.T) {
	guestPull := "docker pull registry.example/agent-compose-guest:v1"

	runner, result, _ := installForWebUITest(t, nil)
	if result.GuestImage != "registry.example/agent-compose-guest:v1" {
		t.Fatalf("guest image = %q", result.GuestImage)
	}
	if !slices.ContainsFunc(runner.calls, func(call string) bool { return strings.Contains(call, guestPull) }) {
		t.Fatalf("guest image was not pre-pulled: %#v", runner.calls)
	}

	skipped, _, _ := installForWebUITest(t, func(options *Options) { options.SkipGuestPull = true })
	if slices.ContainsFunc(skipped.calls, func(call string) bool { return strings.Contains(call, guestPull) }) {
		t.Fatalf("guest image was pulled despite the skip: %#v", skipped.calls)
	}
}

// A guest image is a deferred convenience: the deployment itself is healthy
// without it, so a pull failure must not roll the installation back.
func TestInstallSurvivesGuestImagePullFailure(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	options := DefaultOptions()
	options.InstallDir = installDir
	options.BundleDir = makeTestBundle(t, "v1")
	options.KVMPath = filepath.Join(root, "missing-kvm")

	runner := &fakeRunner{failOn: "docker pull registry.example/agent-compose-guest"}
	var warnings []string
	service := Service{Runner: runner, Reporter: ReporterFunc(func(event Event) {
		if event.Kind == EventWarning {
			warnings = append(warnings, event.Message)
		}
	})}
	if _, err := service.Apply(context.Background(), OperationInstall, options); err != nil {
		t.Fatalf("guest pull failure aborted the install: %v", err)
	}
	if !slices.ContainsFunc(runner.calls, func(call string) bool { return strings.Contains(call, "compose up -d") }) {
		t.Fatalf("deployment was not started: %#v", runner.calls)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "agent-compose-guest") {
		t.Fatalf("guest pull failure was not reported: %#v", warnings)
	}
}

// COMPOSE_PROFILES is a comma-separated list and an existing one is preserved
// verbatim, so an operator's own profile must not hide the installer's.
func TestResultWithUIAcceptsProfileLists(t *testing.T) {
	for _, testCase := range []struct {
		profiles string
		want     bool
	}{
		{"with-ui", true},
		{"with-ui,debug", true},
		{"debug,with-ui", true},
		{" with-ui , debug ", true},
		{"debug", false},
		{"", false},
		{"with-ui-extra", false},
	} {
		if got := (Result{ComposeProfiles: testCase.profiles}).WithUI(); got != testCase.want {
			t.Errorf("WithUI(%q) = %t, want %t", testCase.profiles, got, testCase.want)
		}
	}
}

func TestUpgradeKeepsWebUIAlongsideOtherProfiles(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	options := DefaultOptions()
	options.InstallDir = installDir
	options.BundleDir = makeTestBundle(t, "v1")
	options.KVMPath = filepath.Join(root, "missing-kvm")
	options.NoStart = true
	options.WithUI = true
	options.WithUISet = true
	service := Service{Runner: &fakeRunner{}}
	if _, err := service.Apply(context.Background(), OperationInstall, options); err != nil {
		t.Fatal(err)
	}

	envPath := filepath.Join(installDir, ".env")
	env := readTestEnv(t, envPath)
	if err := env.Set("COMPOSE_PROFILES", "with-ui,debug"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, env.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	options.BundleDir = makeTestBundle(t, "v2")
	options.WithUISet = false
	upgraded, err := service.Apply(context.Background(), OperationUpgrade, options)
	if err != nil {
		t.Fatal(err)
	}
	assertTestEnv(t, readTestEnv(t, envPath), "COMPOSE_PROFILES", "with-ui,debug")
	if !upgraded.WithUI() {
		t.Fatalf("hand-added profile hid the web UI: %#v", upgraded)
	}
	if upgraded.URL == "" {
		t.Fatal("no URL reported for an installation that serves the web UI")
	}
}
