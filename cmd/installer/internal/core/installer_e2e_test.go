package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EInstallerLifecycle(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "agent-compose")
	installerPath := filepath.Join(root, "downloaded-installer")
	writeTestFile(t, installerPath, "installer-binary", 0o755)

	runner := &fakeRunner{}
	var events []string
	service := Service{
		Runner: runner,
		Reporter: ReporterFunc(func(event Event) {
			events = append(events, event.Message)
		}),
	}
	options := DefaultOptions()
	options.InstallDir = installDir
	options.BundleDir = makeTestBundle(t, "v1")
	options.KVMPath = filepath.Join(root, "missing-kvm")
	options.InstallerPath = installerPath

	installed, err := service.Apply(context.Background(), OperationInstall, options)
	if err != nil {
		t.Fatal(err)
	}
	if installed.GeneratedPassword == "" || installed.ComposeFiles != "docker-compose.yml" {
		t.Fatalf("install result = %#v", installed)
	}
	if !regularFile(filepath.Join(installDir, "installer")) || !regularFile(filepath.Join(installDir, ".env")) {
		t.Fatal("installation did not persist installer and environment")
	}
	if strings.Join(runner.calls, "\n") != strings.Join([]string{
		"|docker version --format {{.Server.Version}}",
		"|docker compose version --short",
		installDir + "|docker compose config --quiet",
		installDir + "|docker compose pull",
		installDir + "|docker pull registry.example/agent-compose-guest:v1",
		installDir + "|docker compose up -d",
	}, "\n") {
		t.Fatalf("install calls:\n%s", strings.Join(runner.calls, "\n"))
	}

	options.BundleDir = makeTestBundle(t, "v2")
	upgraded, err := service.Apply(context.Background(), OperationUpgrade, options)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.GeneratedPassword != "" {
		t.Fatal("upgrade unexpectedly regenerated credentials")
	}
	assertTestEnv(t, readTestEnv(t, filepath.Join(installDir, ".env")), "AGENT_COMPOSE_IMAGE", "registry.example/agent-compose:v2")

	writeTestFile(t, filepath.Join(installDir, "operator-note"), "keep", 0o644)
	uninstalled, err := service.Apply(context.Background(), OperationUninstall, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(uninstalled.RetainedFiles) != 1 || uninstalled.RetainedFiles[0] != "operator-note" {
		t.Fatalf("retained files = %#v", uninstalled.RetainedFiles)
	}
	if _, err := os.Stat(filepath.Join(installDir, ".env")); err != nil {
		t.Fatalf("ordinary uninstall removed environment: %v", err)
	}
	if regularFile(filepath.Join(installDir, "docker-compose.yml")) || regularFile(filepath.Join(installDir, "installer")) {
		t.Fatal("ordinary uninstall retained managed files")
	}
	if len(events) == 0 {
		t.Fatal("installer emitted no progress events")
	}
}
