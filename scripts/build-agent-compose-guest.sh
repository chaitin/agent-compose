#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
GUEST_IMAGE_DIR=${GUEST_IMAGE_DIR:-$ROOT_DIR/guest-images}
GUEST_IMAGE_DOCKERFILE=${GUEST_IMAGE_DOCKERFILE:-$GUEST_IMAGE_DIR/Dockerfile.agent-compose-guest}
REGISTRY_MIRROR=${REGISTRY_MIRROR:-docker.io}
PYPI_INDEX_URL=${PYPI_INDEX_URL:-https://mirrors.tuna.tsinghua.edu.cn/pypi/web/simple}
PYPI_TRUSTED_HOST=${PYPI_TRUSTED_HOST:-mirrors.tuna.tsinghua.edu.cn}
GOPROXY=${GOPROXY:-https://goproxy.cn,direct}
GO_VERSION=${GO_VERSION:-1.26.4}
GRPCURL_VERSION=${GRPCURL_VERSION:-v1.9.3}
PROTOC_GEN_GO_VERSION=${PROTOC_GEN_GO_VERSION:-v1.36.11}
PROTOC_GEN_GO_GRPC_VERSION=${PROTOC_GEN_GO_GRPC_VERSION:-v1.6.2}
IMAGE_TAG=${IMAGE_TAG:-agent-compose-guest:latest}

docker build \
  --build-arg REGISTRY_MIRROR="$REGISTRY_MIRROR" \
  --build-arg PYPI_INDEX_URL="$PYPI_INDEX_URL" \
  --build-arg PYPI_TRUSTED_HOST="$PYPI_TRUSTED_HOST" \
  --build-arg GOPROXY="$GOPROXY" \
  --build-arg GO_VERSION="$GO_VERSION" \
  --build-arg GRPCURL_VERSION="$GRPCURL_VERSION" \
  --build-arg PROTOC_GEN_GO_VERSION="$PROTOC_GEN_GO_VERSION" \
  --build-arg PROTOC_GEN_GO_GRPC_VERSION="$PROTOC_GEN_GO_GRPC_VERSION" \
  -f "$GUEST_IMAGE_DOCKERFILE" \
  -t "$IMAGE_TAG" \
  "$ROOT_DIR"

echo "Built guest image: $IMAGE_TAG"
