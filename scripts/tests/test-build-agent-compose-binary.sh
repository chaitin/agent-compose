#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
SOURCE_HELPER="$ROOT_DIR/scripts/build-agent-compose-binary.sh"
SOURCE_RUNTIME_EXPORTER="$ROOT_DIR/scripts/export-runtime-dev-artifact.sh"

fail() {
  printf 'test-build-agent-compose-binary: %s\n' "$*" >&2
  exit 1
}

assert_status() {
  local want=$1
  if [[ $RUN_STATUS -ne $want ]]; then
    printf 'stdout:\n' >&2
    sed 's/^/  /' "$RUN_STDOUT" >&2
    printf 'stderr:\n' >&2
    sed 's/^/  /' "$RUN_STDERR" >&2
    fail "status=$RUN_STATUS, want $want"
  fi
}

assert_success() {
  assert_status 0
}

assert_failure() {
  if [[ $RUN_STATUS -eq 0 ]]; then
    printf 'stdout:\n' >&2
    sed 's/^/  /' "$RUN_STDOUT" >&2
    printf 'stderr:\n' >&2
    sed 's/^/  /' "$RUN_STDERR" >&2
    fail "command unexpectedly succeeded"
  fi
}

assert_contains() {
  local file=$1
  local text=$2
  if ! grep -F -- "$text" "$file" >/dev/null; then
    printf 'file %s:\n' "$file" >&2
    sed 's/^/  /' "$file" >&2
    fail "missing expected text: $text"
  fi
}

assert_not_contains() {
  local file=$1
  local text=$2
  if grep -F -- "$text" "$file" >/dev/null; then
    printf 'file %s:\n' "$file" >&2
    sed 's/^/  /' "$file" >&2
    fail "unexpected text: $text"
  fi
}

assert_output_equals() {
  local expected=$1
  local actual
  actual=$(cat "$RUN_STDOUT")
  if [[ "$actual" != "$expected" ]]; then
    printf 'actual stdout:\n%s\n' "$actual" >&2
    printf 'expected stdout:\n%s\n' "$expected" >&2
    fail "stdout mismatch"
  fi
}

assert_build_arg() {
  assert_contains "$FAKE_GO_LOG" "ARG=$1"
}

assert_no_build() {
  assert_not_contains "$FAKE_GO_LOG" 'CALL=build'
}

TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT

TEST_ROOT="$TMP_DIR/repo"
mkdir -p "$TEST_ROOT/scripts"
cp "$SOURCE_HELPER" "$TEST_ROOT/scripts/build-agent-compose-binary.sh"
cp "$SOURCE_RUNTIME_EXPORTER" "$TEST_ROOT/scripts/export-runtime-dev-artifact.sh"
cp "$ROOT_DIR/scripts/with-go-toolchain.sh" "$TEST_ROOT/scripts/with-go-toolchain.sh"
chmod +x "$TEST_ROOT/scripts/with-go-toolchain.sh"
HELPER="$TEST_ROOT/scripts/build-agent-compose-binary.sh"
printf 'FROM scratch\n' >"$TEST_ROOT/Dockerfile"

FAKE_BIN="$TMP_DIR/fake-bin"
FAKE_GOROOT="$TMP_DIR/fake-goroot"
FAKE_GO="$FAKE_BIN/go"
FAKE_GO_LOG="$TMP_DIR/fake-go.log"
mkdir -p "$FAKE_BIN" "$FAKE_GOROOT/bin"

cat >"$FAKE_GO" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

