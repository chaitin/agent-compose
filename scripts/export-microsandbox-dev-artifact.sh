#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
OUT_DIR=${1:-"$ROOT_DIR/build/microsandbox"}
DOCKERFILE=${DOCKERFILE:-"$ROOT_DIR/Dockerfile"}
REGISTRY_MIRROR_VALUE=${REGISTRY_MIRROR:-docker.io}
HTTP_PROXY_VALUE=${HTTP_PROXY:-}
HTTPS_PROXY_VALUE=${HTTPS_PROXY:-}
ALL_PROXY_VALUE=${ALL_PROXY:-}
NO_PROXY_VALUE=${NO_PROXY:-${no_proxy:-}}
TARGET_ARCH_VALUE=${TARGETARCH:-}
if [ -z "$TARGET_ARCH_VALUE" ]; then
  TARGET_ARCH_VALUE=$("$ROOT_DIR/scripts/with-go-toolchain.sh" go env GOHOSTARCH)
fi
case "$TARGET_ARCH_VALUE" in
  amd64|arm64) ;;
  *)
    echo "unsupported Microsandbox target arch: $TARGET_ARCH_VALUE" >&2
    exit 2
    ;;
esac

STAMP_FILE="$OUT_DIR/.agent-compose-artifact-source"

artifacts_complete() {
  [ -x "$OUT_DIR/bin/msb" ] &&
    [ -x "$OUT_DIR/bin/agentd" ] &&
    [ -s "$OUT_DIR/lib/libkrunfw.so" ] &&
    [ -s "$OUT_DIR/lib/libmicrosandbox_go_ffi.so" ]
}

sha256_files() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$@"
  else
    echo "sha256sum or shasum is required to fingerprint Microsandbox artifacts" >&2
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

expected_stamp=$(printf 'target_arch=%s\nsource_fingerprint=%s' \
  "$TARGET_ARCH_VALUE" "$(source_fingerprint)")

stamp_matches() {
  [ -s "$STAMP_FILE" ] && [ "$(cat "$STAMP_FILE")" = "$expected_stamp" ]
}

write_stamp() {
  local tmp_stamp
  tmp_stamp=$(mktemp "$OUT_DIR/.agent-compose-artifact-source.XXXXXX")
  printf '%s\n' "$expected_stamp" >"$tmp_stamp"
  mv -f "$tmp_stamp" "$STAMP_FILE"
}

if artifacts_complete && stamp_matches; then
  if [ "${AGENT_COMPOSE_ARTIFACT_STATUS_ONLY:-0}" != "1" ]; then
    echo "Microsandbox dev artifacts match the current source contract in $OUT_DIR"
  fi
  exit 0
fi

if [ "${AGENT_COMPOSE_ARTIFACT_STATUS_ONLY:-0}" = "1" ]; then
  exit 1
fi

if [ "${AGENT_COMPOSE_ADOPT_EXISTING_ARTIFACTS:-0}" = "1" ]; then
  if ! artifacts_complete; then
    echo "cannot adopt incomplete Microsandbox dev artifacts in $OUT_DIR" >&2
    exit 1
  fi
  mkdir -p "$OUT_DIR"
  write_stamp
  echo "Adopted existing Microsandbox dev artifacts for $TARGET_ARCH_VALUE in $OUT_DIR"
  exit 0
fi

mkdir -p "$OUT_DIR"

iidfile=$(mktemp)
cid=""
cleanup() {
  rm -f "$iidfile"
  if [ -n "${cid:-}" ]; then
    docker rm -f "$cid" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

docker build \
  --iidfile "$iidfile" \
  -f "$DOCKERFILE" \
  --target microsandbox-fetch \
  --build-arg "HTTP_PROXY=$HTTP_PROXY_VALUE" \
  --build-arg "HTTPS_PROXY=$HTTPS_PROXY_VALUE" \
  --build-arg "ALL_PROXY=$ALL_PROXY_VALUE" \
  --build-arg "NO_PROXY=$NO_PROXY_VALUE" \
  --build-arg "REGISTRY_MIRROR=$REGISTRY_MIRROR_VALUE" \
  --build-arg "TARGETARCH=$TARGET_ARCH_VALUE" \
  "$ROOT_DIR"

image_id=$(tr -d "\n" <"$iidfile")
cid=$(docker create "$image_id")

rm -rf "$OUT_DIR/bin" "$OUT_DIR/lib"
docker cp "$cid":/out/. "$OUT_DIR"
if ! artifacts_complete; then
  echo "exported Microsandbox dev artifacts are incomplete in $OUT_DIR" >&2
  exit 1
fi
write_stamp
