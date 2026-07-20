#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
GO_TOOLCHAIN_SCRIPT=${GO_TOOLCHAIN_SCRIPT:-"$ROOT_DIR/scripts/with-go-toolchain.sh"}
daemon_image=${AGENT_COMPOSE_E2E_DAEMON_IMAGE:-agent-compose:latest}
guest_image=${AGENT_COMPOSE_E2E_GUEST_IMAGE:-agent-compose-guest:latest}
docker_socket=${AGENT_COMPOSE_E2E_DOCKER_SOCKET:-/var/run/docker.sock}
docker_host="unix://${docker_socket}"
task_run_id=${AGENT_COMPOSE_E2E_RUN_ID:-"$(date +%s)-$$"}
task_run_filter="label=agent-compose.e2e.task_run=${task_run_id}"
suite_filter="label=agent-compose.e2e=image-docker"

docker_cmd() {
  docker --host "$docker_host" "$@"
}

resource_ids() {
  local kind=$1
  local filter=$2
  case "$kind" in
    container) docker_cmd ps -aq --filter "$filter" ;;
    network) docker_cmd network ls -q --filter "$filter" ;;
    volume) docker_cmd volume ls -q --filter "$filter" ;;
    *) return 2 ;;
  esac
}

audit_resources() {
  local phase=$1
  local filter=$2
  local status=0
  local kind ids
  for kind in container network volume; do
    if ! ids=$(resource_ids "$kind" "$filter"); then
      printf 'failed to audit image Docker E2E %s resources during %s\n' "$kind" "$phase" >&2
      status=1
      continue
    fi
    if [[ -n "$ids" ]]; then
      printf '%s image Docker E2E %s resources: %s\n' "$phase" "$kind" "$ids" >&2
      status=1
    fi
  done
  return "$status"
}

cleanup_image_docker_e2e_resources() {
  set +e
  local network_ids network_id attached_ids container_ids volume_ids
  network_ids=$(resource_ids network "$task_run_filter" 2>/dev/null)
  for network_id in $network_ids; do
    attached_ids=$(docker_cmd ps -aq --filter "network=$network_id" 2>/dev/null)
    if [[ -n "$attached_ids" ]]; then
      docker_cmd rm -f $attached_ids >/dev/null 2>&1
    fi
  done
  container_ids=$(resource_ids container "$task_run_filter" 2>/dev/null)
  if [[ -n "$container_ids" ]]; then
    docker_cmd rm -f $container_ids >/dev/null 2>&1
  fi
  if [[ -n "$network_ids" ]]; then
    docker_cmd network rm $network_ids >/dev/null 2>&1
  fi
  volume_ids=$(resource_ids volume "$task_run_filter" 2>/dev/null)
  if [[ -n "$volume_ids" ]]; then
    docker_cmd volume rm -f $volume_ids >/dev/null 2>&1
  fi
  set -e
}

if [[ ! -S "$docker_socket" ]]; then
  printf 'Docker socket %s is unavailable; set AGENT_COMPOSE_E2E_DOCKER_SOCKET to a local Unix socket\n' "$docker_socket" >&2
  exit 1
fi
if ! docker_cmd info >/dev/null 2>&1; then
  printf 'Docker daemon at %s is unavailable\n' "$docker_host" >&2
  exit 1
fi
for image in "$daemon_image" "$guest_image"; do
  if ! docker_cmd image inspect "$image" >/dev/null 2>&1; then
    printf 'Docker image %s is unavailable locally; build it before running test:e2e:image-docker\n' "$image" >&2
    exit 1
  fi
done
if ! audit_resources stale "$suite_filter"; then
  exit 1
fi

trap cleanup_image_docker_e2e_resources EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker_cmd run --rm \
  --label agent-compose.e2e=image-docker \
  --label "agent-compose.e2e.task_run=${task_run_id}" \
  --entrypoint sh \
  "$guest_image" -lc '
    set -eu
    ! command -v go >/dev/null 2>&1
    test ! -e /usr/local/go/bin/go
    ! command -v grpcurl >/dev/null 2>&1
    ! command -v protoc-gen-go >/dev/null 2>&1
    ! command -v protoc-gen-go-grpc >/dev/null 2>&1
  '

test_status=0
DOCKER_HOST="$docker_host" \
  AGENT_COMPOSE_E2E_DAEMON_IMAGE="$daemon_image" \
  AGENT_COMPOSE_E2E_GUEST_IMAGE="$guest_image" \
  AGENT_COMPOSE_E2E_DOCKER_SOCKET="$docker_socket" \
  AGENT_COMPOSE_E2E_RUN_ID="$task_run_id" \
  GOCACHE="${GOCACHE:-$ROOT_DIR/.cache/go-build}" \
  "$GO_TOOLCHAIN_SCRIPT" go test ./test/e2e \
    -run '^TestE2EImageDocker(NoKVMStartup|SandboxLifecycle)$' -count=1 -v || test_status=$?

leak_status=0
audit_resources leaked "$task_run_filter" || leak_status=$?
if [[ "$test_status" -ne 0 || "$leak_status" -ne 0 ]]; then
  exit 1
fi