command=${1:-}
if [[ $# -gt 0 ]]; then
  shift
fi

case "$command" in
  env)
    for key in "$@"; do
      case "$key" in
        GOROOT) printf '%s\n' "$FAKE_GOROOT" ;;
        GOVERSION) printf 'go1.25.1\n' ;;
        GOHOSTOS) printf '%s\n' "${FAKE_GOHOSTOS:-linux}" ;;
        GOHOSTARCH) printf '%s\n' "${FAKE_GOHOSTARCH:-amd64}" ;;
        *) printf '\n' ;;
      esac
    done
    ;;
  version)
    printf 'go version go1.25.1 fake/fake\n'
    ;;
  tool)
    if [[ ${1:-} == compile && ${2:-} == -V ]]; then
      printf 'compile version go1.25.1\n'
      exit 0
    fi
    printf 'unexpected fake go tool invocation: %s\n' "$*" >&2
    exit 90
    ;;
  build)
    {
      printf 'CALL=build\n'
      printf 'ENV_GOOS=%s\n' "${GOOS:-}"
      printf 'ENV_GOARCH=%s\n' "${GOARCH:-}"
      printf 'ENV_CGO_ENABLED=%s\n' "${CGO_ENABLED:-}"
      for arg in "$@"; do
        printf 'ARG=%s\n' "$arg"
      done
    } >>"$FAKE_GO_LOG"

    output=
    while [[ $# -gt 0 ]]; do
      case "$1" in
        -o)
          output=${2:-}
          shift 2
          ;;
        -o=*)
          output=${1#-o=}
          shift
          ;;
        *)
          shift
          ;;
      esac
    done
    if [[ -z "$output" ]]; then
      printf 'fake go build did not receive -o\n' >&2
      exit 91
    fi
    mkdir -p -- "$(dirname -- "$output")"
    printf 'fake agent-compose binary\n' >"$output"
    chmod +x "$output"
    ;;
  *)
    printf 'unexpected fake go command: %s\n' "$command" >&2
    exit 92
    ;;
esac
EOF
chmod +x "$FAKE_GO"
ln -s "$FAKE_GO" "$FAKE_GOROOT/bin/go"

for guarded_command in curl wget docker; do
  guard="$FAKE_BIN/$guarded_command"
  cat >"$guard" <<EOF
#!/usr/bin/env bash
printf 'unexpected network/tool invocation: $guarded_command\n' >&2
exit 93
EOF
  chmod +x "$guard"
done

FAKE_GOHOSTOS=linux
FAKE_GOHOSTARCH=amd64
TEST_BUILD_VERBOSE=
RUN_STDOUT="$TMP_DIR/stdout"
RUN_STDERR="$TMP_DIR/stderr"
RUN_STATUS=0

run_helper() {
  : >"$RUN_STDOUT"
  : >"$RUN_STDERR"
  : >"$FAKE_GO_LOG"
  set +e
  env \
    PATH="$FAKE_BIN:$PATH" \
    GO="$FAKE_GO" \
    FAKE_GOROOT="$FAKE_GOROOT" \
    FAKE_GOHOSTOS="$FAKE_GOHOSTOS" \
    FAKE_GOHOSTARCH="$FAKE_GOHOSTARCH" \
    FAKE_GO_LOG="$FAKE_GO_LOG" \
    BUILD_VERBOSE="$TEST_BUILD_VERBOSE" \
    HTTP_PROXY='http://proxy-user:proxy-secret@example.invalid:8080' \
    HTTPS_PROXY='http://proxy-user:proxy-secret@example.invalid:8080' \
    ALL_PROXY='socks5://proxy-user:proxy-secret@example.invalid:1080' \
    bash "$HELPER" "$@" >"$RUN_STDOUT" 2>"$RUN_STDERR"
  RUN_STATUS=$?
  set -e
  for output_file in "$RUN_STDOUT" "$RUN_STDERR"; do
    if grep -F -- 'proxy-secret@example.invalid' "$output_file" >/dev/null; then
      printf 'credential leak in %s:\n' "$output_file" >&2
      sed 's/^/  /' "$output_file" >&2
      fail "helper output exposed proxy credentials"
    fi
  done
}

darwin_auto_config=$(printf '%s\n' \
  'profile=darwin-docker' \
  'goos=darwin' \
  'goarch=arm64' \
  'cgo_enabled=0' \
  'tags=netgo,osusergo' \
  'compiled_drivers=docker' \
  'version=auto')

darwin_print_config=$(printf '%s\n' \
  'profile=darwin-docker' \
  'goos=darwin' \
  'goarch=arm64' \
  'cgo_enabled=0' \
  'tags=netgo,osusergo' \
  'compiled_drivers=docker' \
  'version=print-only')

