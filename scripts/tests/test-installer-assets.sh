#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
BUILDER="$ROOT_DIR/scripts/build-installer-assets.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT

fail() {
  printf 'test-installer-assets: %s\n' "$*" >&2
  exit 1
}

assert_mode() {
  local file=$1 expected=$2 actual
  actual=$(stat -c '%a' "$file")
  [[ $actual == "$expected" ]] \
    || fail "$file mode is $actual, expected $expected"
}

build_assets() {
  local output=$1
  VERSION=v9.8.7 \
    IMAGE_PREFIX=registry.example/agent-compose \
    AGENT_COMPOSE_FRONTEND_VERSION=v-ui-test \
    AGENT_COMPOSE_FRONTEND_VERSIONS=v-ui-test,v-ui-legacy \
    "$BUILDER" "$output" >/dev/null
}

output="$TEST_ROOT/upload"
second_output="$TEST_ROOT/upload-second"
extract_root="$TEST_ROOT/extracted"
mkdir -p "$extract_root"
build_assets "$output"
build_assets "$second_output"

actual_assets=$(find "$output" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort)
expected_assets=$'SHASUMS256.txt\nagent-compose-installer.tar.gz\ninstall.sh'
[[ $actual_assets == "$expected_assets" ]] \
  || fail "release asset set differs: $actual_assets"

assert_mode "$output/install.sh" 755
assert_mode "$output/agent-compose-installer.tar.gz" 644
assert_mode "$output/SHASUMS256.txt" 644
cmp "$ROOT_DIR/deploy/install.sh" "$output/install.sh" >/dev/null \
  || fail 'standalone install.sh differs from deploy/install.sh'

(
  cd "$output"
  sha256sum -c SHASUMS256.txt >/dev/null
)
[[ $(wc -l <"$output/SHASUMS256.txt") -eq 1 ]] \
  || fail 'checksum file must contain exactly one entry'
grep -Eq '^[[:xdigit:]]{64}  agent-compose-installer\.tar\.gz$' "$output/SHASUMS256.txt" \
  || fail 'checksum file does not name only the installer archive'

cmp "$output/agent-compose-installer.tar.gz" "$second_output/agent-compose-installer.tar.gz" >/dev/null \
  || fail 'installer archive is not reproducible'
cmp "$output/SHASUMS256.txt" "$second_output/SHASUMS256.txt" >/dev/null \
  || fail 'installer checksum is not reproducible'

actual_payload=$(tar -tzf "$output/agent-compose-installer.tar.gz" | LC_ALL=C sort)
expected_payload=$'agent-compose-installer/\nagent-compose-installer/.env.example\nagent-compose-installer/docker-compose.kvm.yml\nagent-compose-installer/docker-compose.yml\nagent-compose-installer/images/\nagent-compose-installer/images/manifest.env'
[[ $actual_payload == "$expected_payload" ]] \
  || fail "installer payload differs: $actual_payload"

tar -xzf "$output/agent-compose-installer.tar.gz" -C "$extract_root"
payload="$extract_root/agent-compose-installer"
cmp "$ROOT_DIR/docker-compose.yml" "$payload/docker-compose.yml" >/dev/null \
  || fail 'base Compose file differs in archive'
cmp "$ROOT_DIR/docker-compose.kvm.yml" "$payload/docker-compose.kvm.yml" >/dev/null \
  || fail 'KVM Compose overlay differs in archive'
cmp "$ROOT_DIR/.env.example" "$payload/.env.example" >/dev/null \
  || fail '.env.example differs in archive'

assert_mode "$payload/docker-compose.yml" 644
assert_mode "$payload/docker-compose.kvm.yml" 644
assert_mode "$payload/.env.example" 644
assert_mode "$payload/images/manifest.env" 644

grep -Fxq 'RUNTIME_DRIVER=docker' "$payload/.env.example" \
  || fail '.env.example must keep docker as the active runtime default'
grep -Fxq '# COMPOSE_FILE=docker-compose.yml:docker-compose.kvm.yml' "$payload/.env.example" \
  || fail '.env.example lacks the optional KVM Compose selection'
if grep -Eq '^[[:space:]]*(export[[:space:]]+)?COMPOSE_FILE[[:space:]]*=' "$payload/.env.example"; then
  fail '.env.example must not activate COMPOSE_FILE by default'
fi
grep -Fxq 'AUTH_PASSWORD=' "$payload/.env.example" \
  || fail '.env.example must keep AUTH_PASSWORD empty'
grep -Fxq 'AUTH_SECRET=' "$payload/.env.example" \
  || fail '.env.example must keep AUTH_SECRET empty'

expected_manifest=$'INSTALLER_PAYLOAD_VERSION=1\nAGENT_COMPOSE_IMAGE=registry.example/agent-compose/agent-compose:v9.8.7\nAGENT_COMPOSE_FRONTEND_VERSION=v-ui-test\nAGENT_COMPOSE_FRONTEND_VERSIONS=v-ui-test,v-ui-legacy\nAGENT_COMPOSE_FRONTEND_IMAGE=registry.example/agent-compose/agent-compose-ui:v-ui-test\nDEFAULT_IMAGE=registry.example/agent-compose/agent-compose-guest:v9.8.7'
actual_manifest=$(<"$payload/images/manifest.env")
[[ $actual_manifest == "$expected_manifest" ]] \
  || fail "image manifest differs: $actual_manifest"

if find "$output" "$payload" -type f -name agent-compose -print -quit | grep -q .; then
  fail 'per-architecture agent-compose binary found in release assets'
fi

nonempty="$TEST_ROOT/nonempty"
mkdir -p "$nonempty"
touch "$nonempty/stale-binary"
if build_assets "$nonempty" >/dev/null 2>&1; then
  fail 'builder accepted a pre-existing output path with stale assets'
fi
[[ -f $nonempty/stale-binary ]] || fail 'builder modified rejected output path'

invalid_versions="$TEST_ROOT/invalid-versions"
if VERSION=v9.8.7 IMAGE_PREFIX=registry.example \
  AGENT_COMPOSE_FRONTEND_VERSION=v3 \
  AGENT_COMPOSE_FRONTEND_VERSIONS=v1,v2 \
  "$BUILDER" "$invalid_versions" >/dev/null 2>&1; then
  fail 'builder accepted a frontend default outside the supported list'
fi
[[ ! -e $invalid_versions ]] || fail 'invalid frontend versions left an output directory'

for invalid_list in ',v1' 'v1,' 'v1,,v2'; do
  invalid_output="$TEST_ROOT/invalid-list-${invalid_list//,/_}"
  if VERSION=v9.8.7 IMAGE_PREFIX=registry.example \
    AGENT_COMPOSE_FRONTEND_VERSION=v1 \
    AGENT_COMPOSE_FRONTEND_VERSIONS="$invalid_list" \
    "$BUILDER" "$invalid_output" >/dev/null 2>&1; then
    fail "builder accepted an empty frontend version in $invalid_list"
  fi
  [[ ! -e $invalid_output ]] || fail "invalid frontend list $invalid_list left an output directory"
done

printf 'test-installer-assets: all checks passed\n'
