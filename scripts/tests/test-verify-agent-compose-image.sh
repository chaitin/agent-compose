#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
VERIFIER="$ROOT_DIR/scripts/verify-agent-compose-image.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT

fail() {
  printf 'test-verify-agent-compose-image: %s\n' "$*" >&2
  exit 1
}

FAKE_BIN="$TEST_ROOT/bin"
FAKE_LOG="$TEST_ROOT/docker.log"
FAKE_SEQUENCE="$TEST_ROOT/docker.sequence"
FAKE_STATE="$TEST_ROOT/state"
mkdir -p "$FAKE_BIN" "$FAKE_STATE"

cat >"$FAKE_BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

{
  printf 'host=%s|args=' "${DOCKER_HOST-<unset>}"
  for argument in "$@"; do
    argument=${argument//$'\n'/\\n}
    printf '[%s]' "$argument"
  done
  printf '\n'
} >>"$FAKE_DOCKER_LOG"

mode=${FAKE_DOCKER_MODE:-success}
case "${1:-}:${2:-}" in
  image:inspect)
    printf 'image-inspect\n' >>"$FAKE_DOCKER_SEQUENCE"
    [[ $mode != image-inspect-fail ]] || exit 40
    os=linux
    arch=amd64
    runtime_driver=docker
    boxlite_runtime_dir=/app/boxlite/runtime
    [[ $mode != bad-image-os ]] || os=darwin
    [[ $mode != bad-image-arch ]] || arch=arm64
    [[ $mode != bad-image-env ]] || runtime_driver=boxlite
    [[ $mode != bad-image-path ]] || boxlite_runtime_dir=/wrong/boxlite
    jq -cn \
      --arg os "$os" \
      --arg arch "$arch" \
      --arg runtime_driver "$runtime_driver" \
      --arg boxlite_runtime_dir "$boxlite_runtime_dir" \
      '{Os:$os, Architecture:$arch, Config:{Env:[
        ("RUNTIME_DRIVER=" + $runtime_driver),
        ("BOXLITE_RUNTIME_DIR=" + $boxlite_runtime_dir),
        "MICROSANDBOX_HOME=/data/microsandbox",
        "MICROSANDBOX_MSB_PATH=/app/microsandbox/bin/msb",
        "MICROSANDBOX_LIB_PATH=/app/microsandbox/lib/libmicrosandbox_go_ffi.so",
        "LD_LIBRARY_PATH=/app/boxlite/lib:/app/boxlite/runtime:/app/microsandbox/lib"
      ]}}'
    ;;
  create:*)
    name=''
    entrypoint=''
    previous=''
    for argument in "$@"; do
      if [[ $previous == --name ]]; then name=$argument; fi
      if [[ $previous == --entrypoint ]]; then entrypoint=$argument; fi
      previous=$argument
    done
    [[ -n $name && -n $entrypoint ]] || exit 91
    if [[ $entrypoint == /app/agent-compose ]]; then
      printf 'create-version\n' >>"$FAKE_DOCKER_SEQUENCE"
      [[ $mode != create-version-fail ]] || exit 43
    else
      printf 'create-runtime\n' >>"$FAKE_DOCKER_SEQUENCE"
      [[ $mode != create-runtime-fail ]] || exit 44
    fi
    : >"$FAKE_DOCKER_STATE/$name"
    printf 'fake-container-id\n'
    ;;
  start:--attach)
    name=${3:-}
    [[ -f $FAKE_DOCKER_STATE/$name ]] || exit 92
    if [[ $name == *-version ]]; then
      printf 'start-version\n' >>"$FAKE_DOCKER_SEQUENCE"
      [[ $mode != version-start-fail ]] || exit 41
      if [[ $mode == bad-version ]]; then
        printf '%s\n' '{"version":"fake","os":"linux","arch":"amd64","compiled_drivers":["docker"]}'
      else
        printf '%s\n' '{"version":"fake","os":"linux","arch":"amd64","compiled_drivers":["docker","boxlite","microsandbox","k8s"]}'
      fi
    else
      printf 'start-runtime\n' >>"$FAKE_DOCKER_SEQUENCE"
      [[ $mode != runtime-start-fail ]] || exit 42
    fi
    ;;
  container:inspect)
    printf 'inspect-runtime\n' >>"$FAKE_DOCKER_SEQUENCE"
    if [[ $mode == bad-security ]]; then
      printf '%s\n' '{"Privileged":true,"Devices":[],"DeviceRequests":[]}'
    else
      printf '%s\n' '{"Privileged":false,"Devices":[],"DeviceRequests":[]}'
    fi
    ;;
  rm:-f)
    name=${3:-}
    if [[ $name == *-version ]]; then
      printf 'rm-version\n' >>"$FAKE_DOCKER_SEQUENCE"
    else
      printf 'rm-runtime\n' >>"$FAKE_DOCKER_SEQUENCE"
    fi
    rm -f "$FAKE_DOCKER_STATE/$name"
    ;;
  *)
    exit 93
    ;;