linux_config=$(printf '%s\n' \
  'profile=linux-full' \
  'goos=linux' \
  'goarch=amd64' \
  'cgo_enabled=1' \
  'tags=netgo,osusergo,boxlitecgo,microsandboxcgo,k8scompose' \
  'compiled_drivers=docker,boxlite,microsandbox,k8s' \
  'version=auto')

# Explicit Darwin builds must not inspect or require native runtime artifacts.
darwin_output="$TEST_ROOT/out/darwin/agent-compose"
run_helper \
  --profile darwin-docker \
  --goarch amd64 \
  --output "$darwin_output" \
  --version test-darwin
assert_success
[[ -x "$darwin_output" ]] || fail "Darwin output was not created"
assert_contains "$FAKE_GO_LOG" 'CALL=build'
assert_contains "$FAKE_GO_LOG" 'ENV_GOOS=darwin'
assert_contains "$FAKE_GO_LOG" 'ENV_GOARCH=amd64'
assert_contains "$FAKE_GO_LOG" 'ENV_CGO_ENABLED=0'
assert_build_arg 'netgo,osusergo'
assert_build_arg '-X github.com/chaitin/agent-compose/pkg/config.BuildVersion=test-darwin'
assert_build_arg "$darwin_output"
assert_build_arg './cmd/agent-compose/'
assert_not_contains "$FAKE_GO_LOG" 'ARG=-x'

# Relative outputs are rooted at the repository, independent of caller cwd.
relative_output='out/relative/agent-compose'
run_helper \
  --profile darwin-docker \
  --goarch amd64 \
  --output "$relative_output" \
  --version relative
assert_success
[[ -x "$TEST_ROOT/$relative_output" ]] || fail "relative output was not created under repository root"
assert_build_arg "$relative_output"

# linux-full reports every missing artifact and must not invoke go build.
linux_output="$TEST_ROOT/out/linux/agent-compose"
run_helper \
  --profile linux-full \
  --goarch amd64 \
  --output "$linux_output" \
  --version test-linux
assert_failure
assert_no_build
[[ ! -e "$(dirname -- "$linux_output")" ]] || fail "preflight failure created the output directory"
for missing_path in \
  build/boxlite/include/boxlite.h \
  build/boxlite/lib/libboxlite.so \
  build/boxlite/runtime/boxlite-guest \
  build/boxlite/runtime/boxlite-shim \
  build/microsandbox/bin/msb \
  build/microsandbox/bin/agentd \
  build/microsandbox/lib/libkrunfw.so \
  build/microsandbox/lib/libmicrosandbox_go_ffi.so; do
  assert_contains "$RUN_STDERR" "$missing_path"
done
assert_not_contains "$RUN_STDERR" 'proxy-secret'

# Configuration inspection stays available before artifact preparation.
run_helper \
  --profile linux-full \
  --goarch amd64 \
  --output "$linux_output" \
  --version auto \
  --print-config
assert_success
assert_output_equals "$linux_config"
assert_no_build

# Populate exactly the artifact owner contract, including executable binaries.
for artifact in \
  build/boxlite/include/boxlite.h \
  build/boxlite/lib/libboxlite.so \
  build/microsandbox/lib/libkrunfw.so \
  build/microsandbox/lib/libmicrosandbox_go_ffi.so; do
  mkdir -p -- "$TEST_ROOT/$(dirname -- "$artifact")"
  printf 'fixture\n' >"$TEST_ROOT/$artifact"
done
for artifact in \
  build/boxlite/runtime/boxlite-guest \
  build/boxlite/runtime/boxlite-shim \
  build/microsandbox/bin/msb \
  build/microsandbox/bin/agentd; do
  mkdir -p -- "$TEST_ROOT/$(dirname -- "$artifact")"
  printf '#!/usr/bin/env sh\nexit 0\n' >"$TEST_ROOT/$artifact"
  chmod +x "$TEST_ROOT/$artifact"
done

run_helper \
  --profile linux-full \
  --goarch amd64 \
  --output "$linux_output" \
  --version test-linux
