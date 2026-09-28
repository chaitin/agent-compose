#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT

FAKE_BIN="$TEST_ROOT/bin"
DOCKER_LOG="$TEST_ROOT/docker.log"
mkdir -p "$FAKE_BIN"

cat >"$FAKE_BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ ${1:-} == build ]]
shift
printf '%s\n' "$@" >"$DOCKER_LOG"
EOF
chmod +x "$FAKE_BIN/docker"

fail() {
  printf 'test-task-build-variables: %s\n' "$*" >&2
  exit 1
}

require_line() { # $1=expected line
  grep -Fqx -- "$1" "$DOCKER_LOG" || fail "missing Docker argument: $1"
}

clear_build_environment=(
  -u HTTP_PROXY -u http_proxy
  -u HTTPS_PROXY -u https_proxy
  -u ALL_PROXY -u all_proxy
  -u NO_PROXY -u no_proxy
  -u REGISTRY_MIRROR -u GOPROXY -u NPM_CONFIG_REGISTRY
  -u PIP_INDEX_URL -u PIP_TRUSTED_HOST
  -u GO_VERSION -u GRPCURL_VERSION -u NODE_MAJOR
  -u ARCHLINUX_TAG -u ARCHLINUX_MIRROR
  -u CODEX_VERSION -u CLAUDE_CODE_VERSION
  -u OPENCODE_VERSION -u PI_AGENT_VERSION -u PI_MCP_ADAPTER_VERSION
  -u DSH_VERSION
  -u IMAGE_TAG -u DOCKER_DEFAULT_PLATFORM -u NO_CACHE
)

common_values=(
  http_proxy=http://http-proxy.invalid:8080
  HTTPS_PROXY=http://https-proxy.invalid:8443
  all_proxy=socks5://all-proxy.invalid:1080
  no_proxy=localhost,.example.invalid
  REGISTRY_MIRROR=registry.example.invalid
  GOPROXY=https://go-proxy.example.invalid,direct
  NPM_CONFIG_REGISTRY=https://npm.example.invalid
  PIP_INDEX_URL=https://python.example.invalid/simple
  PIP_TRUSTED_HOST=python.example.invalid
  GO_VERSION=1.99.0
  GRPCURL_VERSION=v9.9.1
  NODE_MAJOR=99
  ARCHLINUX_TAG=base-test
  ARCHLINUX_MIRROR=https://arch.example.invalid
  CODEX_VERSION=9.1.0
  CLAUDE_CODE_VERSION=9.2.0
  OPENCODE_VERSION=9.4.0
  PI_AGENT_VERSION=9.5.0
  PI_MCP_ADAPTER_VERSION=9.6.0
  DSH_VERSION=9.7.0
  IMAGE_TAG=example.invalid/agent-compose-guest:contract
  DOCKER_DEFAULT_PLATFORM=linux/arm64
  NO_CACHE=1
)

run_with_task_variables() {
  : >"$DOCKER_LOG"
  env "${clear_build_environment[@]}" \
    PATH="$FAKE_BIN:$PATH" \
    DOCKER_LOG="$DOCKER_LOG" \
    task --dir "$ROOT_DIR" image:agent-compose-guest "${common_values[@]}" >/dev/null
}

run_with_environment() {
  : >"$DOCKER_LOG"
  env "${clear_build_environment[@]}" \
    PATH="$FAKE_BIN:$PATH" \
    DOCKER_LOG="$DOCKER_LOG" \
    "${common_values[@]}" \
    task --dir "$ROOT_DIR" image:agent-compose-guest >/dev/null
}

run_with_task_variables
cp "$DOCKER_LOG" "$TEST_ROOT/task-variables.log"

for forwarded in \
  'HTTP_PROXY=http://http-proxy.invalid:8080' \
  'HTTPS_PROXY=http://https-proxy.invalid:8443' \
  'ALL_PROXY=socks5://all-proxy.invalid:1080' \
  'NO_PROXY=localhost,.example.invalid' \
  'REGISTRY_MIRROR=registry.example.invalid' \
  'GOPROXY=https://go-proxy.example.invalid,direct' \
  'NPM_CONFIG_REGISTRY=https://npm.example.invalid' \
  'PIP_INDEX_URL=https://python.example.invalid/simple' \
  'PIP_TRUSTED_HOST=python.example.invalid' \
  'GO_VERSION=1.99.0' \
  'GRPCURL_VERSION=v9.9.1' \
  'NODE_MAJOR=99' \
  'CODEX_VERSION=9.1.0' \
  'CLAUDE_CODE_VERSION=9.2.0' \
  'OPENCODE_VERSION=9.4.0' \
  'PI_AGENT_VERSION=9.5.0' \
  'PI_MCP_ADAPTER_VERSION=9.6.0' \
  'DSH_VERSION=9.7.0'; do
  require_line "$forwarded"
done
require_line '--platform'
require_line 'linux/arm64'
require_line '--no-cache'
require_line 'example.invalid/agent-compose-guest:contract'

run_with_environment
if ! cmp -s "$TEST_ROOT/task-variables.log" "$DOCKER_LOG"; then
  diff -u "$TEST_ROOT/task-variables.log" "$DOCKER_LOG" >&2 || true
  fail 'task variables and environment variables produced different Docker arguments'
fi

if grep -Eq '^(BUILD_PLATFORM|BUILD_GOPROXY|PYPI_INDEX_URL|PYPI_TRUSTED_HOST)=' "$DOCKER_LOG"; then
  fail 'non-standard build variable reached Docker'
fi

: >"$DOCKER_LOG"
env "${clear_build_environment[@]}" \
  PATH="$FAKE_BIN:$PATH" \
  DOCKER_LOG="$DOCKER_LOG" \
  task --dir "$ROOT_DIR" image:agent-compose-guest-archlinux \
    ARCHLINUX_TAG=base-test \
    ARCHLINUX_MIRROR=https://arch.example.invalid \
    NODE_MAJOR=99 >/dev/null
require_line './guest-images/Dockerfile.agent-compose-guest-archlinux'
require_line 'ARCHLINUX_TAG=base-test'
require_line 'ARCHLINUX_MIRROR=https://arch.example.invalid'
require_line 'linux/amd64'
if grep -Fqx 'NODE_MAJOR=99' "$DOCKER_LOG"; then
  fail 'Debian-only NODE_MAJOR reached the Arch Linux guest build'
fi

printf 'test-task-build-variables: all checks passed\n'
