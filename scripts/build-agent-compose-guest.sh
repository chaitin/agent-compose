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

append_build_arg REGISTRY_MIRROR "${REGISTRY_MIRROR:-}"
append_build_arg GOPROXY "${GOPROXY:-}"
append_build_arg HTTP_PROXY "${HTTP_PROXY:-${http_proxy:-}}"
append_build_arg HTTPS_PROXY "${HTTPS_PROXY:-${https_proxy:-}}"
append_build_arg ALL_PROXY "${ALL_PROXY:-${all_proxy:-}}"
append_build_arg NO_PROXY "${NO_PROXY:-${no_proxy:-}}"
append_build_arg GO_VERSION "${GO_VERSION:-}"
append_build_arg GRPCURL_VERSION "${GRPCURL_VERSION:-}"
append_build_arg NPM_CONFIG_REGISTRY "${NPM_CONFIG_REGISTRY:-}"
append_build_arg PIP_INDEX_URL "${PIP_INDEX_URL:-}"
append_build_arg PIP_TRUSTED_HOST "${PIP_TRUSTED_HOST:-}"
append_build_arg CODEX_VERSION "${CODEX_VERSION:-}"
append_build_arg CLAUDE_CODE_VERSION "${CLAUDE_CODE_VERSION:-}"
append_build_arg OPENCODE_VERSION "${OPENCODE_VERSION:-}"
append_build_arg PI_AGENT_VERSION "${PI_AGENT_VERSION:-}"
append_build_arg PI_MCP_ADAPTER_VERSION "${PI_MCP_ADAPTER_VERSION:-}"
append_build_arg DSH_VERSION "${DSH_VERSION:-}"

case "$(basename "$GUEST_IMAGE_DOCKERFILE")" in
  Dockerfile.agent-compose-guest-archlinux)
    append_build_arg ARCHLINUX_TAG "${ARCHLINUX_TAG:-}"
    append_build_arg ARCHLINUX_MIRROR "${ARCHLINUX_MIRROR:-}"
    ;;
  *)
    append_build_arg NODE_MAJOR "${NODE_MAJOR:-}"
    ;;
esac

if [[ "${NO_CACHE:-}" == "1" ]]; then
  build_args+=(--no-cache)
fi

docker build "${build_args[@]}" "$ROOT_DIR"

echo "Built guest image: $IMAGE_TAG"
