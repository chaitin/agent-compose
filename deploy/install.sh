#!/usr/bin/env bash
# Download and run the architecture-specific agent-compose installer.

set -euo pipefail

REPOSITORY="${AGENT_COMPOSE_REPO:-chaitin/agent-compose}"
RELEASE_TAG="${AGENT_COMPOSE_INSTALLER_RELEASE:-installer-latest}"
INSTALLER_BASE_URL="${AGENT_COMPOSE_INSTALLER_BASE_URL:-}"
OS_NAME="${AGENT_COMPOSE_UNAME_S:-$(uname -s)}"
MACHINE="${AGENT_COMPOSE_UNAME_M:-$(uname -m)}"

die() {
  printf 'agent-compose installer: %s\n' "$*" >&2
  exit 1
}

[[ "$OS_NAME" == Linux ]] || die "unsupported operating system: $OS_NAME (Linux is required)"
case "$MACHINE" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) die "unsupported architecture: $MACHINE (amd64 or arm64 is required)" ;;
esac

for command_name in curl sha256sum mktemp; do
  command -v "$command_name" >/dev/null 2>&1 || die "$command_name is required"
done

ASSET="agent-compose-installer-linux-$ARCH"
if [[ -z "$INSTALLER_BASE_URL" ]]; then
  INSTALLER_BASE_URL="https://github.com/$REPOSITORY/releases/download/$RELEASE_TAG"
fi
BASE_URL="${INSTALLER_BASE_URL%/}"
TEMP_DIR=$(mktemp -d)
cleanup() {
  rm -rf -- "$TEMP_DIR"
}
trap cleanup EXIT INT TERM

printf 'Downloading agent-compose installer for linux/%s...\n' "$ARCH"
curl -fsSL -o "$TEMP_DIR/$ASSET" "$BASE_URL/$ASSET"
curl -fsSL -o "$TEMP_DIR/SHASUMS256.txt" "$BASE_URL/SHASUMS256.txt"

checksum_line=$(awk -v asset="$ASSET" '$2 == asset || $2 == "*" asset { print; found=1 } END { if (!found) exit 1 }' "$TEMP_DIR/SHASUMS256.txt") \
  || die "checksum entry is missing for $ASSET"
printf '%s\n' "$checksum_line" | (cd "$TEMP_DIR" && sha256sum -c - >/dev/null) \
  || die "checksum verification failed for $ASSET"

chmod 0755 "$TEMP_DIR/$ASSET"
if [[ -r /dev/tty && -t 1 ]]; then
  "$TEMP_DIR/$ASSET" "$@" </dev/tty
else
  "$TEMP_DIR/$ASSET" "$@"
fi
