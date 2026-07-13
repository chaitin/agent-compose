#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: verify-agent-compose-binary.sh \
  --binary PATH --os GOOS --arch GOARCH --drivers DRIVER[,DRIVER...]

Execute an agent-compose binary's `--json version` command and verify:
  - version is a nonempty string
  - os and arch exactly match the expected Go target
  - compiled_drivers exactly matches the expected ordered list

Examples:
  verify-agent-compose-binary.sh \
    --binary ./build/agent-compose \
    --os darwin --arch arm64 --drivers docker

  verify-agent-compose-binary.sh \
    --binary ./build/agent-compose \
    --os linux --arch amd64 --drivers docker,boxlite,microsandbox
EOF
}

die() {
  printf 'verify-agent-compose-binary: %s\n' "$*" >&2
  exit 1
}

BINARY_PATH=
EXPECTED_OS=
EXPECTED_ARCH=
EXPECTED_DRIVERS=

while [[ $# -gt 0 ]]; do
  case $1 in
    --binary)
      [[ $# -ge 2 ]] || die '--binary requires a value'
      BINARY_PATH=$2
      shift 2
      ;;
    --os)
      [[ $# -ge 2 ]] || die '--os requires a value'
      EXPECTED_OS=$2
      shift 2
      ;;
    --arch)
      [[ $# -ge 2 ]] || die '--arch requires a value'
      EXPECTED_ARCH=$2
      shift 2
      ;;
    --drivers)
      [[ $# -ge 2 ]] || die '--drivers requires a value'
      EXPECTED_DRIVERS=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

[[ -n $BINARY_PATH ]] || die '--binary is required'
[[ -n $EXPECTED_OS ]] || die '--os is required'
[[ -n $EXPECTED_ARCH ]] || die '--arch is required'
[[ -n $EXPECTED_DRIVERS ]] || die '--drivers is required'
[[ -f $BINARY_PATH ]] || die "binary does not exist: $BINARY_PATH"
[[ -x $BINARY_PATH ]] || die "binary is not executable: $BINARY_PATH"
command -v jq >/dev/null 2>&1 || die 'jq is required'

expected_drivers_json=$(jq -cn --arg csv "$EXPECTED_DRIVERS" '$csv | split(",")')
if ! jq -e 'length > 0 and all(.[]; type == "string" and test("^[a-z0-9][a-z0-9_-]*$"))' \
  >/dev/null <<<"$expected_drivers_json"; then
  die '--drivers must be a comma-separated list of nonempty driver names without spaces'
fi

if ! metadata=$("$BINARY_PATH" --json version); then
  die "binary command failed: $BINARY_PATH --json version"
fi
if ! metadata_documents=$(jq -cs '.' <<<"$metadata"); then
  die "binary returned invalid JSON metadata: $BINARY_PATH"
fi
if ! jq -e 'length == 1 and (.[0] | type == "object")' >/dev/null <<<"$metadata_documents"; then
  die "binary must return exactly one JSON metadata object: $BINARY_PATH"
fi
metadata=$(jq -c '.[0]' <<<"$metadata_documents")

if jq -e \
  --arg expected_os "$EXPECTED_OS" \
  --arg expected_arch "$EXPECTED_ARCH" \
  --argjson expected_drivers "$expected_drivers_json" \
  '(.version | type == "string" and length > 0)
   and .os == $expected_os
   and .arch == $expected_arch
   and .compiled_drivers == $expected_drivers' \
  >/dev/null <<<"$metadata"; then
  printf 'Verified %s: os=%s arch=%s compiled_drivers=%s\n' \
    "$BINARY_PATH" "$EXPECTED_OS" "$EXPECTED_ARCH" "$EXPECTED_DRIVERS"
  exit 0
fi

actual=$(jq -c \
  '{version: (.version // null), os: (.os // null), arch: (.arch // null), compiled_drivers: (.compiled_drivers // null)}' \
  <<<"$metadata")
expected=$(jq -cn \
  --arg os "$EXPECTED_OS" \
  --arg arch "$EXPECTED_ARCH" \
  --argjson drivers "$expected_drivers_json" \
  '{version: "<nonempty>", os: $os, arch: $arch, compiled_drivers: $drivers}')
printf 'verify-agent-compose-binary: metadata mismatch\n' >&2
printf '  expected: %s\n' "$expected" >&2
printf '  actual:   %s\n' "$actual" >&2
exit 1