assert_success
[[ -x "$linux_output" ]] || fail "Linux output was not created"
assert_contains "$FAKE_GO_LOG" 'ENV_GOOS=linux'
assert_contains "$FAKE_GO_LOG" 'ENV_GOARCH=amd64'
assert_contains "$FAKE_GO_LOG" 'ENV_CGO_ENABLED=1'
assert_build_arg 'netgo,osusergo,boxlitecgo,microsandboxcgo,k8scompose'
assert_build_arg '-X github.com/chaitin/agent-compose/pkg/config.BuildVersion=test-linux'

# auto dispatch uses the Go host OS and defaults architecture from GOHOSTARCH.
FAKE_GOHOSTOS=darwin
FAKE_GOHOSTARCH=arm64
auto_darwin_output="$TEST_ROOT/out/auto-darwin"
run_helper --profile auto --output "$auto_darwin_output" --version auto --print-config
assert_success
assert_output_equals "$darwin_auto_config"
assert_no_build

FAKE_GOHOSTOS=linux
FAKE_GOHOSTARCH=amd64
auto_linux_output="$TEST_ROOT/out/auto-linux"
run_helper --output "$auto_linux_output" --version auto --print-config
assert_success
assert_output_equals "$linux_config"
assert_no_build

# --print-config is deterministic and never builds, but output remains required.
run_helper \
  --profile darwin-docker \
  --goarch arm64 \
  --output "$TEST_ROOT/out/print-only" \
  --version print-only \
  --print-config
assert_success
assert_output_equals "$darwin_print_config"
assert_no_build

# Only BUILD_VERBOSE=1 enables go build -x.
TEST_BUILD_VERBOSE=1
run_helper \
  --profile darwin-docker \
  --goarch arm64 \
  --output "$TEST_ROOT/out/verbose" \
  --version verbose
assert_success
assert_build_arg '-x'

TEST_BUILD_VERBOSE=true
run_helper \
  --profile darwin-docker \
  --goarch arm64 \
  --output "$TEST_ROOT/out/not-verbose" \
  --version not-verbose
assert_success
assert_not_contains "$FAKE_GO_LOG" 'ARG=-x'
TEST_BUILD_VERBOSE=

# Missing --version uses the deterministic git fallback without a network call.
run_helper \
  --profile darwin-docker \
  --goarch amd64 \
  --output "$TEST_ROOT/out/fallback-version"
assert_success
assert_build_arg '-X github.com/chaitin/agent-compose/pkg/config.BuildVersion=unknown'

# Parser, target, and security failures happen before go build.
run_helper --help
assert_success
assert_contains "$RUN_STDOUT" 'usage: build-agent-compose-binary.sh'
assert_no_build

run_helper --profile future --goarch amd64 --output "$TEST_ROOT/out/bad-profile" --version bad
assert_status 2
assert_contains "$RUN_STDERR" 'unknown profile'
assert_no_build

run_helper --profile darwin-docker --goarch 386 --output "$TEST_ROOT/out/bad-arch" --version bad
assert_status 2
assert_contains "$RUN_STDERR" 'unsupported architecture'
assert_no_build

run_helper --profile darwin-docker --goarch amd64 --output '' --version bad
assert_status 2
assert_contains "$RUN_STDERR" '--output must not be empty'
assert_no_build

run_helper --profile
assert_status 2
assert_contains "$RUN_STDERR" '--profile requires a value'
assert_no_build

run_helper --unknown-option --output "$TEST_ROOT/out/unknown" --version bad
assert_status 2
assert_contains "$RUN_STDERR" 'unknown argument'
assert_no_build

FAKE_GOHOSTOS=windows
FAKE_GOHOSTARCH=amd64
run_helper --output "$TEST_ROOT/out/windows" --version bad --print-config
assert_status 2
assert_contains "$RUN_STDERR" 'unsupported host OS'
assert_no_build
FAKE_GOHOSTOS=linux

FAKE_GOHOSTOS=darwin
run_helper \
  --profile linux-full \
  --goarch amd64 \
  --output "$TEST_ROOT/out/linux-on-darwin" \
  --version bad
