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
    -u AGENT_COMPOSE_SANDBOX_HOST_IMAGE \
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
local_json=$(compose_config -f "$ROOT_DIR/docker-compose.yml" -f "$ROOT_DIR/docker-compose.override.yml.example")

# Base daemon contract: Docker-only, loopback-published 7410, no privilege.
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

# The KVM overlay moves the VM runtime out of the daemon and into the
# sandbox-host service. The daemon joins the host network namespace, drops its
# own published port, and no longer needs privilege or /dev/kvm.
jq -e '
  .services["agent-compose"] as $service |
  $service.privileged != true and
  (($service.devices // []) | length == 0) and
  $service.network_mode == "service:sandbox-host" and
  (($service.ports // []) | length == 0) and
  any($service.volumes[]; .source == "/var/run/docker.sock" and .target == "/var/run/docker.sock") and
  any($service.volumes[]; .source == "sandbox-host-run" and .target == "/run/agent-compose")
' >/dev/null <<<"$kvm_json"

# sandbox-host owns the VM processes: privileged, /dev/kvm, the loopback 7410
# port, the `agent-compose` network alias, and the shared socket volume.
jq -e --arg data_source "$ROOT_DIR/data" '
  .services["sandbox-host"] as $host |
  $host.privileged == true and
  (($host.devices // []) | length == 1) and
  (
    $host.devices[0] == "/dev/kvm:/dev/kvm" or
    (
      $host.devices[0].source == "/dev/kvm" and
      $host.devices[0].target == "/dev/kvm" and
      $host.devices[0].permissions == "rwm"
    )
  ) and
  $host.command == ["agent-compose", "sandbox-host"] and
  any($host.ports[]; .host_ip == "127.0.0.1" and .target == 7410 and .published == "7410") and
  any($host.networks.default.aliases[]; . == "agent-compose") and
  any($host.volumes[]; .source == "/var/run/docker.sock" and .target == "/var/run/docker.sock") and
  any($host.volumes[]; .source == $data_source and .target == "/data") and
  any($host.volumes[]; .source == "sandbox-host-run" and .target == "/run/agent-compose")
' >/dev/null <<<"$kvm_json"

# The shared socket volume is declared at the project level.
jq -e '.volumes["sandbox-host-run"] != null' >/dev/null <<<"$kvm_json"

# The daemon's identity (image, working directory, environment) is preserved
# when the KVM overlay is applied.
jq -e --argjson base "$base_json" '
  ($base.services["agent-compose"].image == .services["agent-compose"].image) and
  ($base.services["agent-compose"].working_dir == .services["agent-compose"].working_dir) and
  ($base.services["agent-compose"].environment == .services["agent-compose"].environment)
' >/dev/null <<<"$kvm_json"

jq -e '
  .services["agent-compose"] as $service |
  $service.build != null and
  $service.privileged != true and
  (($service.devices // []) | length == 0)
' >/dev/null <<<"$local_json"

printf 'Compose base, KVM overlay, and local build override contracts passed\n'
