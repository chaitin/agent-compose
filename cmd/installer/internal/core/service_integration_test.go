package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls    []string
	failOn   string
	failures []string
}

func (r *fakeRunner) Run(_ context.Context, dir, name string, args ...string) error {
	call := strings.TrimSpace(dir + "|" + name + " " + strings.Join(args, " "))
	r.calls = append(r.calls, call)
	if r.failOn != "" && strings.Contains(call, r.failOn) {
		return errors.New("injected command failure")
	}
	for _, failure := range r.failures {
		if strings.Contains(call, failure) {
			return errors.New("injected command failure")
		}
	}
	return nil
}

func TestIntegrationInstallUpgradePreservesUserConfiguration(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	missingKVM := filepath.Join(root, "missing-kvm")
	installerPath := filepath.Join(root, "installer")
	writeTestFile(t, installerPath, "binary", 0o755)
	bundleV1 := makeTestBundle(t, "v1")
	runner := &fakeRunner{}
	service := Service{Runner: runner}
	options := DefaultOptions()
	options.InstallDir = installDir
	options.BundleDir = bundleV1
	options.KVMPath = missingKVM
	options.InstallerPath = installerPath
	options.NoStart = true

	result, err := service.Apply(context.Background(), OperationInstall, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.ComposeFiles != "docker-compose.yml" || result.GeneratedPassword == "" {
		t.Fatalf("result = %#v", result)
	}
	envPath := filepath.Join(installDir, ".env")
	env := readTestEnv(t, envPath)
	assertTestEnv(t, env, "AGENT_COMPOSE_IMAGE", "registry.example/agent-compose:v1")
	assertTestEnv(t, env, "AGENT_COMPOSE_DATA_DIR", "./data")
	assertTestEnv(t, env, "AGENT_COMPOSE_HTTP_PORT", "80")
	if err := env.Set("AGENT_COMPOSE_IMAGE", "custom.example/daemon:keep"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, env.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	options.BundleDir = makeTestBundle(t, "v2")
	if _, err := service.Apply(context.Background(), OperationUpgrade, options); err != nil {
		t.Fatal(err)
	}
	upgraded := readTestEnv(t, envPath)
	assertTestEnv(t, upgraded, "AGENT_COMPOSE_IMAGE", "custom.example/daemon:keep")
	assertTestEnv(t, upgraded, "DEFAULT_IMAGE", "registry.example/agent-compose-guest:v2")
	state := readTestEnv(t, filepath.Join(installDir, ".installer-state.env"))
	assertTestEnv(t, state, "AGENT_COMPOSE_IMAGE", "registry.example/agent-compose:v1")
	assertTestEnv(t, state, "DEFAULT_IMAGE", "registry.example/agent-compose-guest:v2")
	if !slices.Contains(runner.calls, installDir+"|docker compose config --quiet") {
		t.Fatalf("compose config call missing: %#v", runner.calls)
	}
}

func TestIntegrationUpgradePreservesPortUnlessExplicitlySet(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	options := DefaultOptions()
	options.InstallDir = installDir
	options.BundleDir = makeTestBundle(t, "v1")
	options.KVMPath = filepath.Join(root, "missing-kvm")
	options.NoStart = true
	// A URL is only reported when the frontend publishes the port.
	options.WithUI = true
	options.WithUISet = true
	service := Service{Runner: &fakeRunner{}}

	if _, err := service.Apply(context.Background(), OperationInstall, options); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(installDir, ".env")
	env := readTestEnv(t, envPath)
	if err := env.Set("AGENT_COMPOSE_HTTP_PORT", "8080"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, env.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	options.BundleDir = makeTestBundle(t, "v2")
	preserved, err := service.Apply(context.Background(), OperationUpgrade, options)
	if err != nil {
		t.Fatal(err)
	}
	assertTestEnv(t, readTestEnv(t, envPath), "AGENT_COMPOSE_HTTP_PORT", "8080")
	if !strings.HasSuffix(preserved.URL, ":8080") {
		t.Fatalf("preserved URL = %q", preserved.URL)
	}

	options.Port = 9090
	options.PortSet = true
	overridden, err := service.Apply(context.Background(), OperationUpgrade, options)
	if err != nil {
		t.Fatal(err)
	}
	assertTestEnv(t, readTestEnv(t, envPath), "AGENT_COMPOSE_HTTP_PORT", "9090")
	if !strings.HasSuffix(overridden.URL, ":9090") {
		t.Fatalf("overridden URL = %q", overridden.URL)
	}
}

func TestIntegrationInstallRollbackRestoresManagedFiles(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	writeTestFile(t, filepath.Join(installDir, "docker-compose.yml"), "old-compose\n", 0o640)
	writeTestFile(t, filepath.Join(installDir, ".env"), "AUTH_PASSWORD=old-password\nAUTH_SECRET=old-secret\nCOMPOSE_FILE=docker-compose.yml\nAGENT_COMPOSE_DATA_DIR=./data\n", 0o600)
	beforeCompose, err := os.ReadFile(filepath.Join(installDir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{failOn: "docker compose pull"}
	options := DefaultOptions()
	options.InstallDir = installDir
	options.BundleDir = makeTestBundle(t, "v2")
	options.KVMPath = filepath.Join(root, "missing-kvm")
	service := Service{Runner: runner}
	if _, err := service.Apply(context.Background(), OperationUpgrade, options); err == nil {
		t.Fatal("expected pull failure")
	}
	afterCompose, err := os.ReadFile(filepath.Join(installDir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterCompose) != string(beforeCompose) {
		t.Fatalf("compose file not restored: %q", afterCompose)
	}
	info, err := os.Stat(filepath.Join(installDir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("restored mode = %o", info.Mode().Perm())
	}
}

func TestIntegrationFailedInitialStartRetainsFilesWhenCleanupFails(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	runner := &fakeRunner{failures: []string{
		"docker compose up -d",
		"docker compose down --remove-orphans",
	}}
	options := DefaultOptions()
	options.InstallDir = installDir
	options.BundleDir = makeTestBundle(t, "v1")
	options.KVMPath = filepath.Join(root, "missing-kvm")

	_, err := (Service{Runner: runner}).Apply(context.Background(), OperationInstall, options)
	if err == nil || !strings.Contains(err.Error(), "cleanup incomplete") || !strings.Contains(err.Error(), installDir) {
		t.Fatalf("install error = %v", err)
	}
	for _, name := range []string{"docker-compose.yml", ".env", ".installer-state.env"} {
		if !regularFile(filepath.Join(installDir, name)) {
			t.Fatalf("recovery file %s was removed", name)
		}
	}
	if info, statErr := os.Stat(filepath.Join(installDir, "data")); statErr != nil || !info.IsDir() {
		t.Fatalf("candidate data directory was removed: %v", statErr)
	}
}

func TestIntegrationFailedInitialStartRollsBackAfterCleanup(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	runner := &fakeRunner{failOn: "docker compose up -d"}
	options := DefaultOptions()
	options.InstallDir = installDir
	options.BundleDir = makeTestBundle(t, "v1")
	options.KVMPath = filepath.Join(root, "missing-kvm")

	if _, err := (Service{Runner: runner}).Apply(context.Background(), OperationInstall, options); err == nil {
		t.Fatal("expected start failure")
	}
	if _, err := os.Stat(installDir); !os.IsNotExist(err) {
		t.Fatalf("failed installation was not rolled back: %v", err)
	}
	wantDown := installDir + "|docker compose down --remove-orphans"
	if !slices.Contains(runner.calls, wantDown) {
		t.Fatalf("cleanup command missing: %#v", runner.calls)
	}
}

func TestIntegrationLegacyDataAndAmbiguousData(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "legacy")
	writeTestFile(t, filepath.Join(installDir, "data", "agent-compose", "data.db"), "legacy", 0o600)
	options := DefaultOptions()
	options.InstallDir = installDir
	options.BundleDir = makeTestBundle(t, "v1")
	options.KVMPath = filepath.Join(root, "missing-kvm")
	options.NoStart = true
	service := Service{Runner: &fakeRunner{}}
	result, err := service.Apply(context.Background(), OperationInstall, options)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(result.DataDir, filepath.Join("data", "agent-compose")) {
		t.Fatalf("data dir = %s", result.DataDir)
	}

	ambiguous := filepath.Join(root, "ambiguous")
	writeTestFile(t, filepath.Join(ambiguous, "data", "data.db"), "current", 0o600)
	writeTestFile(t, filepath.Join(ambiguous, "data", "agent-compose", "data.db"), "legacy", 0o600)
	options.InstallDir = ambiguous
	if _, err := service.Apply(context.Background(), OperationInstall, options); err == nil || !strings.Contains(err.Error(), "both current and legacy") {
		t.Fatalf("ambiguous data error = %v", err)
	}
}

func TestIntegrationUninstallPreservesAndPurgesData(t *testing.T) {
	for _, purge := range []bool{false, true} {
		t.Run(fmt.Sprintf("purge=%t", purge), func(t *testing.T) {
			installDir := filepath.Join(t.TempDir(), "install")
			writeTestFile(t, filepath.Join(installDir, "docker-compose.yml"), "services: {}\n", 0o644)
			writeTestFile(t, filepath.Join(installDir, "docker-compose.kvm.yml"), "services: {}\n", 0o644)
			writeTestFile(t, filepath.Join(installDir, ".env"), "COMPOSE_FILE=docker-compose.yml\n", 0o600)
			writeTestFile(t, filepath.Join(installDir, ".installer-state.env"), "INSTALLER_PAYLOAD_VERSION=1\n", 0o600)
			writeTestFile(t, filepath.Join(installDir, "installer"), "binary", 0o755)
			writeTestFile(t, filepath.Join(installDir, "data", "data.db"), "data", 0o600)
			writeTestFile(t, filepath.Join(installDir, "user-note"), "keep", 0o644)
			options := DefaultOptions()
			options.InstallDir = installDir
			options.Purge = purge
			runner := &fakeRunner{}
			result, err := (Service{Runner: runner}).Apply(context.Background(), OperationUninstall, options)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(result.RetainedFiles, "user-note") {
				t.Fatalf("retained files = %#v", result.RetainedFiles)
			}
			if regularFile(filepath.Join(installDir, "docker-compose.yml")) || regularFile(filepath.Join(installDir, "installer")) {
				t.Fatal("managed files survived uninstall")
			}
			_, envErr := os.Stat(filepath.Join(installDir, ".env"))
			_, dataErr := os.Stat(filepath.Join(installDir, "data", "data.db"))
			if purge && (!os.IsNotExist(envErr) || !os.IsNotExist(dataErr)) {
				t.Fatalf("purge left env/data: %v %v", envErr, dataErr)
			}
			if !purge && (envErr != nil || dataErr != nil) {
				t.Fatalf("ordinary uninstall removed env/data: %v %v", envErr, dataErr)
			}
			if !regularFile(filepath.Join(installDir, "user-note")) {
				t.Fatal("uninstall removed unknown file")
			}
		})
	}
}

func makeTestBundle(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "docker-compose.yml"), "services:\n  agent-compose:\n    image: ${AGENT_COMPOSE_IMAGE}\n", 0o644)
	writeTestFile(t, filepath.Join(dir, "docker-compose.kvm.yml"), "services:\n  agent-compose:\n    privileged: true\n", 0o644)
	writeTestFile(t, filepath.Join(dir, ".env.example"), "AUTH_USERNAME=admin\nAUTH_PASSWORD=\nAUTH_SECRET=\nAGENT_COMPOSE_DATA_DIR=./data\n", 0o644)
	manifest := "INSTALLER_PAYLOAD_VERSION=1\n" +
		"AGENT_COMPOSE_IMAGE=registry.example/agent-compose:" + version + "\n" +
		"AGENT_COMPOSE_FRONTEND_VERSION=latest\n" +
		"AGENT_COMPOSE_FRONTEND_IMAGE=registry.example/agent-compose-ui:latest\n" +
		"DEFAULT_IMAGE=registry.example/agent-compose-guest:" + version + "\n"
	writeTestFile(t, filepath.Join(dir, "images", "manifest.env"), manifest, 0o644)
	return dir
}

func readTestEnv(t *testing.T, path string) *envFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return parseEnvFile(data)
}

func assertTestEnv(t *testing.T, env *envFile, key, want string) {
	t.Helper()
	got, ok := env.Get(key)
	if !ok || got != want {
		t.Fatalf("%s = %q, %v; want %q", key, got, ok, want)
	}
}
