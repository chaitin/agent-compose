#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
FIXTURE_DIR="$ROOT_DIR/cmd/installer/testdata/docker-demo"

for command_name in curl docker go gzip nohup setsid sha256sum tar; do
  command -v "$command_name" >/dev/null 2>&1 \
    || { printf 'demo-installer-docker: %s is required\n' "$command_name" >&2; exit 1; }
done
docker compose version >/dev/null
docker version >/dev/null

DEMO_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/agent-compose-installer-demo.XXXXXX")
COMPOSE_PROJECT_NAME="agent-compose-installer-demo-${DEMO_ROOT##*.}"
COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME,,}
export COMPOSE_PROJECT_NAME
RELEASE_ROOT="$DEMO_ROOT/releases"
INSTALL_DIR="$DEMO_ROOT/install"
BUILD_DIR="$DEMO_ROOT/build"
SERVER_BIN="$BUILD_DIR/demo-release-server"
SERVER_ADDRESS_FILE="$DEMO_ROOT/server.address"
SERVER_PID_FILE="$DEMO_ROOT/server.pid"
SERVER_LOG="$DEMO_ROOT/server.log"
mkdir -p "$RELEASE_ROOT" "$BUILD_DIR"

SERVER_PID=
DEMO_SUCCEEDED=0
cleanup_failure() {
  local status=$?
  trap - EXIT
  if [[ $status -ne 0 && $DEMO_SUCCEEDED -eq 0 ]]; then
    if [[ -f "$INSTALL_DIR/docker-compose.yml" ]]; then
      (cd "$INSTALL_DIR" && docker compose down --remove-orphans) >/dev/null 2>&1 || true
    fi
    [[ -z "$SERVER_PID" ]] || kill "$SERVER_PID" >/dev/null 2>&1 || true
    printf 'Demo failed; diagnostic files retained at %s\n' "$DEMO_ROOT" >&2
  fi
  exit "$status"
}
trap cleanup_failure EXIT

printf '==> Building local release/registry server\n'
go build -trimpath -o "$SERVER_BIN" "$FIXTURE_DIR/server"
nohup setsid "$SERVER_BIN" \
  --root "$RELEASE_ROOT" \
  --address-file "$SERVER_ADDRESS_FILE" \
  --pid-file "$SERVER_PID_FILE" \
  </dev/null >"$SERVER_LOG" 2>&1 &
SERVER_LAUNCH_PID=$!
for _ in $(seq 1 100); do
  [[ -s "$SERVER_ADDRESS_FILE" && -s "$SERVER_PID_FILE" ]] && break
  kill -0 "$SERVER_LAUNCH_PID" 2>/dev/null || [[ -s "$SERVER_PID_FILE" ]] \
    || { sed -n '1,120p' "$SERVER_LOG" >&2; exit 1; }
  sleep 0.1
done
[[ -s "$SERVER_ADDRESS_FILE" && -s "$SERVER_PID_FILE" ]] \
  || { printf 'demo server did not become ready\n' >&2; exit 1; }
SERVER_ADDRESS=$(tr -d '\r\n' <"$SERVER_ADDRESS_FILE")
SERVER_PID=$(tr -d '\r\n' <"$SERVER_PID_FILE")
BASE_URL="http://$SERVER_ADDRESS"
REGISTRY="$SERVER_ADDRESS"

printf '==> Building installer bootstrap assets\n'
VERSION=demo "$ROOT_DIR/scripts/build-installer-binaries.sh" "$RELEASE_ROOT/installer" >/dev/null

build_image() {
  local version=$1 context_dir="$BUILD_DIR/image-$1" image="$REGISTRY/demo/agent-compose:$1"
  mkdir -p "$context_dir"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -X main.version=$version" \
    -o "$context_dir/demo-app" "$FIXTURE_DIR/app"
  docker build --pull=false -q -t "$image" -f "$FIXTURE_DIR/Dockerfile" "$context_dir" >/dev/null
  docker push "$image" >"$DEMO_ROOT/push-$version.log"
}

build_bundle() {
  local version=$1 release_dir="$RELEASE_ROOT/app-$1" payload_root="$BUILD_DIR/payload-$1"
  local payload="$payload_root/agent-compose-installer" image="$REGISTRY/demo/agent-compose:$1"
  mkdir -p "$release_dir" "$payload/images"
  install -m 0644 "$FIXTURE_DIR/docker-compose.yml" "$payload/docker-compose.yml"
  install -m 0644 "$FIXTURE_DIR/env.example" "$payload/.env.example"
  printf 'COMPOSE_PROJECT_NAME=%s\n' "$COMPOSE_PROJECT_NAME" >>"$payload/.env.example"
  {
    printf 'INSTALLER_PAYLOAD_VERSION=1\n'
    printf 'AGENT_COMPOSE_IMAGE=%s\n' "$image"
    printf 'AGENT_COMPOSE_FRONTEND_VERSION=%s\n' "$version"
    printf 'AGENT_COMPOSE_FRONTEND_IMAGE=%s\n' "$image"
    printf 'DEFAULT_IMAGE=%s\n' "$image"
  } >"$payload/images/manifest.env"
  tar -C "$payload_root" -czf "$release_dir/agent-compose-installer.tar.gz" agent-compose-installer
  (cd "$release_dir" && sha256sum agent-compose-installer.tar.gz >SHASUMS256.txt)
}

