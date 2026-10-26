#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)

usage() {
  cat <<'EOF'
usage: build-installer-assets.sh [OUTPUT_DIR]

Build the three GitHub Release installer assets. VERSION and IMAGE_PREFIX must
be set. AGENT_COMPOSE_FRONTEND_VERSION defaults to latest, and
AGENT_COMPOSE_FRONTEND_VERSIONS defaults to that single version.
EOF
}

die() {
  printf 'build-installer-assets: %s\n' "$*" >&2
  exit 1
}

if [[ ${1:-} == -h || ${1:-} == --help ]]; then
  usage
  exit 0
fi
[[ $# -le 1 ]] || die 'expected at most one output directory'

VERSION=${VERSION:-}
IMAGE_PREFIX=${IMAGE_PREFIX:-}
FRONTEND_VERSION=${AGENT_COMPOSE_FRONTEND_VERSION:-latest}
FRONTEND_VERSIONS=${AGENT_COMPOSE_FRONTEND_VERSIONS:-$FRONTEND_VERSION}
OUTPUT_DIR=${1:-"$ROOT_DIR/upload"}

[[ -n $VERSION ]] || die 'VERSION is required'
[[ -n $IMAGE_PREFIX ]] || die 'IMAGE_PREFIX is required'
for value_name in VERSION IMAGE_PREFIX FRONTEND_VERSION FRONTEND_VERSIONS; do
  value=${!value_name}
  [[ $value != *$'\n'* && $value != *$'\r'* ]] \
    || die "$value_name must not contain a newline"
done

frontend_default_found=false
[[ $FRONTEND_VERSIONS != ,* && $FRONTEND_VERSIONS != *, && $FRONTEND_VERSIONS != *,,* ]] \
  || die 'AGENT_COMPOSE_FRONTEND_VERSIONS must not contain empty entries'
IFS=',' read -r -a frontend_versions <<<"$FRONTEND_VERSIONS"
declare -A seen_frontend_versions=()
for frontend_version in "${frontend_versions[@]}"; do
  [[ $frontend_version =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$ ]] \
    || die "invalid frontend version: $frontend_version"
  [[ -z ${seen_frontend_versions[$frontend_version]:-} ]] \
    || die "duplicate frontend version: $frontend_version"
  seen_frontend_versions[$frontend_version]=1
  [[ $frontend_version != "$FRONTEND_VERSION" ]] || frontend_default_found=true
done
$frontend_default_found || die 'AGENT_COMPOSE_FRONTEND_VERSION must be listed in AGENT_COMPOSE_FRONTEND_VERSIONS'

for command_name in gzip sha256sum tar; do
  command -v "$command_name" >/dev/null 2>&1 \
    || die "$command_name is required"
done

OUTPUT_DIR=$(realpath -m -- "$OUTPUT_DIR")
[[ ! -e $OUTPUT_DIR && ! -L $OUTPUT_DIR ]] \
  || die "output path already exists: $OUTPUT_DIR"

output_parent=$(dirname -- "$OUTPUT_DIR")
mkdir -p -- "$output_parent"
work_dir=$(mktemp -d "$output_parent/.installer-assets.XXXXXX")
cleanup() {
  rm -rf -- "$work_dir"
}
trap cleanup EXIT

umask 022
assets="$work_dir/assets"
payload_root="$work_dir/payload"
payload="$payload_root/agent-compose-installer"
mkdir -p -- "$assets" "$payload/images"

# Standalone installer for the curl-to-bash entrypoint.
install -m 0755 "$ROOT_DIR/deploy/install.sh" "$assets/install.sh"

# Architecture-independent archive. Runtime images remain multi-arch registry
# references; host binaries are intentionally not release assets.
install -m 0644 "$ROOT_DIR/docker-compose.yml" "$payload/docker-compose.yml"
install -m 0644 "$ROOT_DIR/docker-compose.kvm.yml" "$payload/docker-compose.kvm.yml"
install -m 0644 "$ROOT_DIR/.env.example" "$payload/.env.example"
{
  printf 'INSTALLER_PAYLOAD_VERSION=1\n'
  printf 'AGENT_COMPOSE_IMAGE=%s/agent-compose:%s\n' "$IMAGE_PREFIX" "$VERSION"
  printf 'AGENT_COMPOSE_FRONTEND_VERSION=%s\n' "$FRONTEND_VERSION"
  printf 'AGENT_COMPOSE_FRONTEND_VERSIONS=%s\n' "$FRONTEND_VERSIONS"
  printf 'AGENT_COMPOSE_FRONTEND_IMAGE=%s/agent-compose-ui:%s\n' "$IMAGE_PREFIX" "$FRONTEND_VERSION"
  printf 'DEFAULT_IMAGE=%s/agent-compose-guest:%s\n' "$IMAGE_PREFIX" "$VERSION"
} >"$payload/images/manifest.env"
chmod 0644 "$payload/images/manifest.env"

# Normalize archive metadata so identical inputs produce identical assets.
tar \
  --sort=name \
  --mtime='UTC 1970-01-01' \
  --owner=0 \
  --group=0 \
  --numeric-owner \
  -C "$payload_root" \
  -cf - agent-compose-installer \
  | gzip -n >"$assets/agent-compose-installer.tar.gz"
chmod 0644 "$assets/agent-compose-installer.tar.gz"

(
  cd "$assets"
  sha256sum agent-compose-installer.tar.gz >SHASUMS256.txt
)
chmod 0644 "$assets/SHASUMS256.txt"

mv -- "$assets" "$OUTPUT_DIR"
printf 'Built installer assets in %s\n' "$OUTPUT_DIR"
