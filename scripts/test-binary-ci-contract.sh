#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
CI_WORKFLOW="$ROOT_DIR/.github/workflows/ci.yml"
IMAGES_WORKFLOW="$ROOT_DIR/.github/workflows/images.yml"
INSTALLER_BUILDER="$ROOT_DIR/scripts/build-installer-assets.sh"
INSTALLER_TEST="$ROOT_DIR/scripts/test-installer-assets.sh"
DAEMON_SMOKE="$ROOT_DIR/scripts/test-agent-compose-daemon-startup.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT

failures=0

fail() {
  printf 'test-binary-ci-contract: %s\n' "$*" >&2
  failures=$((failures + 1))
}

require_regex() { # $1=text $2=extended-regex $3=description
  if ! grep -Eq -- "$2" <<<"$1"; then
    fail "missing $3"
  fi
}

forbid_regex() { # $1=text $2=extended-regex $3=description
  if grep -Eiq -- "$2" <<<"$1"; then
    fail "forbidden $3"
  fi
}

job_block() { # $1=workflow $2=job-id
  awk -v job="$2" '
    $0 == "  " job ":" {
      found = 1
      print
      next
    }
    found && $0 ~ /^  [[:alnum:]_-]+:[[:space:]]*$/ {
      exit
    }
    found {
      print
    }
    END {
      if (!found) {
        exit 1
      }
    }
  ' "$1"
}

load_job() { # $1=workflow $2=job-id $3=destination variable
  local block
  if ! block=$(job_block "$1" "$2"); then
    fail "job '$2' in ${1#"$ROOT_DIR/"}"
    printf -v "$3" '%s' ''
    return
  fi
  printf -v "$3" '%s' "$block"
}

