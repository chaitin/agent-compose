#!/usr/bin/env bash
set -euo pipefail

DEMO_ROOT=${1:-}
PURGE_FILES=${2:-}
[[ "$DEMO_ROOT" == /tmp/agent-compose-installer-demo.* && -d "$DEMO_ROOT" && ! -L "$DEMO_ROOT" ]] \
  || { printf 'usage: cleanup-installer-docker-demo.sh /tmp/agent-compose-installer-demo.XXXXXX [--purge-files]\n' >&2; exit 1; }
STATE_FILE="$DEMO_ROOT/demo.env"
[[ -f "$STATE_FILE" && ! -L "$STATE_FILE" ]] || { printf 'missing safe demo state: %s\n' "$STATE_FILE" >&2; exit 1; }
# shellcheck disable=SC1090
source "$STATE_FILE"

if [[ -f "$INSTALL_DIR/docker-compose.yml" ]]; then
  (cd "$INSTALL_DIR" && docker compose down --remove-orphans)
elif [[ -n "${CONTAINER_ID:-}" ]] && docker container inspect "$CONTAINER_ID" >/dev/null 2>&1; then
  docker rm -f "$CONTAINER_ID"
fi
if [[ -n "${SERVER_PID:-}" && -r "/proc/$SERVER_PID/cmdline" ]] \
  && tr '\0' ' ' <"/proc/$SERVER_PID/cmdline" | grep -Fq "$SERVER_BIN"; then
  kill "$SERVER_PID"
fi
for image in "${IMAGE_V1:-}" "${IMAGE_V2:-}"; do
  [[ -z "$image" ]] || docker image rm "$image" >/dev/null 2>&1 || true
done
printf 'Stopped installer Docker demo at %s\n' "$DEMO_ROOT"

if [[ "$PURGE_FILES" == --purge-files ]]; then
  rm -rf -- "$DEMO_ROOT"
  printf 'Removed demo files.\n'
fi
