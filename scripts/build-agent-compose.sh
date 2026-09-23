#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

# IMAGE_NAME was this helper's original tag input. It is removed in favor of
# IMAGE_TAG, which the guest helper already uses; reject it instead of silently
# building the default tag for callers that still pass it.
if [[ -n ${IMAGE_NAME:-} ]]; then
  printf 'IMAGE_NAME is no longer supported; set IMAGE_TAG instead\n' >&2
  exit 1
fi

IMAGE_TAG="${IMAGE_TAG:-agent-compose:latest}"
DOCKERFILE="${DOCKERFILE:-Dockerfile}"
BUILD_CONTEXT="${BUILD_CONTEXT:-$ROOT_DIR}"
VERSION="${VERSION:-$(git -C "$ROOT_DIR" describe --always --tags --long 2>/dev/null || git -C "$ROOT_DIR" rev-parse --short=12 HEAD 2>/dev/null || echo 'unknown')}"

cd "$ROOT_DIR"

build_args=(
  -f "$DOCKERFILE"
  -t "$IMAGE_TAG"
  --build-arg "VERSION=$VERSION"
)

if [[ -n ${DOCKER_DEFAULT_PLATFORM:-} ]]; then
  build_args+=(--platform "$DOCKER_DEFAULT_PLATFORM")
fi

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
append_build_arg GOPROXY "${GOPROXY:-}"
append_build_arg BUF_VERSION "${BUF_VERSION:-}"
append_build_arg GO_VERSION "${GO_VERSION:-}"

if [[ "$(basename "$DOCKERFILE")" == "Dockerfile.agent-compose-local" ]]; then
  build_args+=(
    --build-context "boxlite-local=$ROOT_DIR/build/boxlite"
    --build-context "microsandbox-local=$ROOT_DIR/build/microsandbox"
  )
else
  append_build_arg GITHUB_MIRROR "${GITHUB_MIRROR:-}"
  append_build_arg BOXLITE_VERSION "${BOXLITE_VERSION:-}"
  append_build_arg MICROSANDBOX_VERSION "${MICROSANDBOX_VERSION:-}"
fi

if [[ "${NO_CACHE:-}" == "1" ]]; then
  build_args+=(--no-cache)
fi

docker build "${build_args[@]}" "$BUILD_CONTEXT"

echo "Built daemon image: $IMAGE_TAG"
