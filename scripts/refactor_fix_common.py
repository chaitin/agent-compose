#!/usr/bin/env python3
"""Common mechanical fixes after the first-pass package split.

This intentionally handles broad, repeatable breakages:
  * add shared driver type imports to driver implementation subpackages;
  * export driver subpackage constructors for the root facade;
  * add common model/persistence aliases to internal packages that still use
    legacy same-package type names.
"""

from __future__ import annotations

import re
import subprocess
from pathlib import Path


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def write(path: Path, text: str) -> None:
    path.write_text(text, encoding="utf-8")


def ensure_import(path: Path, spec: str) -> None:
    text = read(path)
    if spec in text:
        return
    if "import (\n" in text:
        text = text.replace("import (\n", f"import (\n\t{spec}\n", 1)
    else:
        text = re.sub(r'import\s+"([^"]+)"', f'import (\n\t{spec}\n\t"\\1"\n)', text, count=1)
    write(path, text)


def remove_import(path: Path, spec: str) -> None:
    text = read(path)
    new = text.replace(f"\t{spec}\n", "")
    if new != text:
        write(path, new)


def replace(path: Path, old: str, new: str) -> None:
    text = read(path)
    if old in text:
        write(path, text.replace(old, new))


def append_aliases(path: Path, aliases: str) -> None:
    text = read(path)
    marker = "// refactor aliases"
    if marker in text:
        return
    insert_at = 0
    m = re.search(r"import\s+(?:\([^)]+\)|\"[^\"]+\")\n", text, flags=re.S)
    if m:
        insert_at = m.end()
    else:
        m = re.search(r"^package\s+\w+\n", text, flags=re.M)
        insert_at = m.end() if m else 0
    text = text[:insert_at] + "\n" + aliases.strip() + "\n\n" + text[insert_at:]
    write(path, text)


