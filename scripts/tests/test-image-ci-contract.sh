#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
WORKFLOW="$ROOT_DIR/.github/workflows/images.yml"
INSTALLER_BUILDER="$ROOT_DIR/scripts/build-installer-assets.sh"
INSTALLER_TEST="$ROOT_DIR/scripts/tests/test-installer-assets.sh"
IMAGE_E2E="$ROOT_DIR/scripts/test-image-docker-e2e.sh"
DAEMON_BUILDER="$ROOT_DIR/scripts/build-agent-compose.sh"
GUEST_BUILDER="$ROOT_DIR/scripts/build-agent-compose-guest.sh"
GUEST_DOCKERFILE="$ROOT_DIR/guest-images/Dockerfile.agent-compose-guest"

failures=0
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT

fail() {
  printf 'test-image-ci-contract: %s\n' "$*" >&2
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

job_block() { # $1=job-id
  awk -v job="$1" '
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
  ' "$WORKFLOW"
}

load_job() { # $1=job-id $2=destination variable
  local block
  if ! block=$(job_block "$1"); then
    fail "job '$1' in .github/workflows/images.yml"
    printf -v "$2" '%s' ''
    return
  fi
  printf -v "$2" '%s' "$block"
}

step_containing() { # $1=job block $2=literal needle
  awk -v needle="$2" '
    /^      - / {
      if (matched) {
        print step
        emitted = 1
        exit
      }
      step = $0 ORS
      matched = index($0, needle) > 0
      next
    }
    length(step) > 0 {
      step = step $0 ORS
      if (index($0, needle) > 0) {
        matched = 1
      }
    }
    END {
      if (matched && !emitted) {
        printf "%s", step
      }
    }
  ' <<<"$1"
}

header=$(awk '/^jobs:[[:space:]]*$/ { exit } { print }' "$WORKFLOW")
require_regex "$header" '^[[:space:]]*-[[:space:]]*docker-compose\.kvm\.yml[[:space:]]*$' \
  'docker-compose.kvm.yml workflow path filter'

load_job setup setup_job
load_job build build_job
load_job image-smoke image_smoke_job
load_job merge merge_job
load_job release release_job

if [[ -n $setup_job ]]; then
  require_regex "$setup_job" 'actions/checkout@v4' 'setup checkout for contract audit'
  require_regex "$setup_job" '(\./)?scripts/tests/test-image-ci-contract\.sh' \
    'setup execution of image CI contract test'
fi

extract_setup_script() {
  awk '
    /^      - id:[[:space:]]*platforms[[:space:]]*$/ { in_step = 1; next }
    in_step && /^        run:[[:space:]]*\|[[:space:]]*$/ { capture = 1; next }
    capture {
      if (substr($0, 1, 10) == "          ") {
        print substr($0, 11)
        next
      }
      exit
    }
  ' <<<"$setup_job"
}

setup_script=$(extract_setup_script)
if [[ -z $setup_script ]]; then
  fail 'executable setup platform matrix script'
else
  run_setup_matrix() { # $1=event $2=expected JSON
    local event_name=$1 expected=$2 output actual
    output=$(mktemp)
    if ! EVENT_NAME=$event_name GITHUB_OUTPUT=$output bash -c "$setup_script"; then
      fail "setup matrix execution for $event_name"
      rm -f -- "$output"
      return
    fi
    actual=$(sed -n 's/^json=//p' "$output")
    if [[ $(wc -l <"$output") -ne 1 || $actual != "$expected" ]]; then
      fail "setup matrix for $event_name is '$actual', expected '$expected'"
    fi
    rm -f -- "$output"
  }

  run_setup_matrix pull_request \
    '[{"tag":"linux/amd64","arch":"amd64","runner":"ubuntu-latest"}]'
  run_setup_matrix push \
    '[{"tag":"linux/amd64","arch":"amd64","runner":"ubuntu-latest"},{"tag":"linux/arm64","arch":"arm64","runner":"ubuntu-24.04-arm"}]'
fi

if [[ -n $build_job ]]; then
  require_regex "$build_job" 'needs:[[:space:]]*setup' 'build dependency on setup'
  require_regex "$build_job" 'runs-on:[[:space:]]*\$\{\{[[:space:]]*matrix\.platform\.runner' \
    'build native matrix runner'
  require_regex "$build_job" 'platform:[[:space:]]*\$\{\{[[:space:]]*fromJSON\(needs\.setup\.outputs\.platforms\)' \
    'build dynamic setup matrix'
  require_regex "$build_job" 'name:[[:space:]]*\[agent-compose,[[:space:]]*agent-compose-guest\]' \
    'daemon and guest build matrix'
  require_regex "$build_job" 'uses:[[:space:]]*docker/build-push-action@v6' \
    'Buildx image build action'
  require_regex "$build_job" 'platforms:[[:space:]]*\$\{\{[[:space:]]*matrix\.platform\.tag' \
    'native target platform selection'
  require_regex "$build_job" 'push-by-digest=true' 'push-by-digest output'
  require_regex "$build_job" 'name-canonical=true' 'canonical digest output'
  require_regex "$build_job" "push=\\$\\{\\{[[:space:]]*github\\.event_name[[:space:]]*!=[[:space:]]*[\"']pull_request[\"']" \
    'non-PR digest push condition'
  require_regex "$build_job" 'cache-from:[[:space:]]*type=gha,scope=\$\{\{[[:space:]]*matrix\.name[[:space:]]*\}\}-\$\{\{[[:space:]]*matrix\.platform\.arch' \
    'per-image/per-architecture GHA cache read'
  require_regex "$build_job" 'cache-to:[[:space:]]*type=gha,mode=max,scope=\$\{\{[[:space:]]*matrix\.name[[:space:]]*\}\}-\$\{\{[[:space:]]*matrix\.platform\.arch' \
    'per-image/per-architecture GHA cache write'

  upload_step=$(step_containing "$build_job" 'actions/upload-artifact@')
  if [[ -z $upload_step ]]; then
    fail 'one-day digest artifact upload step'
  else
    require_regex "$upload_step" "if:[[:space:]]*github\\.event_name[[:space:]]*!=[[:space:]]*[\"']pull_request[\"']" \
      'non-PR digest artifact condition'
    require_regex "$upload_step" 'name:[[:space:]]*digests-\$\{\{[[:space:]]*matrix\.image[[:space:]]*\}\}--\$\{\{[[:space:]]*matrix\.platform\.arch' \
      'digest artifact image/architecture identity'
    require_regex "$upload_step" 'path:[[:space:]]*\$\{\{[[:space:]]*runner\.temp[[:space:]]*\}\}/digests/\*' \
      'digest-only artifact path'
    require_regex "$upload_step" 'retention-days:[[:space:]]*1' 'one-day digest retention'
    forbid_regex "$upload_step" 'build/agent-compose|agent-compose-(darwin|linux)-(amd64|arm64)' \
      'binary path in digest artifact upload'
  fi
  upload_count=$(grep -Ec 'actions/upload-artifact@' <<<"$build_job" || true)
  [[ $upload_count -eq 1 ]] || fail "build job has $upload_count upload-artifact steps, expected one digest handoff"

  verify_digest_step=$(step_containing "$build_job" 'verify-agent-compose-image.sh')
  if [[ -z $verify_digest_step ]]; then
    fail 'non-PR daemon digest image verifier step'
  else
    require_regex "$verify_digest_step" "matrix\\.name[[:space:]]*==[[:space:]]*[\"']agent-compose[\"']" \
      'daemon-only digest verification condition'
    require_regex "$verify_digest_step" "github\\.event_name[[:space:]]*!=[[:space:]]*[\"']pull_request[\"']" \
      'non-PR digest verification condition'
    require_regex "$verify_digest_step" 'steps\.build\.outputs\.digest' 'built digest verification reference'
    require_regex "$verify_digest_step" 'matrix\.platform\.arch' 'digest verification matrix architecture'
    require_regex "$verify_digest_step" 'IMAGE_PREFIX.*matrix\.image|matrix\.image.*IMAGE_PREFIX' \
      'digest verification registry image reference'
  fi
fi

if [[ -n $image_smoke_job ]]; then
  require_regex "$image_smoke_job" 'needs:[[:space:]]*build' 'image-smoke dependency on native builds'
  require_regex "$image_smoke_job" 'runs-on:[[:space:]]*ubuntu-latest' 'image-smoke amd64 runner'
  require_regex "$image_smoke_job" 'linux/amd64' 'image-smoke amd64 platform'
  require_regex "$image_smoke_job" 'docker/setup-buildx-action@v3' 'image-smoke Buildx setup'
  smoke_build_count=$(grep -Ec 'docker/build-push-action@v6' <<<"$image_smoke_job" || true)
  [[ $smoke_build_count -eq 2 ]] \
    || fail "image-smoke has $smoke_build_count loadable build steps, expected two"
  load_count=$(grep -Ec 'load:[[:space:]]*true' <<<"$image_smoke_job" || true)
  [[ $load_count -eq 2 ]] || fail "image-smoke has $load_count load:true settings, expected two"
  require_regex "$image_smoke_job" 'file:[[:space:]]*Dockerfile[[:space:]]*$' \
    'image-smoke published daemon Dockerfile'
  require_regex "$image_smoke_job" 'file:[[:space:]]*guest-images/Dockerfile\.agent-compose-guest' \
    'image-smoke published guest Dockerfile'
  require_regex "$image_smoke_job" 'cache-from:[[:space:]]*type=gha,scope=agent-compose-amd64' \
    'image-smoke daemon amd64 cache reuse'
  require_regex "$image_smoke_job" 'cache-from:[[:space:]]*type=gha,scope=agent-compose-guest-amd64' \
    'image-smoke guest amd64 cache reuse'
  require_regex "$image_smoke_job" 'agent-compose:[[:alnum:]_.${}{}/-]*smoke|smoke[[:alnum:]_.${}{}/-]*agent-compose' \
    'image-smoke explicit daemon reference'
  require_regex "$image_smoke_job" 'agent-compose-guest:[[:alnum:]_.${}{}/-]*smoke|smoke[[:alnum:]_.${}{}/-]*agent-compose-guest' \
    'image-smoke explicit guest reference'
  require_regex "$image_smoke_job" '(\./)?scripts/verify-agent-compose-image\.sh' \
    'image-smoke daemon image verifier'
  require_regex "$image_smoke_job" '(expected[_-]?arch|--arch)[[:space:]=]+amd64' \
    'image-smoke verifier amd64 assertion'
  require_regex "$image_smoke_job" '(\./)?scripts/test-image-docker-e2e\.sh' \
    'image-smoke Docker lifecycle E2E'
  for variable in AGENT_COMPOSE_E2E_DAEMON_IMAGE AGENT_COMPOSE_E2E_GUEST_IMAGE AGENT_COMPOSE_E2E_DOCKER_SOCKET; do
    require_regex "$image_smoke_job" "$variable[[:space:]]*[:=]" "image-smoke explicit $variable"
  done
  require_regex "$image_smoke_job" "AGENT_COMPOSE_E2E_DOCKER_SOCKET[[:space:]]*[:=][[:space:]]*[\"']?/var/run/docker\\.sock" \
    'image-smoke explicit Docker socket'
  forbid_regex "$image_smoke_job" '/dev/kvm|--device([=[:space:]]|$)|--privileged|privileged:[[:space:]]*true' \
    'KVM or privileged image-smoke execution'
fi

if [[ -n $merge_job ]]; then
  require_regex "$merge_job" "if:[[:space:]]*github\\.event_name[[:space:]]*!=[[:space:]]*[\"']pull_request[\"']" \
    'non-PR manifest merge condition'
  require_regex "$merge_job" 'needs:[[:space:]]*$' 'merge dependency list'
  require_regex "$merge_job" '^[[:space:]]*-[[:space:]]*build[[:space:]]*$' 'merge dependency on build'
  require_regex "$merge_job" '^[[:space:]]*-[[:space:]]*image-smoke[[:space:]]*$' \
    'merge dependency on successful image smoke'
  require_regex "$merge_job" 'image:[[:space:]]*\[agent-compose,[[:space:]]*agent-compose-guest\]' \
    'daemon and guest manifest matrix'
  require_regex "$merge_job" 'docker buildx imagetools create' 'multi-arch manifest creation'

  manifest_verify_step=$(step_containing "$merge_job" 'verify-image-manifest.sh')
  if [[ -z $manifest_verify_step ]]; then
    fail 'daemon multi-arch manifest verifier step'
  else
    require_regex "$manifest_verify_step" 'linux/amd64' 'manifest linux/amd64 assertion'
    require_regex "$manifest_verify_step" 'linux/arm64' 'manifest linux/arm64 assertion'
    require_regex "$manifest_verify_step" 'IMAGE_PREFIX.*matrix\.image|matrix\.image.*IMAGE_PREFIX' \
      'manifest verification registry image reference'
    create_line=$(grep -n -m1 'docker buildx imagetools create' <<<"$merge_job" | cut -d: -f1)
    verify_line=$(grep -n -m1 'verify-image-manifest\.sh' <<<"$merge_job" | cut -d: -f1)
    if [[ -z $create_line || -z $verify_line || $verify_line -le $create_line ]]; then
      fail 'manifest verification after manifest creation'
    fi
  fi
fi

if [[ -n $release_job ]]; then
  require_regex "$release_job" "if:[[:space:]]*github\\.ref_type[[:space:]]*==[[:space:]]*[\"']tag[\"']" \
    'tag-only release guard'
  require_regex "$release_job" '\./scripts/build-installer-assets\.sh[[:space:]]+\./upload' \
    'shared installer asset builder in release job'
  require_regex "$release_job" 'gh release (upload|create).*upload/\*' \
    'release publication from installer output only'
  forbid_regex "$release_job" 'build/agent-compose|build-agent-compose-binary|agent-compose-(darwin|linux)-(amd64|arm64)' \
    'binary build or binary path in release job'
  forbid_regex "$release_job" 'actions/upload-artifact@' 'workflow artifact upload in release job'
fi

[[ -x $INSTALLER_BUILDER ]] || fail 'executable scripts/build-installer-assets.sh'
[[ -x $INSTALLER_TEST ]] || fail 'executable scripts/tests/test-installer-assets.sh'
if [[ -f $INSTALLER_TEST ]]; then
  require_regex "$(<"$INSTALLER_TEST")" 'build-installer-assets\.sh' \
    'installer asset test coverage of release builder'
fi
[[ -x $IMAGE_E2E ]] || fail 'executable scripts/test-image-docker-e2e.sh'
[[ -x $DAEMON_BUILDER ]] || fail 'executable scripts/build-agent-compose.sh'
[[ -x $GUEST_BUILDER ]] || fail 'executable scripts/build-agent-compose-guest.sh'
[[ -f $GUEST_DOCKERFILE ]] || fail 'default guest Dockerfile'
if [[ -f $DAEMON_BUILDER ]]; then
  daemon_builder_source=$(<"$DAEMON_BUILDER")
  require_regex "$daemon_builder_source" 'docker[[:space:]]+build' \
    'loadable Docker build in daemon image helper'
  require_regex "$daemon_builder_source" 'DOCKERFILE' \
    'daemon Dockerfile selection in image helper'
fi
if [[ -f $GUEST_BUILDER ]]; then
  guest_builder_source=$(<"$GUEST_BUILDER")
  require_regex "$guest_builder_source" 'docker[[:space:]]+build' \
    'loadable Docker build in guest image helper'
  require_regex "$guest_builder_source" 'Dockerfile\.agent-compose-guest' \
    'guest Dockerfile selection in image helper'
fi
if [[ -f $GUEST_DOCKERFILE ]]; then
  guest_dockerfile_source=$(<"$GUEST_DOCKERFILE")
  forbid_regex "$guest_dockerfile_source" 'golang|go-tools|go[[:space:]]+install|grpcurl|protoc-gen-go' \
    'Go toolchain or Go-built tools in default guest image'
  forbid_regex "$guest_dockerfile_source" 'GOPATH=' \
    'Go workspace environment in default guest image'
  forbid_regex "$guest_dockerfile_source" 'PATH=[^[:space:]]*/usr/local/go/bin' \
    'Go toolchain path in default guest image'

  npm_install_run_count=0
  run_block=''
  while IFS= read -r dockerfile_line || [[ -n $dockerfile_line ]]; do
    if [[ -z $run_block ]]; then
      [[ $dockerfile_line == RUN\ * ]] || continue
      run_block=$dockerfile_line
    else
      run_block+=" $dockerfile_line"
    fi
    if [[ $dockerfile_line == *\\ ]]; then
      continue
    fi
    if grep -Eq 'npm[[:space:]]+(ci|install)' <<<"$run_block"; then
      npm_install_run_count=$((npm_install_run_count + 1))
      require_regex "$run_block" 'npm[[:space:]]+cache[[:space:]]+clean[[:space:]]+--force' \
        "npm cache cleanup in guest install layer $npm_install_run_count"
      require_regex "$run_block" 'rm[[:space:]]+-rf[[:space:]]+/root/\.npm' \
        "npm cache directory removal in guest install layer $npm_install_run_count"
    fi
    run_block=''
  done <"$GUEST_DOCKERFILE"
  [[ $npm_install_run_count -gt 0 ]] || fail 'npm install layers in default guest Dockerfile'
fi

FAKE_BIN="$TEST_ROOT/fake-bin"
FAKE_DOCKER_LOG="$TEST_ROOT/docker.log"
mkdir -p "$FAKE_BIN"
cat >"$FAKE_BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ ${1:-} == build ]]
shift
printf '%s\n' "$@" >"$FAKE_DOCKER_LOG"
EOF
chmod +x "$FAKE_BIN/docker"

run_daemon_builder() { # remaining arguments are environment overrides
  : >"$FAKE_DOCKER_LOG"
  env \
    PATH="$FAKE_BIN:$PATH" \
    FAKE_DOCKER_LOG="$FAKE_DOCKER_LOG" \
    IMAGE_NAME=agent-compose:contract \
    DOCKERFILE=Dockerfile \
    BUILD_CONTEXT="$ROOT_DIR" \
    VERSION=contract \
    HTTP_PROXY= HTTPS_PROXY= ALL_PROXY= NO_PROXY= no_proxy= \
    REGISTRY_MIRROR= GOPROXY= \
    "$@" \
    "$DAEMON_BUILDER" >/dev/null
}

run_guest_builder() { # remaining arguments are environment overrides
  : >"$FAKE_DOCKER_LOG"
  env \
    PATH="$FAKE_BIN:$PATH" \
    FAKE_DOCKER_LOG="$FAKE_DOCKER_LOG" \
    GUEST_IMAGE_DOCKERFILE="$ROOT_DIR/guest-images/Dockerfile.agent-compose-guest" \
    IMAGE_TAG=agent-compose-guest:contract \
    REGISTRY_MIRROR= PYPI_INDEX_URL= PYPI_TRUSTED_HOST= \
    "$@" \
    "$GUEST_BUILDER" >/dev/null
}

if ! run_daemon_builder; then
  fail 'daemon image helper default build invocation'
else
  require_regex "$(<"$FAKE_DOCKER_LOG")" '^VERSION=contract$' 'daemon VERSION build argument'
  for omitted in HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY REGISTRY_MIRROR GOPROXY; do
    forbid_regex "$(<"$FAKE_DOCKER_LOG")" "^$omitted=" "empty daemon $omitted build argument"
  done
fi

if ! run_daemon_builder \
  HTTP_PROXY=http://http-proxy.invalid:8080 \
  HTTPS_PROXY=http://https-proxy.invalid:8443 \
  ALL_PROXY=socks5://all-proxy.invalid:1080 \
  NO_PROXY=localhost,.example.invalid \
  REGISTRY_MIRROR=registry.example.invalid \
  GOPROXY=https://go-proxy.example.invalid,direct; then
  fail 'daemon image helper override build invocation'
else
  daemon_log=$(<"$FAKE_DOCKER_LOG")
  for forwarded in \
    'HTTP_PROXY=http://http-proxy.invalid:8080' \
    'HTTPS_PROXY=http://https-proxy.invalid:8443' \
    'ALL_PROXY=socks5://all-proxy.invalid:1080' \
    'NO_PROXY=localhost,.example.invalid' \
    'REGISTRY_MIRROR=registry.example.invalid' \
    'GOPROXY=https://go-proxy.example.invalid,direct'; do
    require_regex "$daemon_log" "^$forwarded$" "daemon $forwarded build argument"
  done
fi

if ! run_guest_builder; then
  fail 'guest image helper default build invocation'
else
  guest_log=$(<"$FAKE_DOCKER_LOG")
  for omitted in REGISTRY_MIRROR PYPI_INDEX_URL PYPI_TRUSTED_HOST; do
    forbid_regex "$guest_log" "^$omitted=" "empty guest $omitted build argument"
  done
fi

if ! run_guest_builder \
  REGISTRY_MIRROR=registry.example.invalid \
  PYPI_INDEX_URL=https://python.example.invalid/simple \
  PYPI_TRUSTED_HOST=python.example.invalid; then
  fail 'guest image helper override build invocation'
else
  guest_log=$(<"$FAKE_DOCKER_LOG")
  for forwarded in \
    'REGISTRY_MIRROR=registry.example.invalid' \
    'PYPI_INDEX_URL=https://python.example.invalid/simple' \
    'PYPI_TRUSTED_HOST=python.example.invalid'; do
    require_regex "$guest_log" "^$forwarded$" "guest $forwarded build argument"
  done
fi

if ((failures > 0)); then
  printf 'test-image-ci-contract: %d contract check(s) failed\n' "$failures" >&2
  exit 1
fi

printf 'test-image-ci-contract: all checks passed\n'
