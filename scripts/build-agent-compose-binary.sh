#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
GO_TOOLCHAIN_SCRIPT=${GO_TOOLCHAIN_SCRIPT:-"$ROOT_DIR/scripts/with-go-toolchain.sh"}
BOXLITE_ARTIFACT_DIR=${BOXLITE_ARTIFACT_DIR:-"$ROOT_DIR/build/boxlite"}
MICROSANDBOX_ARTIFACT_DIR=${MICROSANDBOX_ARTIFACT_DIR:-"$ROOT_DIR/build/microsandbox"}

usage() {
  cat <<'EOF'
usage: build-agent-compose-binary.sh [options]

Options:
  --profile auto|darwin-docker|linux-full
  --goarch amd64|arm64
  --output PATH
  --version VALUE
  --print-config
  --help

The profile defaults to auto and --goarch defaults to the Go host architecture.
Set BUILD_VERBOSE=1 to pass -x to go build.
EOF
}

die() {
  printf 'build-agent-compose-binary: %s\n' "$1" >&2
  exit 2
}

require_value() {
  local option=$1
  local remaining=$2
  if [[ $remaining -lt 2 ]]; then
    die "$option requires a value"
  fi
}

default_version() {
  git -C "$ROOT_DIR" describe --always --tags --long 2>/dev/null ||
    git -C "$ROOT_DIR" rev-parse --short=12 HEAD 2>/dev/null ||
    printf 'unknown\n'
}

host_go_env() {
  "$GO_TOOLCHAIN_SCRIPT" go env "$1"
}

profile=auto
goarch=
output=
version=
version_set=0
print_config=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile)
      require_value "$1" "$#"
      profile=$2
      shift 2
      ;;
    --goarch)
      require_value "$1" "$#"
      goarch=$2
      shift 2
      ;;
    --output)
      require_value "$1" "$#"
      output=$2
      shift 2
      ;;
    --version)
      require_value "$1" "$#"
      version=$2
      version_set=1
      shift 2
      ;;
    --print-config)
      print_config=1
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

case "$profile" in
  auto|darwin-docker|linux-full) ;;
  *) die "unknown profile: $profile" ;;
esac

if [[ -z "$output" ]]; then
  die "--output must not be empty"
fi

if [[ -n "$goarch" ]]; then
  case "$goarch" in
    amd64|arm64) ;;
    *) die "unsupported architecture: $goarch" ;;
  esac
fi

if [[ $version_set -eq 0 ]]; then
  version=$(default_version)
fi
if [[ "$version" == *$'\n'* || "$version" == *$'\r'* ]]; then
  die "--version must not contain a newline"
fi

host_os=
host_arch=
if [[ "$profile" == auto ]]; then
  host_os=$(host_go_env GOHOSTOS)
  host_arch=$(host_go_env GOHOSTARCH)
  case "$host_os" in
    darwin) profile=darwin-docker ;;
    linux) profile=linux-full ;;
    *) die "unsupported host OS for auto profile: $host_os" ;;
  esac
  if [[ -z "$goarch" ]]; then
    goarch=$host_arch
  fi
elif [[ -z "$goarch" ]]; then
  goarch=$(host_go_env GOHOSTARCH)
fi

if [[ "$profile" == linux-full ]]; then
  if [[ -z "$host_os" ]]; then
    host_os=$(host_go_env GOHOSTOS)
  fi
  if [[ "$host_os" != linux ]]; then
    die "linux-full requires a Linux host; detected $host_os"
  fi
fi

case "$goarch" in
  amd64|arm64) ;;
  *) die "unsupported architecture: $goarch" ;;
esac

case "$profile" in
  darwin-docker)
    goos=darwin
    cgo_enabled=0
    tags=netgo,osusergo
    compiled_drivers=docker
    ;;
  linux-full)
    goos=linux
    cgo_enabled=1
    tags=netgo,osusergo,boxlitecgo,microsandboxcgo,k8scompose
    compiled_drivers=docker,boxlite,microsandbox,k8s
    ;;
esac

if [[ $print_config -eq 1 ]]; then
  printf 'profile=%s\n' "$profile"
  printf 'goos=%s\n' "$goos"
  printf 'goarch=%s\n' "$goarch"
  printf 'cgo_enabled=%s\n' "$cgo_enabled"
  printf 'tags=%s\n' "$tags"
  printf 'compiled_drivers=%s\n' "$compiled_drivers"
  printf 'version=%s\n' "$version"
  exit 0
fi

preflight_linux_full_artifacts() {
  local -a failures=()
  local path

  for path in \
    "$BOXLITE_ARTIFACT_DIR/include/boxlite.h" \
    "$BOXLITE_ARTIFACT_DIR/lib/libboxlite.so" \
    "$MICROSANDBOX_ARTIFACT_DIR/lib/libkrunfw.so" \
    "$MICROSANDBOX_ARTIFACT_DIR/lib/libmicrosandbox_go_ffi.so"; do
    if [[ ! -s "$path" ]]; then
      failures+=("missing or empty: $path")
    fi
  done

  for path in \
    "$BOXLITE_ARTIFACT_DIR/runtime/boxlite-guest" \
    "$BOXLITE_ARTIFACT_DIR/runtime/boxlite-shim" \
    "$MICROSANDBOX_ARTIFACT_DIR/bin/msb" \
    "$MICROSANDBOX_ARTIFACT_DIR/bin/agentd"; do
    if [[ ! -x "$path" ]]; then
      failures+=("missing or not executable: $path")
    fi
  done

  if [[ ${#failures[@]} -gt 0 ]]; then
    printf 'build-agent-compose-binary: linux-full artifact preflight failed:\n' >&2
    printf '  - %s\n' "${failures[@]}" >&2
    return 1
  fi
}

if [[ "$profile" == linux-full ]]; then
  preflight_linux_full_artifacts
fi

cd "$ROOT_DIR"
mkdir -p -- "$(dirname -- "$output")"

build_version_arg="github.com/chaitin/agent-compose/pkg/config.BuildVersion=$version"
if [[ "$build_version_arg" == *[[:space:]]* ]]; then
  if [[ "$build_version_arg" != *"'"* ]]; then
    build_version_arg="'$build_version_arg'"
  elif [[ "$build_version_arg" != *'"'* ]]; then
    build_version_arg="\"$build_version_arg\""
  else
    die "--version with whitespace must not contain both quote characters"
  fi
fi
ldflags="-X $build_version_arg"
build_args=(go build)
if [[ ${BUILD_VERBOSE:-0} == 1 ]]; then
  build_args+=(-x)
fi
build_args+=(
  -ldflags "$ldflags"
  -tags "$tags"
  -o "$output"
  ./cmd/agent-compose/
)

CGO_ENABLED=$cgo_enabled GOOS=$goos GOARCH=$goarch \
  "$GO_TOOLCHAIN_SCRIPT" "${build_args[@]}"
