#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
GUEST_IMAGE_DIR=${GUEST_IMAGE_DIR:-$ROOT_DIR/guest-images}
GUEST_IMAGE_DOCKERFILE=${GUEST_IMAGE_DOCKERFILE:-$GUEST_IMAGE_DIR/Dockerfile.agent-compose-guest}
IMAGE_TAG=${IMAGE_TAG:-agent-compose-guest:latest}

build_args=(
  -f "$GUEST_IMAGE_DOCKERFILE"
  -t "$IMAGE_TAG"
)

append_build_arg() {
  local name=$1
  local value=$2
  if [[ -n "$value" ]]; then
    build_args+=(--build-arg "$name=$value")
  fi
}

append_build_arg REGISTRY_MIRROR "${REGISTRY_MIRROR:-}"
append_build_arg PYPI_INDEX_URL "${PYPI_INDEX_URL:-}"
append_build_arg PYPI_TRUSTED_HOST "${PYPI_TRUSTED_HOST:-}"

docker build "${build_args[@]}" "$ROOT_DIR"

echo "Built guest image: $IMAGE_TAG"
