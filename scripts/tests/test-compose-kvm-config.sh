#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)

if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  printf 'Docker Compose is required to validate deployment configuration\n' >&2
  exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  printf 'jq is required to validate rendered Compose configuration\n' >&2
  exit 1
fi

compose_config() {
  env \
    -u COMPOSE_FILE \
    -u COMPOSE_PROFILES \
    -u AGENT_COMPOSE_IMAGE \
    -u AGENT_COMPOSE_FRONTEND_IMAGE \
    -u AGENT_COMPOSE_RUNTIME_BASE_URL \
    -u AGENT_COMPOSE_DATA_DIR \
    -u AGENT_COMPOSE_UI_DATA_DIR \
    docker compose \
      --project-directory "$ROOT_DIR" \
      --env-file "$ROOT_DIR/.env.example" \
      "$@" \
      config --format json
}

base_json=$(compose_config -f "$ROOT_DIR/docker-compose.yml")
ui_json=$(compose_config -f "$ROOT_DIR/docker-compose.yml" --profile with-ui)
kvm_json=$(compose_config -f "$ROOT_DIR/docker-compose.yml" -f "$ROOT_DIR/docker-compose.kvm.yml")
sandbox_json=$(compose_config -f "$ROOT_DIR/docker-compose.yml" -f "$ROOT_DIR/docker-compose.sandbox-network.yml")
local_json=$(compose_config -f "$ROOT_DIR/docker-compose.yml" -f "$ROOT_DIR/docker-compose.override.yml.example")

jq -e --arg data_source "$ROOT_DIR/data" '
  .services["agent-compose"] as $service |
  $service.privileged != true and
  (($service.devices // []) | length == 0) and
  $service.build == null and
  any($service.volumes[]; .source == "/var/run/docker.sock" and .target == "/var/run/docker.sock") and
  any($service.volumes[]; .source == $data_source and .target == "/data") and
  any($service.volumes[]; .target == "/data/work/.env" and .read_only == true) and
  any($service.ports[]; .host_ip == "127.0.0.1" and .target == 7410 and .published == "7410")
' >/dev/null <<<"$base_json"

jq -e --arg data_source "$ROOT_DIR/data" --arg ui_data_source "$ROOT_DIR/data/agent-compose-ui" '
  .services["agent-compose-ui"] as $service |
  any($service.volumes[]; .source == $ui_data_source and .target == "/data") and
  any($service.volumes[]; .source == ($data_source + "/sandboxes") and .target == "/data/sandboxes" and .read_only == true)
' >/dev/null <<<"$ui_json"

jq -e '
  .services["agent-compose"] as $service |
  $service.privileged == true and
  (($service.devices // []) | length == 1) and
  (
    $service.devices[0] == "/dev/kvm:/dev/kvm" or
    (
      $service.devices[0].source == "/dev/kvm" and
      $service.devices[0].target == "/dev/kvm" and
      $service.devices[0].permissions == "rwm"
    )
  )
' >/dev/null <<<"$kvm_json"

normalize_compose=$(cat <<'JQ'
del(.services["agent-compose"].privileged, .services["agent-compose"].devices) |
.services |= with_entries(
  if (.value.volumes? | type) == "array" then
    .value.volumes |= sort_by(.source, .target)
  else
    .
  end
)
JQ
)
base_without_kvm=$(jq -cS "$normalize_compose" <<<"$base_json")
kvm_without_kvm=$(jq -cS "$normalize_compose" <<<"$kvm_json")
if [[ "$base_without_kvm" != "$kvm_without_kvm" ]]; then
  printf 'KVM overlay changes rendered Compose fields beyond privileged and devices\n' >&2
  diff -u <(jq -S . <<<"$base_without_kvm") <(jq -S . <<<"$kvm_without_kvm") >&2 || true
  exit 1
fi

jq -e '
  .services["agent-compose"] as $service |
  $service.build != null and
  $service.privileged != true and
  (($service.devices // []) | length == 0)
' >/dev/null <<<"$local_json"

jq -e '
  .services["agent-compose"].networks.default == null and
  .services["agent-compose"].networks.sandbox == null and
  .networks.sandbox.name == "agent-compose-sandbox"
' >/dev/null <<<"$sandbox_json"

printf 'Compose base, KVM overlay, sandbox-network overlay, and local build override contracts passed\n'