printf '==> Building and pushing isolated v1/v2 demo images\n'
build_image v1
build_image v2
build_bundle v1
build_bundle v2

installer_env() {
  local app_version=$1
  shift
  env \
    AGENT_COMPOSE_INSTALLER_BASE_URL="$BASE_URL/installer" \
    AGENT_COMPOSE_RELEASE_BASE_URL="$BASE_URL/app-$app_version" \
    AGENT_COMPOSE_KVM_DETECT_PATH="$DEMO_ROOT/no-kvm" \
    "$@"
}

wait_for_version() {
  local expected=$1 mapping url body
  mapping=$(cd "$INSTALL_DIR" && docker compose port agent-compose 7410 | tail -n1)
  url="http://$mapping/api/version"
  for _ in $(seq 1 100); do
    body=$(curl -fsS "$url" 2>/dev/null || true)
    if grep -Fq '"version":"'"$expected"'"' <<<"$body"; then
      printf '%s\n' "$url"
      return 0
    fi
    sleep 0.1
  done
  printf 'expected %s at %s, last response: %s\n' "$expected" "$url" "$body" >&2
  return 1
}

printf '==> Real bootstrap + install (v1)\n'
installer_env v1 "$ROOT_DIR/deploy/install.sh" install --dir "$INSTALL_DIR" --yes
V1_URL=$(wait_for_version v1)
printf 'operator marker\n' >"$INSTALL_DIR/data/operator-marker.txt"

printf '==> Real retained-installer upgrade (v1 -> v2)\n'
AGENT_COMPOSE_RELEASE_BASE_URL="$BASE_URL/app-v2" \
  AGENT_COMPOSE_KVM_DETECT_PATH="$DEMO_ROOT/no-kvm" \
  "$INSTALL_DIR/installer" upgrade --dir "$INSTALL_DIR" --version v2 --yes
V2_URL=$(wait_for_version v2)

printf '==> Real uninstall preserving configuration/data\n'
"$INSTALL_DIR/installer" uninstall --dir "$INSTALL_DIR" --yes
[[ -f "$INSTALL_DIR/.env" && -f "$INSTALL_DIR/data/operator-marker.txt" ]] \
  || { printf 'ordinary uninstall did not preserve configuration/data\n' >&2; exit 1; }
[[ ! -e "$INSTALL_DIR/docker-compose.yml" && ! -e "$INSTALL_DIR/installer" ]] \
  || { printf 'ordinary uninstall retained managed files\n' >&2; exit 1; }

printf '==> Real reinstall (v2), preserving operator marker\n'
installer_env v2 "$ROOT_DIR/deploy/install.sh" install --dir "$INSTALL_DIR" --version v2 --yes
FINAL_URL=$(wait_for_version v2)
[[ -f "$INSTALL_DIR/data/operator-marker.txt" ]] || { printf 'reinstall lost operator data\n' >&2; exit 1; }
CONTAINER_ID=$(cd "$INSTALL_DIR" && docker compose ps -q agent-compose)

cat >"$DEMO_ROOT/demo.env" <<EOF
export DEMO_ROOT=$DEMO_ROOT
export INSTALL_DIR=$INSTALL_DIR
export SERVER_ADDRESS=$SERVER_ADDRESS
export SERVER_PID=$SERVER_PID
export SERVER_BIN=$SERVER_BIN
export REGISTRY=$REGISTRY
export V1_URL=$V1_URL
export V2_URL=$V2_URL
export FINAL_URL=$FINAL_URL
export CONTAINER_ID=$CONTAINER_ID
export IMAGE_V1=$REGISTRY/demo/agent-compose:v1
export IMAGE_V2=$REGISTRY/demo/agent-compose:v2
export AGENT_COMPOSE_INSTALLER_BASE_URL=$BASE_URL/installer
export AGENT_COMPOSE_RELEASE_BASE_URL=$BASE_URL/app-v2
export AGENT_COMPOSE_INSTALL_DIR=$INSTALL_DIR
export AGENT_COMPOSE_KVM_DETECT_PATH=$DEMO_ROOT/no-kvm
export COMPOSE_PROJECT_NAME=$COMPOSE_PROJECT_NAME
EOF
chmod 0600 "$DEMO_ROOT/demo.env"

DEMO_SUCCEEDED=1
printf '\nReal installer Docker demo is ready and has been left running.\n'
printf '  State:       %s\n' "$DEMO_ROOT/demo.env"
printf '  Install dir: %s\n' "$INSTALL_DIR"
printf '  Version URL: %s\n' "$FINAL_URL"
printf '  Container:   %s\n' "$CONTAINER_ID"
printf '  Server PID:  %s\n' "$SERVER_PID"
printf '\nInspect with:\n  source %q/demo.env\n  curl \"\$FINAL_URL\"\n  (cd \"\$INSTALL_DIR\" && docker compose ps && docker compose logs)\n' "$DEMO_ROOT"
printf '\nClean up later with:\n  %q/scripts/cleanup-installer-docker-demo.sh %q\n' "$ROOT_DIR" "$DEMO_ROOT"