trigger_block=$(awk '
  /^on:[[:space:]]*$/ { found = 1 }
  found && /^permissions:[[:space:]]*$/ { exit }
  found { print }
' "$CI_WORKFLOW")
require_regex "$trigger_block" '^[[:space:]]*push:[[:space:]]*$' 'CI push trigger'
require_regex "$trigger_block" '^[[:space:]]*branches:[[:space:]]*$' 'CI push branch filter'
require_regex "$trigger_block" '^[[:space:]]*-[[:space:]]*main[[:space:]]*$' 'CI push on main'
require_regex "$trigger_block" '^[[:space:]]*tags:[[:space:]]*$' 'CI push tag filter'
require_regex "$trigger_block" "^[[:space:]]*-[[:space:]]*[\"']?v\\*[\"']?[[:space:]]*$" 'CI push on v* tags'

load_job "$CI_WORKFLOW" binary-matrix binary_matrix
load_job "$CI_WORKFLOW" binary-darwin binary_darwin
load_job "$CI_WORKFLOW" binary-darwin-native binary_darwin_native
load_job "$CI_WORKFLOW" binary-linux binary_linux

if [[ -n $binary_matrix ]]; then
  require_regex "$binary_matrix" 'amd64=.*arch[^[:alnum:]]+amd64.*runner[^[:alnum:]]+ubuntu-latest' \
    'binary-matrix amd64/ubuntu-latest mapping'
  require_regex "$binary_matrix" 'arm64=.*arch[^[:alnum:]]+arm64.*runner[^[:alnum:]]+ubuntu-24\.04-arm' \
    'binary-matrix arm64/ubuntu-24.04-arm mapping'
  require_regex "$binary_matrix" 'if .*EVENT_NAME.*pull_request' \
    'binary-matrix pull-request branch'
  require_regex "$binary_matrix" 'echo .*\$amd64.*GITHUB_OUTPUT' \
    'binary-matrix pull-request amd64 output'
  require_regex "$binary_matrix" 'echo .*\$amd64.*\$arm64.*GITHUB_OUTPUT' \
    'binary-matrix non-PR amd64+arm64 output'

  matrix_script=$(awk '
    /^      - id: platforms[[:space:]]*$/ { step = 1; next }
    step && /^        run: \|[[:space:]]*$/ { body = 1; next }
    body && /^  [[:alnum:]_-]+:[[:space:]]*$/ { exit }
    body { sub(/^          /, ""); print }
  ' "$CI_WORKFLOW")
  for event_and_expected in \
    'pull_request|json=[{"arch":"amd64","runner":"ubuntu-latest"}]' \
    'push|json=[{"arch":"amd64","runner":"ubuntu-latest"},{"arch":"arm64","runner":"ubuntu-24.04-arm"}]'; do
    event=${event_and_expected%%|*}
    expected=${event_and_expected#*|}
    output="$TEST_ROOT/matrix-$event"
    if ! EVENT_NAME="$event" GITHUB_OUTPUT="$output" bash -euo pipefail -c "$matrix_script"; then
      fail "binary-matrix $event dry run"
      continue
    fi
    actual=$(<"$output")
    [[ $actual == "$expected" ]] \
      || fail "binary-matrix $event output is '$actual', expected '$expected'"
  done
fi

metadata_verifier='(\./)?scripts/[[:alnum:]_.-]*(verify|test)[[:alnum:]_.-]*(binary|metadata|build-info|version)[[:alnum:]_.-]*\.sh'

if [[ -n $binary_darwin ]]; then
  require_regex "$binary_darwin" 'strategy:' 'binary-darwin strategy'
  require_regex "$binary_darwin" 'matrix:' 'binary-darwin matrix'
  require_regex "$binary_darwin" 'amd64' 'binary-darwin amd64 target'
  require_regex "$binary_darwin" 'arm64' 'binary-darwin arm64 target'
  require_regex "$binary_darwin" 'darwin-docker' 'binary-darwin darwin-docker profile'
  require_regex "$binary_darwin" '--print-config' 'binary-darwin profile assertion'
  require_regex "$binary_darwin" 'actions/setup-go@v5' 'binary-darwin Go setup'
  require_regex "$binary_darwin" 'cache:[[:space:]]*true' 'binary-darwin Go cache'
  require_regex "$binary_darwin" '(compiled[_-]drivers|expected[_-]drivers|drivers)[^[:alnum:]]+docker([^[:alnum:]]|$)' \
    'binary-darwin Docker-only driver assertion'
  forbid_regex "$binary_darwin" 'boxlite|microsandbox' 'native drivers in binary-darwin job'
fi

if [[ -n $binary_darwin_native ]]; then
  require_regex "$binary_darwin_native" 'runs-on:[[:space:]]*macos' \
    'binary-darwin-native macOS runner'
  require_regex "$binary_darwin_native" 'darwin-docker' \
    'binary-darwin-native darwin-docker profile'
  require_regex "$binary_darwin_native" 'actions/setup-go@v5' \
    'binary-darwin-native Go setup'
  require_regex "$binary_darwin_native" 'cache:[[:space:]]*true' \
    'binary-darwin-native Go cache'
  require_regex "$binary_darwin_native" "$metadata_verifier" \
    'binary-darwin-native metadata verifier execution'
  require_regex "$binary_darwin_native" '(compiled[_-]drivers|expected[_-]drivers|drivers)[^[:alnum:]]+docker([^[:alnum:]]|$)' \
    'binary-darwin-native Docker-only driver assertion'
  require_regex "$binary_darwin_native" '(\./)?scripts/test-agent-compose-daemon-startup\.sh' \
    'binary-darwin-native daemon startup smoke execution'
  require_regex "$binary_darwin_native" '--expected-os[[:space:]]+darwin' \
    'binary-darwin-native daemon expected OS assertion'
  require_regex "$binary_darwin_native" '--expected-arch[[:space:]]+' \
    'binary-darwin-native daemon expected architecture assertion'
  require_regex "$binary_darwin_native" '--expected-drivers[[:space:]]+docker([^[:alnum:]]|$)' \
    'binary-darwin-native daemon Docker-only assertion'
  forbid_regex "$binary_darwin_native" 'boxlite|microsandbox' \
    'native drivers in binary-darwin-native job'
fi

if [[ -n $binary_linux ]]; then
  require_regex "$binary_linux" 'binary-matrix' 'binary-linux dependency on binary-matrix'
  require_regex "$binary_linux" 'fromJSON\(needs\.binary-matrix\.outputs\.' \
    'binary-linux dynamic matrix output'
  require_regex "$binary_linux" 'runs-on:[[:space:]]*\$\{\{[[:space:]]*matrix\.[[:alnum:]_.-]*runner' \
    'binary-linux matrix runner selection'
  require_regex "$binary_linux" '(--profile[[:space:]]+linux-full|task[[:space:]]+build:agent-compose:linux)' \
    'binary-linux linux-full build'
  require_regex "$binary_linux" 'actions/setup-go@v5' 'binary-linux Go setup'
  require_regex "$binary_linux" 'cache:[[:space:]]*true' 'binary-linux Go cache'
  require_regex "$binary_linux" "$metadata_verifier" 'binary-linux metadata verifier execution'
  require_regex "$binary_linux" 'docker[^[:alnum:]]+boxlite[^[:alnum:]]+microsandbox' \
    'binary-linux exact ordered three-driver assertion'
fi

binary_jobs=$(printf '%s\n%s\n%s\n%s\n' \
  "$binary_matrix" "$binary_darwin" "$binary_darwin_native" "$binary_linux")
forbid_regex "$binary_jobs" 'actions/upload-artifact@' 'binary upload-artifact step in CI'

load_job "$IMAGES_WORKFLOW" release release_job
if [[ -n $release_job ]]; then
  require_regex "$release_job" "if:[[:space:]]*github\\.ref_type[[:space:]]*==[[:space:]]*['\"]tag['\"]" \
    'tag-only release guard'
  require_regex "$release_job" '\./scripts/build-installer-assets\.sh[[:space:]]+\./upload' \
    'tested installer asset builder in release job'
  require_regex "$release_job" 'gh release (upload|create).*upload/\*' \
    'release publication from installer output only'
  forbid_regex "$release_job" 'build/agent-compose|build-agent-compose-binary|agent-compose-(darwin|linux)-(amd64|arm64)' \
    'binary build or binary path in release job'
  forbid_regex "$release_job" 'actions/upload-artifact@' \
    'workflow artifact upload in release job'
fi

[[ -x $INSTALLER_BUILDER ]] || fail 'executable scripts/build-installer-assets.sh'
[[ -x $INSTALLER_TEST ]] || fail 'executable scripts/test-installer-assets.sh'
if [[ -f $INSTALLER_TEST ]]; then
  require_regex "$(<"$INSTALLER_TEST")" 'build-installer-assets\.sh' \
    'installer asset test coverage of release builder'
fi

[[ -x $DAEMON_SMOKE ]] || fail 'executable scripts/test-agent-compose-daemon-startup.sh'
if [[ -f $DAEMON_SMOKE ]]; then
  daemon_smoke_source=$(<"$DAEMON_SMOKE")
  require_regex "$daemon_smoke_source" '(^|[[:space:]])daemon([[:space:]]|$)' \
    'daemon startup command in native smoke helper'
  require_regex "$daemon_smoke_source" '--json[[:space:]]+version' \
    'CLI metadata verification in native smoke helper'
  require_regex "$daemon_smoke_source" '/api/version' \
    'daemon version endpoint verification in native smoke helper'
fi

if ((failures > 0)); then
  printf 'test-binary-ci-contract: %d contract check(s) failed\n' "$failures" >&2
  exit 1
fi

printf 'test-binary-ci-contract: all checks passed\n'