assert_status 2
assert_contains "$RUN_STDERR" 'linux-full requires a Linux host'
assert_no_build
FAKE_GOHOSTOS=linux

newline_version=$'release\nproxy-secret-must-not-be-echoed'
run_helper \
  --profile darwin-docker \
  --goarch amd64 \
  --output "$TEST_ROOT/out/newline" \
  --version "$newline_version"
assert_status 2
assert_contains "$RUN_STDERR" '--version must not contain a newline'
assert_not_contains "$RUN_STDERR" 'proxy-secret-must-not-be-echoed'
assert_no_build

quoted_version="unsafe 'single' and \"double\""
run_helper \
  --profile darwin-docker \
  --goarch amd64 \
  --output "$TEST_ROOT/out/quoted-version" \
  --version "$quoted_version"
assert_status 2
assert_contains "$RUN_STDERR" 'must not contain both quote characters'
assert_not_contains "$RUN_STDERR" "$quoted_version"
assert_no_build

for error_file in "$RUN_STDOUT" "$RUN_STDERR"; do
  assert_not_contains "$error_file" 'proxy-user'
  assert_not_contains "$error_file" 'proxy-secret@example.invalid'
done

# Arguments containing shell metacharacters remain single values and are not run.
injection_marker="$TMP_DIR/injection-ran"
literal_output="$TEST_ROOT/out/literal;output"
injection_version="release; touch $injection_marker"
run_helper \
  --profile darwin-docker \
  --goarch amd64 \
  --output "$literal_output" \
  --version "$injection_version"
assert_success
[[ -x "$literal_output" ]] || fail "literal metacharacter output was not created"
[[ ! -e "$injection_marker" ]] || fail "version text was executed as shell input"
assert_build_arg "-X 'github.com/chaitin/agent-compose/pkg/config.BuildVersion=$injection_version'"