def main() -> None:
    # Driver implementation packages use the public types package.
    for root in [
        Path("pkg/driver/docker"),
        Path("pkg/driver/boxlite"),
        Path("pkg/driver/microsandbox"),
        Path("pkg/driver/mount"),
        Path("pkg/driver/session"),
        Path("pkg/driver/internal/execfilter"),
    ]:
        for path in root.glob("*.go"):
            if path.name.endswith("_test.go") or "coverage_shape" in path.name:
                continue
            ensure_import(path, '. "agent-compose/pkg/driver/types"')

    for path in Path("pkg/driver/internal/execfilter").glob("*_test.go"):
        ensure_import(path, '. "agent-compose/pkg/driver/types"')

    # Export the constructors used by pkg/driver/driver.go.
    for path, old, new in [
        (Path("pkg/driver/docker/runtime.go"), "func newDockerRuntime", "func NewRuntime"),
        (Path("pkg/driver/boxlite/cgo.go"), "func newBoxRuntime", "func NewRuntime"),
        (Path("pkg/driver/boxlite/stub.go"), "func newBoxRuntime", "func NewRuntime"),
        (Path("pkg/driver/microsandbox/runtime.go"), "func newMicrosandboxRuntime", "func NewRuntime"),
        (Path("pkg/driver/microsandbox/runtime_stub.go"), "func newMicrosandboxRuntime", "func NewRuntime"),
    ]:
        replace(path, old, new)

    # Exec filter is shared by driver runtime implementations.
    ef = Path("pkg/driver/internal/execfilter/output_filter.go")
    replace(ef, "type execOutputFilter", "type ExecOutputFilter")
    replace(ef, "func newExecOutputFilter", "func New")
    replace(ef, "*execOutputFilter", "*ExecOutputFilter")
    replace(ef, "execOutputFilter{", "ExecOutputFilter{")
    for path in [
        Path("pkg/driver/docker/runtime.go"),
        Path("pkg/driver/microsandbox/runtime.go"),
        Path("pkg/driver/boxlite/cgo.go"),
    ]:
        ensure_import(path, 'execfilter "agent-compose/pkg/driver/internal/execfilter"')
        replace(path, "*execOutputFilter", "*execfilter.ExecOutputFilter")
        replace(path, "newExecOutputFilter()", "execfilter.New()")

    # Root driver facade imports subpackage constructors and aliases shared types.
    driver_go = Path("pkg/driver/driver.go")
    for spec in [
        'boxlitedriver "agent-compose/pkg/driver/boxlite"',
        'dockerdriver "agent-compose/pkg/driver/docker"',
        'microsandboxdriver "agent-compose/pkg/driver/microsandbox"',
        'drivertypes "agent-compose/pkg/driver/types"',
    ]:
        ensure_import(driver_go, spec)
    replace(driver_go, "return newBoxRuntime(do.MustInvoke[*appconfig.Config](di))", "return boxlitedriver.NewRuntime(do.MustInvoke[*appconfig.Config](di))")
    replace(driver_go, "return newBoxRuntime(config)", "return boxlitedriver.NewRuntime(config)")
    replace(driver_go, "return newDockerRuntime(config)", "return dockerdriver.NewRuntime(config)")
    replace(driver_go, "return newMicrosandboxRuntime(config)", "return microsandboxdriver.NewRuntime(config)")
    append_aliases(
        driver_go,
        """
// refactor aliases
type SessionEnvVar = drivertypes.SessionEnvVar
type SessionSummary = drivertypes.SessionSummary
type Session = drivertypes.Session
type VMState = drivertypes.VMState
type ProxyState = drivertypes.ProxyState
type ExecChunk = drivertypes.ExecChunk
type ExecSpec = drivertypes.ExecSpec
type ExecResult = drivertypes.ExecResult
type ExecStreamWriter = drivertypes.ExecStreamWriter
type SessionVMInfo = drivertypes.SessionVMInfo
type BoxRuntime = drivertypes.BoxRuntime
""",
    )

    # Public driver primitives shared across implementation packages. Dot
    # imports only expose exported identifiers, so promote the small helpers
    # that were previously same-package private.
    types_go = Path("pkg/driver/types/types.go")
    text = read(types_go)
    if "RuntimeDriverBoxlite" not in text:
        text = text.replace(
            "type SessionEnvVar struct",
            """const (
\tRuntimeDriverBoxlite      = "boxlite"
\tRuntimeDriverDocker       = "docker"
\tRuntimeDriverMicrosandbox = "microsandbox"
)

const DirectoryOnlyGuestSessionPath = "/data"

type SessionEnvVar struct""",
            1,
        )
    elif "DirectoryOnlyGuestSessionPath" not in text:
        text = text.replace(
            ")\n\ntype SessionEnvVar struct",
            ")\n\nconst DirectoryOnlyGuestSessionPath = \"/data\"\n\ntype SessionEnvVar struct",
            1,
        )
    if "func ImageCacheRootForDriver" not in text:
        text += r'''

func ImageCacheRootForDriver(config *appconfig.Config) string {
	if config == nil {
		return filepath.Join(".", "data", "images")
	}
	if root := strings.TrimSpace(config.ImageCacheRoot); root != "" {
		return root
	}
	if dataRoot := strings.TrimSpace(config.DataRoot); dataRoot != "" {
		return filepath.Join(dataRoot, "images")
	}
	return filepath.Join(".", "data", "images")
}
'''
    if "func DirectoryOnlyGuestSessionBootstrapCommand" not in text:
        text += r'''

func DirectoryOnlyGuestSessionBootstrapCommand(config *appconfig.Config) string {
	appconfig.ApplyDefaultGuestPaths(config)
	commands := []string{
		"if [ -d " + ShellQuote(filepath.Join(DirectoryOnlyGuestSessionPath, "workspace")) + " ] && [ -d " + ShellQuote(filepath.Join(DirectoryOnlyGuestSessionPath, "home")) + " ]; then",
	}
	for _, link := range []struct {
		source string
		target string
	}{
		{source: filepath.Join(DirectoryOnlyGuestSessionPath, "workspace"), target: config.GuestWorkspacePath},
		{source: filepath.Join(DirectoryOnlyGuestSessionPath, "state"), target: config.GuestStateRoot},
		{source: filepath.Join(DirectoryOnlyGuestSessionPath, "runtime"), target: config.GuestRuntimeRoot},
		{source: filepath.Join(DirectoryOnlyGuestSessionPath, "logs"), target: config.GuestLogRoot},
		{source: filepath.Join(DirectoryOnlyGuestSessionPath, "home"), target: config.GuestHomePath},
	} {
		source := filepath.Clean(link.source)
		target := filepath.Clean(link.target)
		if source == target {
			continue
		}
		commands = append(commands,
			"  rm -rf "+ShellQuote(target)+";",
			"  mkdir -p "+ShellQuote(filepath.Dir(target))+";",
			"  ln -s "+ShellQuote(source)+" "+ShellQuote(target)+";",
		)
	}
	commands = append(commands, "fi")
	return strings.Join(commands, " ")
}
'''
    replacements = {
        "func firstNonEmpty": "func FirstNonEmpty",
        "func hostSessionDir": "func HostSessionDir",
        "func hostSessionHome": "func HostSessionHome",
        "func shellQuote": "func ShellQuote",
        "hostSessionDir(session)": "HostSessionDir(session)",
    }
    for old, new in replacements.items():
        text = text.replace(old, new)
    text = re.sub(r"\nfunc SessionEnvMap\(groups \.\.\.\[\]SessionEnvVar\) map\[string\]string \{\n\treturn SessionEnvMap\(groups\.\.\.\)\n\}\n", "\n", text)
    if "func ResolveRuntimeDriver" not in text:
        text += r'''

func ResolveRuntimeDriver(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return RuntimeDriverDocker
	case RuntimeDriverBoxlite:
		return RuntimeDriverBoxlite
	case RuntimeDriverDocker, "docker-engine":
		return RuntimeDriverDocker
	case "msb", RuntimeDriverMicrosandbox:
		return RuntimeDriverMicrosandbox
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func ValidateRuntimeDriver(value string) error {
	switch ResolveRuntimeDriver(value) {
	case RuntimeDriverBoxlite, RuntimeDriverDocker, RuntimeDriverMicrosandbox:
		return nil
	default:
		return fmt.Errorf("unsupported agent-compose runtime driver %q", strings.TrimSpace(value))
	}
}

func ResolveSessionRuntimeDriver(value, fallback string) (string, error) {
	input := value
	if strings.TrimSpace(input) == "" {
		input = fallback
	}
	driver := ResolveRuntimeDriver(input)
	if err := ValidateRuntimeDriver(driver); err != nil {
		return "", err
	}
	return driver, nil
}

func DefaultGuestImageForDriver(config *appconfig.Config, driver string) string {
	switch ResolveRuntimeDriver(driver) {
	case RuntimeDriverMicrosandbox:
		return config.MicrosandboxDefaultImage
	case RuntimeDriverDocker:
		return FirstNonEmpty(config.DockerDefaultImage, config.DefaultImage)
	}
	return config.DefaultImage
}

func RuntimeHomeForDriver(config *appconfig.Config, driver string) string {
	switch ResolveRuntimeDriver(driver) {
	case RuntimeDriverMicrosandbox:
		return config.MicrosandboxHome
	case RuntimeDriverDocker:
		return config.DockerHome
	}
	return config.BoxliteHome
}
'''
    if "func sessionEnvMap" not in text:
        text = text.replace(
            "func LLMProviderKeyName",
            "func sessionEnvMap(groups ...[]SessionEnvVar) map[string]string {\n\treturn SessionEnvMap(groups...)\n}\n\nfunc LLMProviderKeyName",
            1,
        )
    if '"fmt"' not in text:
        text = text.replace('import (\n\t"context"', 'import (\n\t"context"\n\t"fmt"', 1)
    if 'appconfig "agent-compose/pkg/config"' not in text:
        text = text.replace('import (\n\t"context"', 'import (\n\t"context"\n\n\tappconfig "agent-compose/pkg/config"', 1)
    write(types_go, text)

    # Keep the root facade API while delegating shared runtime-driver logic to
    # the public types package.
    runtime_driver_go = Path("pkg/driver/runtime_driver.go")
    write(runtime_driver_go, """package driver

import (
\tappconfig "agent-compose/pkg/config"
\tdrivertypes "agent-compose/pkg/driver/types"
)

const (
\tRuntimeDriverBoxlite      = drivertypes.RuntimeDriverBoxlite
\tRuntimeDriverDocker       = drivertypes.RuntimeDriverDocker
\tRuntimeDriverMicrosandbox = drivertypes.RuntimeDriverMicrosandbox
)

func ResolveRuntimeDriver(value string) string {
\treturn drivertypes.ResolveRuntimeDriver(value)
}

func ValidateRuntimeDriver(value string) error {
\treturn drivertypes.ValidateRuntimeDriver(value)
}

func ResolveSessionRuntimeDriver(value, fallback string) (string, error) {
\treturn drivertypes.ResolveSessionRuntimeDriver(value, fallback)
}

func DefaultGuestImageForDriver(config *appconfig.Config, driver string) string {
\treturn drivertypes.DefaultGuestImageForDriver(config, driver)
}

func RuntimeHomeForDriver(config *appconfig.Config, driver string) string {
\treturn drivertypes.RuntimeHomeForDriver(config, driver)
}
""")

    # Promote shared type helper call sites after splitting packages.
    for root in [Path("pkg/driver/boxlite"), Path("pkg/driver/docker"), Path("pkg/driver/microsandbox"), Path("pkg/driver/mount"), Path("pkg/driver/session")]:
        for path in root.glob("*.go"):
            for old, new in {
                "sessionEnvMap(": "SessionEnvMap(",
                "firstNonEmpty(": "FirstNonEmpty(",
                "hostSessionDir(": "HostSessionDir(",
                "hostSessionHome(": "HostSessionHome(",
                "shellQuote(": "ShellQuote(",
                "resolveRuntimeDriver(": "ResolveRuntimeDriver(",
                "validateRuntimeDriver(": "ValidateRuntimeDriver(",
                "resolveSessionRuntimeDriver(": "ResolveSessionRuntimeDriver(",
                "defaultGuestImageForDriver(": "DefaultGuestImageForDriver(",
                "runtimeHomeForDriver(": "RuntimeHomeForDriver(",
                "directoryOnlyGuestSessionPath": "DirectoryOnlyGuestSessionPath",
            }.items():
                replace(path, old, new)

    # Cross-package helper ownership after the split.
    for path in [Path("pkg/driver/docker/runtime.go"), Path("pkg/driver/microsandbox/runtime.go")]:
        ensure_import(path, 'boxlitedriver "agent-compose/pkg/driver/boxlite"')
        ensure_import(path, 'driverimage "agent-compose/pkg/driver/image"')
        ensure_import(path, 'drivermount "agent-compose/pkg/driver/mount"')
        for old, new in {
            "waitForJupyterProxy(": "boxlitedriver.WaitForJupyterProxy(",
            "readSessionJupyterLog(": "boxlitedriver.ReadSessionJupyterLog(",
            "jupyterLogIndicatesReady(": "boxlitedriver.JupyterLogIndicatesReady(",
            "jupyterDirectURL(": "boxlitedriver.JupyterDirectURL(",
            "jupyterLaunchCommand(": "boxlitedriver.JupyterLaunchCommand(",
            "loadRuntimeMountManifest(": "drivermount.LoadRuntimeMountManifest(",
            "loadDirectoryRuntimeMountManifest(": "drivermount.LoadDirectoryRuntimeMountManifest(",
            "resolveSessionGuestImage(": "driverimage.ResolveSessionGuestImage(",
        }.items():
            replace(path, old, new)

    for path in [Path("pkg/driver/boxlite/cgo.go"), Path("pkg/driver/microsandbox/runtime.go")]:
        ensure_import(path, 'drivermount "agent-compose/pkg/driver/mount"')
        replace(path, "loadDirectoryRuntimeMountManifest(", "drivermount.LoadDirectoryRuntimeMountManifest(")

    for path in [Path("pkg/driver/boxlite/cgo.go"), Path("pkg/driver/microsandbox/runtime.go"), Path("pkg/driver/session/start.go")]:
        ensure_import(path, 'driverimage "agent-compose/pkg/driver/image"')
        replace(path, "resolveSessionGuestImage(", "driverimage.ResolveSessionGuestImage(")

    session_go = Path("pkg/driver/session/start.go")
    ensure_import(session_go, 'dockerdriver "agent-compose/pkg/driver/docker"')
    ensure_import(session_go, 'drivermount "agent-compose/pkg/driver/mount"')
    replace(session_go, "ensureDockerImage", "dockerdriver.EnsureDockerImage")
    replace(session_go, "prepareRuntimeMountManifest(", "drivermount.PrepareRuntimeMountManifest(")

    for path in [Path("pkg/driver/boxlite/cgo.go"), Path("pkg/driver/microsandbox/runtime.go")]:
        ensure_import(path, 'envpath "agent-compose/pkg/driver/internal/envpath"')
        ensure_import(path, 'dockerprobe "agent-compose/pkg/driver/internal/dockerprobe"')
        replace(path, "prependEnvPath(", "envpath.Prepend(")
        replace(path, "dockerDaemonAvailable", "dockerprobe.Available")

    env_path_go = Path("pkg/driver/internal/envpath/env_path.go")
    replace(env_path_go, "func prependEnvPath", "func Prepend")

    # Export Jupyter helpers from the package that already owns the launch
    # implementation. This is an interim coherent boundary for the first split.
    guest_go = Path("pkg/driver/boxlite/guest_cgo.go")
    for old, new in {
        "func jupyterDirectURL": "func JupyterDirectURL",
        "func readSessionJupyterLog": "func ReadSessionJupyterLog",
        "func jupyterLogIndicatesReady": "func JupyterLogIndicatesReady",
        "func jupyterLaunchCommand": "func JupyterLaunchCommand",
        "jupyterDirectURL(": "JupyterDirectURL(",
        "readSessionJupyterLog(": "ReadSessionJupyterLog(",
        "jupyterLogIndicatesReady(": "JupyterLogIndicatesReady(",
        "jupyterLaunchCommand(": "JupyterLaunchCommand(",
    }.items():
        replace(guest_go, old, new)

    # Fix execfilter tests after promoting the implementation type.
    for path in Path("pkg/driver/internal/execfilter").glob("*_test.go"):
        replace(path, "newExecOutputFilter()", "New()")
        replace(path, "execOutputFilter", "ExecOutputFilter")

    # Minimal local Docker image reference resolver for the image package.
    local_oci = Path("pkg/driver/image/local_docker_oci.go")
    replace(local_oci, "resolveLocalDockerImageRef(ctx, dockerClient, imageRef)", "resolveLocalDockerImageRefForImage(ctx, dockerClient, imageRef)")
    if "func resolveLocalDockerImageRefForImage" not in read(local_oci):
        write(local_oci, read(local_oci) + r'''

func resolveLocalDockerImageRefForImage(ctx context.Context, dockerClient *client.Client, imageRef string) (string, bool, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return "", false, nil
	}
	if _, err := dockerClient.ImageInspect(ctx, imageRef); err == nil {
		return imageRef, true, nil
	} else if !cerrdefs.IsNotFound(err) {
		return "", false, err
	}
	images, err := dockerClient.ImageList(ctx, typesimage.ListOptions{All: true})
	if err != nil {
		return "", false, err
	}
	for _, image := range images {
		for _, candidateRef := range append(append([]string(nil), image.RepoTags...), image.RepoDigests...) {
			if strings.TrimSpace(candidateRef) == imageRef || strings.HasSuffix(candidateRef, "/"+imageRef) {
				return candidateRef, true, nil
			}
		}
	}
	return "", false, nil
}
''')

    for path in [
        Path("pkg/driver/boxlite/image_resolver.go"),
        Path("pkg/driver/boxlite/image_materialize_cgo.go"),
        Path("pkg/driver/docker/probe.go"),
        Path("pkg/driver/microsandbox/image_resolver.go"),
    ]:
        if path.exists():
            remove_import(path, '. "agent-compose/pkg/driver/types"')

    image_oci = Path("pkg/driver/image/local_docker_oci.go")
    replace(image_oci, "type localDockerImageRootfs", "type LocalDockerImageRootfs")
    replace(image_oci, "localDockerImageRootfs", "LocalDockerImageRootfs")
    replace(image_oci, "func materializeLocalDockerImageRootfs(", "func MaterializeLocalDockerImageRootfs(")
    replace(image_oci, "func materializeLocalDockerImageRootfsWithClient(", "func materializeLocalDockerImageRootfsWithClient(")
    replace(image_oci, "return materializeLocalDockerImageRootfsWithClient", "return materializeLocalDockerImageRootfsWithClient")

    boxlite_resolver = Path("pkg/driver/boxlite/image_resolver.go")
    text = read(boxlite_resolver)
    text = re.sub(r"\nfunc (?:drivertypes\.)?ImageCacheRootForDriver\(config \*appconfig\.Config\) string \{.*?\n\}\n", "\n", text, flags=re.S)
    text = text.replace('\tappconfig "agent-compose/pkg/config"\n', "")
    text = text.replace('\t"path/filepath"\n', "")
    text = text.replace('\tdrivertypes "agent-compose/pkg/driver/types"\n', "")
    write(boxlite_resolver, text)

    for path in [Path("pkg/driver/boxlite/image_materialize_cgo.go"), Path("pkg/driver/boxlite/image_resolver_test.go"), Path("pkg/driver/microsandbox/image_resolver.go"), Path("pkg/driver/mount/oci_image_smoke_test.go")]:
        if path.exists():
            ensure_import(path, 'drivertypes "agent-compose/pkg/driver/types"')
            replace(path, "imageCacheRootForDriver(", "drivertypes.ImageCacheRootForDriver(")

    ms_runtime = Path("pkg/driver/microsandbox/runtime.go")
    remove_import(ms_runtime, 'dockerdriver "agent-compose/pkg/driver/docker"')
    replace(ms_runtime, "materializeLocalDockerImageRootfs(", "driverimage.MaterializeLocalDockerImageRootfs(")

    # Test files still exercise shared driver DTOs/constants after the split.
    for root in [Path("pkg/driver/mount"), Path("pkg/driver/session")]:
        for path in root.glob("*_test.go"):
            ensure_import(path, '. "agent-compose/pkg/driver/types"')

    session_test = Path("pkg/driver/session/start_test.go")
    ensure_import(session_test, 'drivermount "agent-compose/pkg/driver/mount"')
    replace(session_test, "loadDirectoryRuntimeMountManifest(", "drivermount.LoadDirectoryRuntimeMountManifest(")
    if "func testRuntimeMountConfig" not in read(session_test):
        write(session_test, read(session_test) + r'''

func testRuntimeMountConfig() *appconfig.Config {
	return &appconfig.Config{
		GuestWorkspacePath: "/workspace",
		GuestHomePath:      "/root",
		GuestStateRoot:     "/data/state",
		GuestRuntimeRoot:   "/data/runtime",
		GuestLogRoot:       "/data/logs",
	}
}

func testRuntimeMountSession(root string) *Session {
	return &Session{Summary: SessionSummary{
		ID:            "session-1",
		WorkspacePath: filepath.Join(root, "workspace"),
	}}
}
''')

    replace(Path("pkg/driver/mount/manifest_boxlite_smoke_test.go"), "//go:build boxlitecgo", "//go:build boxlitecgo && driversmoke")
    replace(Path("pkg/driver/mount/manifest_microsandbox_smoke_test.go"), "//go:build cgo", "//go:build cgo && driversmoke")
    mount_test = Path("pkg/driver/mount/manifest_test.go")
    replace(mount_test, "directoryOnlyGuestSessionBootstrapCommand(", "DirectoryOnlyGuestSessionBootstrapCommand(")
    if "func assertFileContent" not in read(mount_test):
        write(mount_test, read(mount_test) + r'''

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", path, err)
	}
	if got := string(data); got != want {
		t.Fatalf("ReadFile(%s) = %q, want %q", path, got, want)
	}
}
''')

    docker_runtime_test = Path("pkg/driver/docker/runtime_test.go")
    ensure_import(docker_runtime_test, '. "agent-compose/pkg/driver/types"')
    ensure_import(docker_runtime_test, 'drivermount "agent-compose/pkg/driver/mount"')
    replace(docker_runtime_test, "prepareRuntimeMountManifest(", "drivermount.PrepareRuntimeMountManifest(")
    if "func testRuntimeMountSession" not in read(docker_runtime_test):
        write(docker_runtime_test, read(docker_runtime_test) + r'''

func testRuntimeMountSession(root string) *Session {
	return &Session{Summary: SessionSummary{
		ID:            "session-1",
		WorkspacePath: filepath.Join(root, "workspace"),
	}}
}
''')

    docker_image_go = Path("pkg/driver/docker/image.go")
    remove_import(docker_image_go, '. "agent-compose/pkg/driver/types"')

    subprocess.run(["gofmt", "-w", "pkg/driver"], check=True)


if __name__ == "__main__":
    main()
