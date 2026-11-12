#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)

usage() {
  cat <<'EOF'
Usage: export-runtime-dev-artifact.sh <boxlite|microsandbox> [OUT_DIR]

Export native runtime development artifacts from the repository Dockerfile.
EOF
}

driver=${1:-}
case "$driver" in
  boxlite)
    DISPLAY_NAME=BoxLite
    DOCKER_TARGET=boxlite-build
    DEFAULT_OUT_DIR="$ROOT_DIR/build/boxlite"
    REQUIRED_FILES=(
      include/boxlite.h
      lib/libboxlite.so
    )
    REQUIRED_EXECUTABLES=(
      runtime/boxlite-guest
      runtime/boxlite-shim
    )
    CLEAN_DIRS=(include lib runtime)
    RUNTIME_VERSION_OVERRIDE=${BOXLITE_VERSION:-}
    ;;
  microsandbox)
    DISPLAY_NAME=Microsandbox
    DOCKER_TARGET=microsandbox-fetch
    DEFAULT_OUT_DIR="$ROOT_DIR/build/microsandbox"
    REQUIRED_FILES=(
      lib/libkrunfw.so
      lib/libmicrosandbox_go_ffi.so
    )
    REQUIRED_EXECUTABLES=(
      bin/msb
      bin/agentd
    )
    CLEAN_DIRS=(bin lib)
    RUNTIME_VERSION_OVERRIDE=${MICROSANDBOX_VERSION:-}
    ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    if [[ -n "$driver" ]]; then
      printf 'unknown runtime driver: %s\n' "$driver" >&2
    fi
    exit 2
    ;;
esac

if [[ $# -gt 2 ]]; then
  usage >&2
  exit 2
fi

OUT_DIR=${2:-"$DEFAULT_OUT_DIR"}
DOCKERFILE=${DOCKERFILE:-"$ROOT_DIR/Dockerfile"}
TARGET_ARCH_VALUE=${TARGETARCH:-}
if [[ -z "$TARGET_ARCH_VALUE" ]]; then
  TARGET_ARCH_VALUE=$("$ROOT_DIR/scripts/with-go-toolchain.sh" go env GOHOSTARCH)
fi
case "$TARGET_ARCH_VALUE" in
  amd64|arm64) ;;
  *)
    printf 'unsupported %s target arch: %s\n' "$DISPLAY_NAME" "$TARGET_ARCH_VALUE" >&2
    exit 2
    ;;
esac

STAMP_FILE="$OUT_DIR/.agent-compose-artifact-source"

artifacts_complete() {
  local path
  for path in "${REQUIRED_FILES[@]}"; do
    [[ -s "$OUT_DIR/$path" ]] || return 1
  done
  for path in "${REQUIRED_EXECUTABLES[@]}"; do
    [[ -x "$OUT_DIR/$path" ]] || return 1
  done
}

sha256_files() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$@"
  else
    printf 'sha256sum or shasum is required to fingerprint %s artifacts\n' "$DISPLAY_NAME" >&2
    return 1
  fi
}

sha256_stdin() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum
  else
    shasum -a 256
  fi
}

source_fingerprint() {
  sha256_files \
    "$DOCKERFILE" \
    "$ROOT_DIR/scripts/build-agent-compose-binary.sh" \
    "${BASH_SOURCE[0]}" |
    awk '{print $1}' |
    sha256_stdin |
    awk '{print $1}'
}

expected_stamp=$(printf 'target_arch=%s\nruntime_version_override=%s\nsource_fingerprint=%s' \
  "$TARGET_ARCH_VALUE" "$RUNTIME_VERSION_OVERRIDE" "$(source_fingerprint)")

stamp_matches() {
  [[ -s "$STAMP_FILE" ]] && [[ "$(<"$STAMP_FILE")" == "$expected_stamp" ]]
}

write_stamp() {
  local tmp_stamp
  tmp_stamp=$(mktemp "$OUT_DIR/.agent-compose-artifact-source.XXXXXX")
  printf '%s\n' "$expected_stamp" >"$tmp_stamp"
  mv -f "$tmp_stamp" "$STAMP_FILE"
}

if artifacts_complete && stamp_matches; then
  if [[ "${AGENT_COMPOSE_ARTIFACT_STATUS_ONLY:-0}" != "1" ]]; then
    printf '%s dev artifacts match the current source contract in %s\n' "$DISPLAY_NAME" "$OUT_DIR"
  fi
  exit 0
fi

if [[ "${AGENT_COMPOSE_ARTIFACT_STATUS_ONLY:-0}" == "1" ]]; then
  exit 1
fi

if [[ "${AGENT_COMPOSE_ADOPT_EXISTING_ARTIFACTS:-0}" == "1" ]]; then
  if ! artifacts_complete; then
    printf 'cannot adopt incomplete %s dev artifacts in %s\n' "$DISPLAY_NAME" "$OUT_DIR" >&2
    exit 1
  fi
  mkdir -p "$OUT_DIR"
  write_stamp
  printf 'Adopted existing %s dev artifacts for %s in %s\n' "$DISPLAY_NAME" "$TARGET_ARCH_VALUE" "$OUT_DIR"
  exit 0
fi

mkdir -p "$OUT_DIR"

iidfile=$(mktemp)
cid=""
cleanup() {
  rm -f "$iidfile"
  if [[ -n "${cid:-}" ]]; then
    docker rm -f "$cid" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

build_args=(
  --iidfile "$iidfile"
  -f "$DOCKERFILE"
  --target "$DOCKER_TARGET"
  --build-arg "TARGETARCH=$TARGET_ARCH_VALUE"
)

append_build_arg() {
  local name=$1
  local value=$2
  if [[ -n "$value" ]]; then
    build_args+=(--build-arg "$name=$value")
  fi
}

append_build_arg HTTP_PROXY "${HTTP_PROXY:-${http_proxy:-}}"
append_build_arg HTTPS_PROXY "${HTTPS_PROXY:-${https_proxy:-}}"
append_build_arg ALL_PROXY "${ALL_PROXY:-${all_proxy:-}}"
append_build_arg NO_PROXY "${NO_PROXY:-${no_proxy:-}}"
append_build_arg REGISTRY_MIRROR "${REGISTRY_MIRROR:-}"
append_build_arg GITHUB_MIRROR "${GITHUB_MIRROR:-}"
case "$driver" in
  boxlite) append_build_arg BOXLITE_VERSION "${BOXLITE_VERSION:-}" ;;
  microsandbox) append_build_arg MICROSANDBOX_VERSION "${MICROSANDBOX_VERSION:-}" ;;
esac

docker build "${build_args[@]}" "$ROOT_DIR"

image_id=$(tr -d '\n' <"$iidfile")
cid=$(docker create "$image_id")

clean_paths=()
for path in "${CLEAN_DIRS[@]}"; do
  clean_paths+=("$OUT_DIR/$path")
done
rm -rf "${clean_paths[@]}"
docker cp "$cid":/out/. "$OUT_DIR"
if ! artifacts_complete; then
  printf 'exported %s dev artifacts are incomplete in %s\n' "$DISPLAY_NAME" "$OUT_DIR" >&2
  exit 1
fi
write_stamp