# Artifact exporter stamps bind complete fixtures to source inputs and target
# architecture without invoking Docker. A source or architecture change must
# invalidate the cached set.
for driver_and_dir in \
  'boxlite build/boxlite' \
  'microsandbox build/microsandbox'; do
  driver=${driver_and_dir%% *}
  artifact_dir=${driver_and_dir#* }
  env \
    PATH="$FAKE_BIN:$PATH" \
    GO="$FAKE_GO" \
    FAKE_GOROOT="$FAKE_GOROOT" \
    TARGETARCH=amd64 \
    AGENT_COMPOSE_ADOPT_EXISTING_ARTIFACTS=1 \
    "$TEST_ROOT/scripts/export-runtime-dev-artifact.sh" "$driver" "$TEST_ROOT/$artifact_dir" >/dev/null

  env \
    PATH="$FAKE_BIN:$PATH" \
    GO="$FAKE_GO" \
    FAKE_GOROOT="$FAKE_GOROOT" \
    TARGETARCH=amd64 \
    AGENT_COMPOSE_ARTIFACT_STATUS_ONLY=1 \
    "$TEST_ROOT/scripts/export-runtime-dev-artifact.sh" "$driver" "$TEST_ROOT/$artifact_dir"

  env \
    PATH="$FAKE_BIN:$PATH" \
    GO="$FAKE_GO" \
    FAKE_GOROOT="$FAKE_GOROOT" \
    TARGETARCH=amd64 \
    "$TEST_ROOT/scripts/export-runtime-dev-artifact.sh" "$driver" "$TEST_ROOT/$artifact_dir" >/dev/null

  if env \
    PATH="$FAKE_BIN:$PATH" \
    GO="$FAKE_GO" \
    FAKE_GOROOT="$FAKE_GOROOT" \
    TARGETARCH=arm64 \
    AGENT_COMPOSE_ARTIFACT_STATUS_ONLY=1 \
    "$TEST_ROOT/scripts/export-runtime-dev-artifact.sh" "$driver" "$TEST_ROOT/$artifact_dir"; then
    fail "$driver exporter accepted an artifact stamp for the wrong architecture"
  fi
done

printf '# fingerprint change\n' >>"$TEST_ROOT/Dockerfile"
for driver_and_dir in \
  'boxlite build/boxlite' \
  'microsandbox build/microsandbox'; do
  driver=${driver_and_dir%% *}
  artifact_dir=${driver_and_dir#* }
  if env \
    PATH="$FAKE_BIN:$PATH" \
    GO="$FAKE_GO" \
    FAKE_GOROOT="$FAKE_GOROOT" \
    TARGETARCH=amd64 \
    AGENT_COMPOSE_ARTIFACT_STATUS_ONLY=1 \
    "$TEST_ROOT/scripts/export-runtime-dev-artifact.sh" "$driver" "$TEST_ROOT/$artifact_dir"; then
    fail "$driver exporter accepted an artifact stamp after its source changed"
  fi
done

# Docker-backed exporter invocations use Dockerfile defaults when overrides are
# empty and forward every non-empty override without changing its value.
FAKE_DOCKER_LOG="$TMP_DIR/fake-docker.log"
FAKE_DOCKER_STATE="$TMP_DIR/fake-docker-state"
cat >"$FAKE_BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

command=${1:-}
shift || true
case "$command" in
  build)
    iidfile=
    target=
    while [[ $# -gt 0 ]]; do
      printf 'ARG=%s\n' "$1" >>"$FAKE_DOCKER_LOG"
      case "$1" in
        --iidfile)
          iidfile=${2:-}
          shift 2
          ;;
        --target)
          target=${2:-}
          shift 2
          ;;
        *)
          shift
          ;;
      esac
    done
    [[ -n "$iidfile" && -n "$target" ]]
    printf 'fake-image\n' >"$iidfile"
    printf '%s\n' "$target" >"$FAKE_DOCKER_STATE"
    ;;
  create)
    printf 'fake-container\n'
    ;;
  cp)
    destination=${2:?}
    case "$(<"$FAKE_DOCKER_STATE")" in
      boxlite-build)
        mkdir -p "$destination/include" "$destination/lib" "$destination/runtime"
        printf 'fixture\n' >"$destination/include/boxlite.h"
        printf 'fixture\n' >"$destination/lib/libboxlite.so"
        printf '#!/usr/bin/env sh\n' >"$destination/runtime/boxlite-guest"
        printf '#!/usr/bin/env sh\n' >"$destination/runtime/boxlite-shim"
        chmod +x "$destination/runtime/boxlite-guest" "$destination/runtime/boxlite-shim"
        ;;
      microsandbox-fetch)
        mkdir -p "$destination/bin" "$destination/lib"
        printf '#!/usr/bin/env sh\n' >"$destination/bin/msb"
        printf '#!/usr/bin/env sh\n' >"$destination/bin/agentd"
        chmod +x "$destination/bin/msb" "$destination/bin/agentd"
        printf 'fixture\n' >"$destination/lib/libkrunfw.so"
        printf 'fixture\n' >"$destination/lib/libmicrosandbox_go_ffi.so"
        ;;
      *)
        exit 94
        ;;
    esac
    ;;
  rm)
    ;;
  *)
    printf 'unexpected fake docker command: %s\n' "$command" >&2
    exit 95
    ;;
esac
EOF
chmod +x "$FAKE_BIN/docker"

run_exporter() {
  local driver=$1
  local output=$2
  : >"$RUN_STDOUT"
  : >"$RUN_STDERR"
  : >"$FAKE_DOCKER_LOG"
  rm -f "$FAKE_DOCKER_STATE"
  set +e
  env \
    PATH="$FAKE_BIN:$PATH" \
    GO="$FAKE_GO" \
    FAKE_GOROOT="$FAKE_GOROOT" \
    FAKE_DOCKER_LOG="$FAKE_DOCKER_LOG" \
    FAKE_DOCKER_STATE="$FAKE_DOCKER_STATE" \
    TARGETARCH="${EXPORT_TARGETARCH:-amd64}" \
    HTTP_PROXY="${EXPORT_HTTP_PROXY:-}" \
    http_proxy= \
    HTTPS_PROXY="${EXPORT_HTTPS_PROXY:-}" \
    https_proxy= \
    ALL_PROXY="${EXPORT_ALL_PROXY:-}" \
    all_proxy= \
    NO_PROXY="${EXPORT_NO_PROXY:-}" \
    no_proxy= \
    REGISTRY_MIRROR="${EXPORT_REGISTRY_MIRROR:-}" \
    GITHUB_MIRROR="${EXPORT_GITHUB_MIRROR:-}" \
    BOXLITE_VERSION="${EXPORT_BOXLITE_VERSION:-}" \
    MICROSANDBOX_VERSION="${EXPORT_MICROSANDBOX_VERSION:-}" \
    "$TEST_ROOT/scripts/export-runtime-dev-artifact.sh" "$driver" "$output" \
    >"$RUN_STDOUT" 2>"$RUN_STDERR"
  RUN_STATUS=$?
  set -e
}

