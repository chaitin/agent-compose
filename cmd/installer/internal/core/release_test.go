package core

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReplaceImageRegistryPreservesRepositoryAndIdentifier(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		reference string
		registry  string
		want      string
	}{
		{name: "implicit Docker Hub", reference: "chaitin/agent-compose:v1", registry: "registry.example.com", want: "registry.example.com/chaitin/agent-compose:v1"},
		{name: "explicit Docker Hub", reference: "docker.io/chaitin/agent-compose:v1", registry: "registry.example.com:5000", want: "registry.example.com:5000/chaitin/agent-compose:v1"},
		{name: "other registry digest", reference: "old.example/team/guest@sha256:abc", registry: "new.example", want: "new.example/team/guest@sha256:abc"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := replaceImageRegistry(testCase.reference, testCase.registry); got != testCase.want {
				t.Fatalf("replaceImageRegistry() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestPlanImageReferencesManagesSandboxHostImageIndependently(t *testing.T) {
	makeManifest := func(daemonVersion string) *envFile {
		return parseEnvFile([]byte(
			"AGENT_COMPOSE_IMAGE=registry.example/agent-compose:" + daemonVersion + "\n" +
				"AGENT_COMPOSE_FRONTEND_VERSION=latest\n" +
				"AGENT_COMPOSE_FRONTEND_IMAGE=registry.example/agent-compose-ui:latest\n" +
				"DEFAULT_IMAGE=registry.example/agent-compose-guest:" + daemonVersion + "\n" +
				"AGENT_COMPOSE_SANDBOX_HOST_IMAGE=registry.example/agent-compose:host-v1\n"))
	}
	options := DefaultOptions()
	env := parseEnvFile(nil)
	state := parseEnvFile(nil)

	install := planImageReferences(env, state, makeManifest("v2"), options, "install")
	if got := install.selection(sandboxHostImageKey); got.Value != "registry.example/agent-compose:host-v1" || got.Source != ImageSourceRelease {
		t.Fatalf("install sandbox-host selection = %#v", got)
	}
	if err := applyPlannedImageReferences(env, state, install); err != nil {
		t.Fatal(err)
	}
	if got, _ := env.Get(sandboxHostImageKey); got != "registry.example/agent-compose:host-v1" {
		t.Fatalf("env sandbox-host image = %q", got)
	}

	// A daemon-only upgrade advances the backend while the sandbox-host stays
	// pinned to its independently released tag.
	upgrade := planImageReferences(env, state, makeManifest("v3"), options, "upgrade")
	if got := upgrade.selection(backendImageKey); got.Value != "registry.example/agent-compose:v3" {
		t.Fatalf("upgrade backend selection = %#v", got)
	}
	if got := upgrade.selection(sandboxHostImageKey); got.Value != "registry.example/agent-compose:host-v1" || got.Source != ImageSourceRelease {
		t.Fatalf("upgrade sandbox-host selection = %#v", got)
	}
}

func TestSupportedFrontendVersions(t *testing.T) {
	manifest := parseEnvFile([]byte("AGENT_COMPOSE_FRONTEND_VERSION=v2\nAGENT_COMPOSE_FRONTEND_VERSIONS=v2,v1\n"))
	versions, defaultVersion, err := supportedFrontendVersions(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if defaultVersion != "v2" || !reflect.DeepEqual(versions, []string{"v2", "v1"}) {
		t.Fatalf("versions = %#v, default = %q", versions, defaultVersion)
	}

	for name, content := range map[string]string{
		"default missing":          "AGENT_COMPOSE_FRONTEND_VERSIONS=v1,v2\n",
		"default absent from list": "AGENT_COMPOSE_FRONTEND_VERSION=v3\nAGENT_COMPOSE_FRONTEND_VERSIONS=v1,v2\n",
		"duplicate":                "AGENT_COMPOSE_FRONTEND_VERSION=v1\nAGENT_COMPOSE_FRONTEND_VERSIONS=v1,v1\n",
		"invalid tag":              "AGENT_COMPOSE_FRONTEND_VERSION=v1\nAGENT_COMPOSE_FRONTEND_VERSIONS=v1,bad/tag\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := supportedFrontendVersions(parseEnvFile([]byte(content))); err == nil {
				t.Fatal("expected invalid frontend version manifest")
			}
		})
	}
}

func TestPreviewAppliesRegistryAndSelectedFrontendVersion(t *testing.T) {
	options := DefaultOptions()
	options.BundleDir = makeTestBundleWithFrontendVersions(t, "v2", "v2", "v2,v1")
	options.InstallDir = filepath.Join(t.TempDir(), "install")
	options.Registry = "mirror.example.com"
	options.RegistrySet = true
	options.FrontendVersion = "v1"
	options.FrontendVersionSet = true
	service := Service{}
	release, err := service.ResolveRelease(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()

	preview, err := service.PreviewImages(OperationInstall, options, release)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Registry != "mirror.example.com" || preview.FrontendVersion != "v1" {
		t.Fatalf("preview settings = %#v", preview)
	}
	for _, selection := range []ImageSelection{preview.Backend, preview.Frontend, preview.Guest} {
		if !strings.HasPrefix(selection.Value, "mirror.example.com/") {
			t.Fatalf("image did not use registry: %#v", selection)
		}
	}
	if !strings.HasSuffix(preview.Frontend.Value, ":v1") {
		t.Fatalf("frontend image = %q", preview.Frontend.Value)
	}

	options.FrontendVersion = "unsupported"
	if _, err := service.PreviewImages(OperationInstall, options, release); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported frontend version error = %v", err)
	}
}

func TestPreviewPreservesDigestPinnedDefaultFrontendImage(t *testing.T) {
	options := DefaultOptions()
	options.BundleDir = makeTestBundleWithFrontendVersions(t, "v2", "v2", "v2,v1")
	options.InstallDir = filepath.Join(t.TempDir(), "install")
	pinned := "registry.example/agent-compose-ui@sha256:abcdef"
	setTestBundleManifestValue(t, options.BundleDir, frontendImageKey, pinned)
	service := Service{}
	release, err := service.ResolveRelease(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()

	preview, err := service.PreviewImages(OperationInstall, options, release)
	if err != nil {
		t.Fatal(err)
	}
	if release.Images.Frontend != pinned || preview.Frontend.Value != pinned {
		t.Fatalf("default frontend images = release %q, preview %q; want %q", release.Images.Frontend, preview.Frontend.Value, pinned)
	}

	options.FrontendVersion = "v1"
	options.FrontendVersionSet = true
	preview, err = service.PreviewImages(OperationInstall, options, release)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Frontend.Value != "registry.example/agent-compose-ui:v1" {
		t.Fatalf("selected frontend image = %q", preview.Frontend.Value)
	}
}

func TestInstallPersistsAndUpgradeClearsRegistry(t *testing.T) {
	root := t.TempDir()
	options := DefaultOptions()
	options.BundleDir = makeTestBundleWithFrontendVersions(t, "v2", "v2", "v2,v1")
	options.InstallDir = filepath.Join(root, "install")
	options.KVMPath = filepath.Join(root, "missing-kvm")
	options.NoStart = true
	options.Registry = "mirror.example.com:5000"
	options.RegistrySet = true
	options.FrontendVersion = "v1"
	options.FrontendVersionSet = true

	if _, err := (Service{Runner: &fakeRunner{}}).Apply(context.Background(), OperationInstall, options); err != nil {
		t.Fatal(err)
	}
	state := readTestEnv(t, filepath.Join(options.InstallDir, ".installer-state.env"))
	assertTestEnv(t, state, installerRegistryKey, "mirror.example.com:5000")
	assertTestEnv(t, state, installerImagePrefixKey, "")
	assertTestEnv(t, state, installerFrontendVersionKey, "v1")
	env := readTestEnv(t, filepath.Join(options.InstallDir, ".env"))
	assertTestEnv(t, env, backendImageKey, "mirror.example.com:5000/agent-compose:v2")
	assertTestEnv(t, env, frontendImageKey, "mirror.example.com:5000/agent-compose-ui:v1")
	assertTestEnv(t, env, guestImageKey, "mirror.example.com:5000/agent-compose-guest:v2")
	assertTestEnv(t, env, sandboxHostImageKey, "mirror.example.com:5000/agent-compose:host-v1")

	options.BundleDir = makeTestBundleWithFrontendVersions(t, "v3", "v2", "v2,v1")
	options.Registry = ""
	if _, err := (Service{Runner: &fakeRunner{}}).Apply(context.Background(), OperationUpgrade, options); err != nil {
		t.Fatal(err)
	}
	state = readTestEnv(t, filepath.Join(options.InstallDir, ".installer-state.env"))
	assertTestEnv(t, state, installerRegistryKey, "")
	env = readTestEnv(t, filepath.Join(options.InstallDir, ".env"))
	assertTestEnv(t, env, backendImageKey, "registry.example/agent-compose:v3")
	assertTestEnv(t, env, frontendImageKey, "registry.example/agent-compose-ui:v1")
	assertTestEnv(t, env, guestImageKey, "registry.example/agent-compose-guest:v3")
	// The sandbox-host image is pinned independently of the daemon release, so
	// it survives the v2->v3 upgrade untouched.
	assertTestEnv(t, env, sandboxHostImageKey, "registry.example/agent-compose:host-v1")
}

func TestEffectiveOptionsMigratesLegacyImagePrefix(t *testing.T) {
	state := parseEnvFile([]byte(
		"AGENT_COMPOSE_IMAGE=legacy.example/team/agent-compose:v1\n" +
			"AGENT_COMPOSE_FRONTEND_IMAGE=legacy.example/team/agent-compose-ui:v1\n" +
			"DEFAULT_IMAGE=legacy.example/team/agent-compose-guest:v1\n",
	))
	manifest := parseEnvFile([]byte(
		"AGENT_COMPOSE_FRONTEND_VERSION=v2\n" +
			"AGENT_COMPOSE_FRONTEND_VERSIONS=v2,v1\n",
	))
	env := parseEnvFile([]byte("AGENT_COMPOSE_FRONTEND_VERSION=v1\n"))
	effective, err := effectiveImageOptions(env, state, manifest, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if effective.ImagePrefix != "legacy.example/team" || effective.FrontendVersion != "v1" {
		t.Fatalf("legacy settings = %#v", effective)
	}
}

func TestUpgradePreservesSupportedFrontendVersionAndRejectsRemovedVersion(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	options := DefaultOptions()
	options.BundleDir = makeTestBundleWithFrontendVersions(t, "v1", "v2", "v2,v1")
	options.InstallDir = installDir
	options.KVMPath = filepath.Join(root, "missing-kvm")
	options.NoStart = true
	options.Registry = "mirror.example.com"
	options.RegistrySet = true
	options.FrontendVersion = "v1"
	options.FrontendVersionSet = true
	service := Service{Runner: &fakeRunner{}}
	if _, err := service.Apply(context.Background(), OperationInstall, options); err != nil {
		t.Fatal(err)
	}

	options.BundleDir = makeTestBundleWithFrontendVersions(t, "v2", "v3", "v3,v1")
	options.Registry, options.RegistrySet = "", false
	options.FrontendVersion, options.FrontendVersionSet = "", false
	if _, err := service.Apply(context.Background(), OperationUpgrade, options); err != nil {
		t.Fatal(err)
	}
	env := readTestEnv(t, filepath.Join(installDir, ".env"))
	assertTestEnv(t, env, frontendVersionKey, "v1")
	assertTestEnv(t, env, frontendImageKey, "mirror.example.com/agent-compose-ui:v1")

	options.BundleDir = makeTestBundleWithFrontendVersions(t, "v3", "v3", "v3,v2")
	if _, err := service.Apply(context.Background(), OperationUpgrade, options); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("removed frontend version error = %v", err)
	}
}

func TestUpgradeExplicitFrontendVersionOverridesOperatorEditedValue(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	options := DefaultOptions()
	options.BundleDir = makeTestBundleWithFrontendVersions(t, "v1", "v1", "v1,v2")
	options.InstallDir = installDir
	options.KVMPath = filepath.Join(root, "missing-kvm")
	options.NoStart = true
	options.FrontendVersion = "v1"
	options.FrontendVersionSet = true
	service := Service{Runner: &fakeRunner{}}
	if _, err := service.Apply(context.Background(), OperationInstall, options); err != nil {
		t.Fatal(err)
	}

	envPath := filepath.Join(installDir, ".env")
	env := readTestEnv(t, envPath)
	if err := env.Set(frontendVersionKey, "operator-edited"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, env.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	options.BundleDir = makeTestBundleWithFrontendVersions(t, "v2", "v2", "v2,v1")
	options.FrontendVersion = "v2"
	if _, err := service.Apply(context.Background(), OperationUpgrade, options); err != nil {
		t.Fatal(err)
	}
	env = readTestEnv(t, envPath)
	assertTestEnv(t, env, frontendVersionKey, "v2")
	assertTestEnv(t, env, frontendImageKey, "registry.example/agent-compose-ui:v2")
	state := readTestEnv(t, filepath.Join(installDir, ".installer-state.env"))
	assertTestEnv(t, state, frontendVersionKey, "v2")
	assertTestEnv(t, state, installerFrontendVersionKey, "v2")
}

func TestResolveReleaseReportsManifestImages(t *testing.T) {
	options := DefaultOptions()
	options.BundleDir = makeTestBundle(t, "v2")
	options.InstallDir = filepath.Join(t.TempDir(), "install")
	service := Service{}

	release, err := service.ResolveRelease(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()

	want := ImageReferences{
		Backend:  "registry.example/agent-compose:v2",
		Frontend: "registry.example/agent-compose-ui:latest",
		Guest:    "registry.example/agent-compose-guest:v2",
	}
	if release.Images != want {
		t.Fatalf("release images = %#v, want %#v", release.Images, want)
	}
	preview, err := service.PreviewImages(OperationInstall, options, release)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Backend.Value != want.Backend || preview.Backend.Source != ImageSourceRelease ||
		preview.Frontend.Value != want.Frontend || preview.Guest.Value != want.Guest {
		t.Fatalf("install preview = %#v", preview)
	}
}

func TestPreviewImagesMatchesUpgradePreservation(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	writeTestFile(t, filepath.Join(installDir, ".env"), "AGENT_COMPOSE_IMAGE=operator.example/backend:keep\nAGENT_COMPOSE_FRONTEND_IMAGE=registry.example/agent-compose-ui:v1\nDEFAULT_IMAGE=registry.example/agent-compose-guest:v1\n", 0o600)
	writeTestFile(t, filepath.Join(installDir, ".installer-state.env"), "AGENT_COMPOSE_IMAGE=registry.example/agent-compose:v1\nAGENT_COMPOSE_FRONTEND_IMAGE=registry.example/agent-compose-ui:v1\nDEFAULT_IMAGE=registry.example/agent-compose-guest:v1\n", 0o600)
	options := DefaultOptions()
	options.BundleDir = makeTestBundle(t, "v2")
	options.InstallDir = installDir
	service := Service{}
	release, err := service.ResolveRelease(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()

	preview, err := service.PreviewImages(OperationUpgrade, options, release)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Backend != (ImageSelection{Value: "operator.example/backend:keep", Source: ImageSourcePreserved}) {
		t.Fatalf("backend preview = %#v", preview.Backend)
	}
	if preview.Frontend != (ImageSelection{Value: "registry.example/agent-compose-ui:latest", Source: ImageSourceRelease}) {
		t.Fatalf("frontend preview = %#v", preview.Frontend)
	}
	if preview.Guest != (ImageSelection{Value: "registry.example/agent-compose-guest:v2", Source: ImageSourceRelease}) {
		t.Fatalf("guest preview = %#v", preview.Guest)
	}
}

func TestApplyReleaseReusesResolvedBundle(t *testing.T) {
	root := t.TempDir()
	options := DefaultOptions()
	options.BundleDir = makeTestBundle(t, "v1")
	options.InstallDir = filepath.Join(root, "install")
	options.KVMPath = filepath.Join(root, "missing-kvm")
	options.NoStart = true
	service := Service{Runner: &fakeRunner{}}
	release, err := service.ResolveRelease(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()

	options.BundleDir = filepath.Join(root, "bundle-was-removed")
	result, err := service.ApplyRelease(context.Background(), OperationInstall, options, release)
	if err != nil {
		t.Fatal(err)
	}
	if result.GuestImage != "registry.example/agent-compose-guest:v1" {
		t.Fatalf("guest image = %q", result.GuestImage)
	}
	if _, err := os.Stat(filepath.Join(options.InstallDir, ".env")); err != nil {
		t.Fatal(err)
	}
}

func makeTestBundleWithFrontendVersions(t *testing.T, releaseVersion, defaultVersion, versions string) string {
	t.Helper()
	dir := makeTestBundle(t, releaseVersion)
	path := filepath.Join(dir, "images", "manifest.env")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest := parseEnvFile(data)
	for key, value := range map[string]string{
		frontendVersionKey:  defaultVersion,
		frontendVersionsKey: versions,
		frontendImageKey:    "registry.example/agent-compose-ui:" + defaultVersion,
	} {
		if err := manifest.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, manifest.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func setTestBundleManifestValue(t *testing.T, dir, key, value string) {
	t.Helper()
	path := filepath.Join(dir, "images", "manifest.env")
	manifest := readTestEnv(t, path)
	if err := manifest.Set(key, value); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, manifest.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