esac
EOF
chmod +x "$FAKE_BIN/docker"

run_verifier() { # $1=mode
  local mode=$1
  : >"$FAKE_LOG"
  : >"$FAKE_SEQUENCE"
  rm -f "$FAKE_STATE"/*
  set +e
  PATH="$FAKE_BIN:$PATH" \
    DOCKER_HOST=unix:///fake/docker.sock \
    FAKE_DOCKER_LOG="$FAKE_LOG" \
    FAKE_DOCKER_SEQUENCE="$FAKE_SEQUENCE" \
    FAKE_DOCKER_STATE="$FAKE_STATE" \
    FAKE_DOCKER_MODE="$mode" \
    VERIFY_AGENT_COMPOSE_IMAGE_RUN_ID=test-run \
    "$VERIFIER" --image registry.example/agent-compose:test --arch amd64 \
      >"$TEST_ROOT/stdout" 2>"$TEST_ROOT/stderr"
  RUN_STATUS=$?
  set -e
}

assert_no_state() {
  [[ -z $(find "$FAKE_STATE" -mindepth 1 -print -quit) ]] \
    || fail 'verifier left a fake container behind'
}

run_verifier success
[[ $RUN_STATUS -eq 0 ]] || fail 'success case failed'
expected_sequence=$'image-inspect\ncreate-version\nstart-version\nrm-version\ncreate-runtime\ninspect-runtime\nstart-runtime\nrm-runtime'
actual_sequence=$(<"$FAKE_SEQUENCE")
[[ $actual_sequence == "$expected_sequence" ]] \
  || fail "Docker command sequence differs: $actual_sequence"
if grep -Fv 'host=unix:///fake/docker.sock|' "$FAKE_LOG" | grep -q .; then
  fail 'a Docker command did not inherit DOCKER_HOST'
fi
grep -F 'args=[image][inspect][--format][{{json .}}][registry.example/agent-compose:test]' "$FAKE_LOG" >/dev/null \
  || fail 'image inspect argv differs'
grep -F '[--entrypoint][/app/agent-compose][registry.example/agent-compose:test][--json][version]' "$FAKE_LOG" >/dev/null \
  || fail 'version container argv differs'
grep -F '[--entrypoint][/bin/sh][registry.example/agent-compose:test][-ec]' "$FAKE_LOG" >/dev/null \
  || fail 'runtime container argv differs'
grep -F 'test ! -e /dev/kvm' "$FAKE_LOG" >/dev/null \
  || fail 'runtime container does not assert absent /dev/kvm'
if grep -Eq '\[--privileged\]|\[--device([=\]])' "$FAKE_LOG"; then
  fail 'verifier requested privileged mode or a host device'
fi
assert_no_state

for mode in \
  image-inspect-fail \
  bad-image-os \
  bad-image-arch \
  bad-image-env \
  bad-image-path \
  create-version-fail \
  version-start-fail \
  bad-version \
  create-runtime-fail \
  bad-security \
  runtime-start-fail; do
  run_verifier "$mode"
  [[ $RUN_STATUS -ne 0 ]] || fail "$mode unexpectedly succeeded"
  assert_no_state
done

printf 'test-verify-agent-compose-image: all checks passed\n'