EXPORT_TARGETARCH=amd64
EXPORT_HTTP_PROXY=
EXPORT_HTTPS_PROXY=
EXPORT_ALL_PROXY=
EXPORT_NO_PROXY=
EXPORT_REGISTRY_MIRROR=
EXPORT_GITHUB_MIRROR=
EXPORT_BOXLITE_VERSION=
EXPORT_MICROSANDBOX_VERSION=
run_exporter boxlite "$TEST_ROOT/export-defaults"
assert_success
assert_contains "$FAKE_DOCKER_STATE" 'boxlite-build'
assert_contains "$FAKE_DOCKER_LOG" 'ARG=TARGETARCH=amd64'
for omitted in HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY REGISTRY_MIRROR GITHUB_MIRROR BOXLITE_VERSION MICROSANDBOX_VERSION; do
  assert_not_contains "$FAKE_DOCKER_LOG" "ARG=$omitted="
done

EXPORT_HTTP_PROXY='http://http-proxy.invalid:8080'
EXPORT_HTTPS_PROXY='http://https-proxy.invalid:8443'
EXPORT_ALL_PROXY='socks5://all-proxy.invalid:1080'
EXPORT_NO_PROXY='localhost,.example.invalid'
EXPORT_REGISTRY_MIRROR='registry.example.invalid'
EXPORT_GITHUB_MIRROR='https://github.example.invalid'
EXPORT_MICROSANDBOX_VERSION='v9.9.9'
run_exporter microsandbox "$TEST_ROOT/export-overrides"
assert_success
assert_contains "$FAKE_DOCKER_STATE" 'microsandbox-fetch'
for forwarded in \
  'HTTP_PROXY=http://http-proxy.invalid:8080' \
  'HTTPS_PROXY=http://https-proxy.invalid:8443' \
  'ALL_PROXY=socks5://all-proxy.invalid:1080' \
  'NO_PROXY=localhost,.example.invalid' \
  'REGISTRY_MIRROR=registry.example.invalid' \
  'GITHUB_MIRROR=https://github.example.invalid' \
  'MICROSANDBOX_VERSION=v9.9.9'; do
  assert_contains "$FAKE_DOCKER_LOG" "ARG=$forwarded"
done
assert_not_contains "$FAKE_DOCKER_LOG" 'ARG=BOXLITE_VERSION='

EXPORT_TARGETARCH=amd64
EXPORT_BOXLITE_VERSION='v8.8.8'
EXPORT_MICROSANDBOX_VERSION=
run_exporter boxlite "$TEST_ROOT/export-defaults"
assert_success
assert_contains "$FAKE_DOCKER_LOG" 'ARG=BOXLITE_VERSION=v8.8.8'
assert_contains "$TEST_ROOT/export-defaults/.agent-compose-artifact-source" \
  'runtime_version_override=v8.8.8'

run_exporter future "$TEST_ROOT/export-unknown"
assert_status 2
assert_contains "$RUN_STDERR" 'unknown runtime driver: future'

EXPORT_TARGETARCH=386
run_exporter boxlite "$TEST_ROOT/export-bad-arch"
assert_status 2
assert_contains "$RUN_STDERR" 'unsupported BoxLite target arch: 386'

printf 'test-build-agent-compose-binary: all checks passed\n'
