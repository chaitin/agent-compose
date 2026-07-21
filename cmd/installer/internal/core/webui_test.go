package core

import (
	"context"
	"path/filepath"
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
