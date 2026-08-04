#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
TASKFILE="$ROOT_DIR/Taskfile.yml"
WORKFLOW="$ROOT_DIR/.github/workflows/images.yml"
INSTALLER_BUILDER="$ROOT_DIR/scripts/build-installer-assets.sh"
INSTALLER_TEST="$ROOT_DIR/scripts/tests/test-installer-assets.sh"
IMAGE_E2E="$ROOT_DIR/scripts/test-image-docker-e2e.sh"
DAEMON_BUILDER="$ROOT_DIR/scripts/build-agent-compose.sh"
GUEST_BUILDER="$ROOT_DIR/scripts/build-agent-compose-guest.sh"
GUEST_DOCKERFILE="$ROOT_DIR/guest-images/Dockerfile.agent-compose-guest"
ARCHLINUX_GUEST_DOCKERFILE="$ROOT_DIR/guest-images/Dockerfile.agent-compose-guest-archlinux"
DEVBOX_DOCKERFILE="$ROOT_DIR/guest-images/Dockerfile.devbox-archlinux"
DAEMON_DOCKERFILE="$ROOT_DIR/Dockerfile"
LOCAL_DAEMON_DOCKERFILE="$ROOT_DIR/Dockerfile.agent-compose-local"
COMPOSE_OVERRIDE="$ROOT_DIR/docker-compose.override.yml.example"

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

public_build_sources=''
for source_file in \
  "$DAEMON_DOCKERFILE" \
  "$LOCAL_DAEMON_DOCKERFILE" \
  "$GUEST_DOCKERFILE" \
  "$ARCHLINUX_GUEST_DOCKERFILE" \
  "$DEVBOX_DOCKERFILE" \
  "$WORKFLOW" \
  "$COMPOSE_OVERRIDE"; do
  public_build_sources+=$(<"$source_file")
  public_build_sources+=$'\n'
done

for regional_mirror in \
  'mirrors\.tuna\.tsinghua\.edu\.cn' \
  'goproxy\.cn' \
  'mirrors\.aliyun\.com' \
  'registry\.npmmirror\.com'; do
  forbid_regex "$public_build_sources" "$regional_mirror" \
    "regional mirror default $regional_mirror in public build configuration"
done

for daemon_dockerfile in "$DAEMON_DOCKERFILE" "$LOCAL_DAEMON_DOCKERFILE"; do
  require_regex "$(<"$daemon_dockerfile")" \
    '^ARG[[:space:]]+GOPROXY=https://proxy\.golang\.org,direct$' \
    "official Go proxy default in $(basename "$daemon_dockerfile")"
done

require_regex "$(<"$GUEST_DOCKERFILE")" \
  '^ARG[[:space:]]+PYPI_INDEX_URL=https://pypi\.org/simple$' \
  'official PyPI default in Debian guest Dockerfile'
require_regex "$(<"$ARCHLINUX_GUEST_DOCKERFILE")" \
  '^ARG[[:space:]]+ARCHLINUX_MIRROR[[:space:]]*$' \
  'upstream mirrorlist default in Arch Linux guest Dockerfile'
for guest_dockerfile in "$GUEST_DOCKERFILE" "$ARCHLINUX_GUEST_DOCKERFILE"; do
  require_regex "$(<"$guest_dockerfile")" \
    'npm[[:space:]]+config[[:space:]]+set[[:space:]]+registry[[:space:]]+https://registry\.npmjs\.org' \
    "official npm registry in $(basename "$guest_dockerfile")"
  require_regex "$(<"$guest_dockerfile")" \
    '^ARG[[:space:]]+PYPI_TRUSTED_HOST[[:space:]]*$' \
    "empty trusted PyPI host default in $(basename "$guest_dockerfile")"
done

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
require_regex "$header" 'REGISTRY:[[:space:]]*docker\.io' 'Docker Hub registry'
require_regex "$header" 'IMAGE_PREFIX:[[:space:]]*chaitin' 'Docker Hub chaitin namespace'
forbid_regex "$header" 'ghcr\.io' 'GHCR registry configuration'
require_regex "$header" '^[[:space:]]*-[[:space:]]*"\*"[[:space:]]*$' \
  'all-tag image publication trigger'
forbid_regex "$header" '^[[:space:]]*-[[:space:]]*"v\*"[[:space:]]*$' \
  'version-only image publication trigger'

load_job setup setup_job
load_job build build_job
load_job build-archlinux-guest archlinux_build_job
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
  require_regex "$build_job" 'username:[[:space:]]*\$\{\{[[:space:]]*secrets\.DOCKERIO_USERNAME' \
    'Docker Hub username secret'
  require_regex "$build_job" 'password:[[:space:]]*\$\{\{[[:space:]]*secrets\.DOCKERIO_PASSWORD' \
    'Docker Hub password secret'
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

if [[ -n $archlinux_build_job ]]; then
  require_regex "$archlinux_build_job" 'needs:[[:space:]]*setup' \
    'Arch Linux guest build dependency on setup'
  require_regex "$archlinux_build_job" 'runs-on:[[:space:]]*ubuntu-latest' \
    'Arch Linux guest native amd64 runner'
  require_regex "$archlinux_build_job" 'username:[[:space:]]*\$\{\{[[:space:]]*secrets\.DOCKERIO_USERNAME' \
    'Arch Linux Docker Hub username secret'
  require_regex "$archlinux_build_job" 'password:[[:space:]]*\$\{\{[[:space:]]*secrets\.DOCKERIO_PASSWORD' \
    'Arch Linux Docker Hub password secret'
  require_regex "$archlinux_build_job" 'file:[[:space:]]*guest-images/Dockerfile\.agent-compose-guest-archlinux' \
    'Arch Linux guest published Dockerfile'
  require_regex "$archlinux_build_job" 'platforms:[[:space:]]*linux/amd64' \
    'Arch Linux guest amd64 publication platform'
  forbid_regex "$archlinux_build_job" 'linux/arm64' \
    'Arch Linux guest arm64 publication platform'
  require_regex "$archlinux_build_job" 'images:[[:space:]]*\$\{\{[[:space:]]*env\.IMAGE_PREFIX[[:space:]]*\}\}/agent-compose-guest[[:space:]]*$' \
    'Arch Linux guest shared registry repository'
  require_regex "$archlinux_build_job" 'outputs:.*name=\$\{\{[[:space:]]*env\.IMAGE_PREFIX[[:space:]]*\}\}/agent-compose-guest,' \
    'Arch Linux guest shared digest repository'
  forbid_regex "$archlinux_build_job" 'images:.*agent-compose-guest-archlinux|outputs:.*name=.*agent-compose-guest-archlinux' \
    'standalone Arch Linux guest registry repository'
  require_regex "$archlinux_build_job" 'push-by-digest=true' \
    'Arch Linux guest push-by-digest output'
  require_regex "$archlinux_build_job" 'name-canonical=true' \
    'Arch Linux guest canonical digest output'
  require_regex "$archlinux_build_job" "push=\\$\\{\\{[[:space:]]*github\\.event_name[[:space:]]*!=[[:space:]]*[\"']pull_request[\"']" \
    'Arch Linux guest non-PR digest push condition'
  require_regex "$archlinux_build_job" 'cache-from:[[:space:]]*type=gha,scope=agent-compose-guest-archlinux-amd64' \
    'Arch Linux guest amd64 GHA cache read'
  require_regex "$archlinux_build_job" 'cache-to:[[:space:]]*type=gha,mode=max,scope=agent-compose-guest-archlinux-amd64' \
    'Arch Linux guest amd64 GHA cache write'

  archlinux_upload_step=$(step_containing "$archlinux_build_job" 'actions/upload-artifact@')
  if [[ -z $archlinux_upload_step ]]; then
    fail 'Arch Linux guest digest artifact upload step'
  else
    require_regex "$archlinux_upload_step" "if:[[:space:]]*github\\.event_name[[:space:]]*!=[[:space:]]*[\"']pull_request[\"']" \
      'Arch Linux guest non-PR digest artifact condition'
    require_regex "$archlinux_upload_step" 'name:[[:space:]]*digests-agent-compose-guest-archlinux--amd64' \
      'Arch Linux guest digest artifact identity'
    require_regex "$archlinux_upload_step" 'retention-days:[[:space:]]*1' \
      'Arch Linux guest one-day digest retention'
  fi
fi

if [[ -n $image_smoke_job ]]; then
  require_regex "$image_smoke_job" '^[[:space:]]*-[[:space:]]*build[[:space:]]*$' \
    'image-smoke dependency on native builds'
  require_regex "$image_smoke_job" '^[[:space:]]*-[[:space:]]*build-archlinux-guest[[:space:]]*$' \
    'image-smoke dependency on Arch Linux guest build'
  require_regex "$image_smoke_job" 'runs-on:[[:space:]]*ubuntu-latest' 'image-smoke amd64 runner'
  require_regex "$image_smoke_job" 'linux/amd64' 'image-smoke amd64 platform'
  require_regex "$image_smoke_job" 'go install github\.com/bufbuild/buf/cmd/buf@v1\.68\.1' \
    'image-smoke Buf installation for host tests'
  require_regex "$image_smoke_job" 'buf generate' \
    'image-smoke protobuf generation for host tests'
  require_regex "$image_smoke_job" 'docker/setup-buildx-action@v3' 'image-smoke Buildx setup'
  smoke_build_count=$(grep -Ec 'docker/build-push-action@v6' <<<"$image_smoke_job" || true)
  [[ $smoke_build_count -eq 3 ]] \
    || fail "image-smoke has $smoke_build_count loadable build steps, expected three"
  load_count=$(grep -Ec 'load:[[:space:]]*true' <<<"$image_smoke_job" || true)
  [[ $load_count -eq 3 ]] || fail "image-smoke has $load_count load:true settings, expected three"
  require_regex "$image_smoke_job" 'file:[[:space:]]*Dockerfile[[:space:]]*$' \
    'image-smoke published daemon Dockerfile'
  require_regex "$image_smoke_job" 'file:[[:space:]]*guest-images/Dockerfile\.agent-compose-guest' \
    'image-smoke published guest Dockerfile'
  require_regex "$image_smoke_job" 'file:[[:space:]]*guest-images/Dockerfile\.agent-compose-guest-archlinux' \
    'image-smoke published Arch Linux guest Dockerfile'
  require_regex "$image_smoke_job" 'cache-from:[[:space:]]*type=gha,scope=agent-compose-amd64' \
    'image-smoke daemon amd64 cache reuse'
  require_regex "$image_smoke_job" 'cache-from:[[:space:]]*type=gha,scope=agent-compose-guest-amd64' \
    'image-smoke guest amd64 cache reuse'
  require_regex "$image_smoke_job" 'cache-from:[[:space:]]*type=gha,scope=agent-compose-guest-archlinux-amd64' \
    'image-smoke Arch Linux guest amd64 cache reuse'
  require_regex "$image_smoke_job" 'agent-compose:[[:alnum:]_.${}{}/-]*smoke|smoke[[:alnum:]_.${}{}/-]*agent-compose' \
    'image-smoke explicit daemon reference'
  require_regex "$image_smoke_job" 'agent-compose-guest:[[:alnum:]_.${}{}/-]*smoke|smoke[[:alnum:]_.${}{}/-]*agent-compose-guest' \
    'image-smoke explicit guest reference'
  require_regex "$image_smoke_job" 'agent-compose-guest:archlinux-[[:alnum:]_.${}{}/-]*smoke' \
    'image-smoke explicit Arch Linux guest reference'
  require_regex "$image_smoke_job" '(\./)?scripts/verify-agent-compose-image\.sh' \
    'image-smoke daemon image verifier'
  require_regex "$image_smoke_job" '(expected[_-]?arch|--arch)[[:space:]=]+amd64' \
    'image-smoke verifier amd64 assertion'
  require_regex "$image_smoke_job" '(\./)?scripts/test-image-docker-e2e\.sh' \
    'image-smoke Docker lifecycle E2E'
  lifecycle_smoke_count=$(grep -Ec '(\./)?scripts/test-image-docker-e2e\.sh' <<<"$image_smoke_job" || true)
  [[ $lifecycle_smoke_count -eq 2 ]] \
    || fail "image-smoke has $lifecycle_smoke_count lifecycle runs, expected two"
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
  require_regex "$merge_job" '^[[:space:]]*-[[:space:]]*build-archlinux-guest[[:space:]]*$' \
    'merge dependency on Arch Linux guest build'
  require_regex "$merge_job" '^[[:space:]]*-[[:space:]]*image-smoke[[:space:]]*$' \
    'merge dependency on successful image smoke'
  require_regex "$merge_job" '^[[:space:]]*-[[:space:]]*image:[[:space:]]*agent-compose[[:space:]]*$' \
    'daemon manifest matrix entry'
  require_regex "$merge_job" '^[[:space:]]*-[[:space:]]*image:[[:space:]]*agent-compose-guest[[:space:]]*$' \
    'default guest manifest matrix entry'
  guest_manifest_count=$(grep -Ec '^[[:space:]]*-[[:space:]]*image:[[:space:]]*agent-compose-guest[[:space:]]*$' <<<"$merge_job" || true)
  [[ $guest_manifest_count -eq 2 ]] \
    || fail "merge job has $guest_manifest_count guest manifest entries, expected default and Arch Linux variants"
  require_regex "$merge_job" 'artifact:[[:space:]]*agent-compose-guest-archlinux' \
    'Arch Linux guest digest artifact matrix entry'
  require_regex "$merge_job" 'tag_suffix:[[:space:]]*-archlinux' \
    'Arch Linux guest variant tag suffix'
  require_regex "$merge_job" 'stable_tag:[[:space:]]*archlinux' \
    'Arch Linux guest stable tag'
  require_regex "$merge_job" 'platforms:[[:space:]]*linux/amd64,linux/arm64' \
    'default image multi-architecture manifest entries'
  require_regex "$merge_job" 'platforms:[[:space:]]*linux/amd64[[:space:]]*$' \
    'Arch Linux guest amd64-only manifest entry'
  require_regex "$merge_job" 'docker buildx imagetools create' 'multi-arch manifest creation'
  require_regex "$merge_job" 'type=ref,event=tag,suffix=\$\{\{[[:space:]]*matrix\.tag_suffix' \
    'variant-aware pushed Git tag image metadata'
  metadata_step=$(step_containing "$merge_job" 'docker/metadata-action@v5')
  if [[ -z $metadata_step ]]; then
    fail 'manifest Docker metadata step'
  else
    require_regex "$metadata_step" 'flavor:[[:space:]]*latest=false' \
      'disabled automatic latest tag metadata'
  fi
  require_regex "$merge_job" "type=raw,value=\\$\\{\\{[[:space:]]*matrix\.stable_tag[[:space:]]*\\}\\},enable=\\$\\{\\{[[:space:]]*startsWith\\(github\\.ref,[[:space:]]*['\"]refs/tags/v['\"]\\)[[:space:]]*\\}\\}" \
    'version-tag-only stable image metadata'
  require_regex "$merge_job" 'username:[[:space:]]*\$\{\{[[:space:]]*secrets\.DOCKERIO_USERNAME' \
    'manifest Docker Hub username secret'
  require_regex "$merge_job" 'password:[[:space:]]*\$\{\{[[:space:]]*secrets\.DOCKERIO_PASSWORD' \
    'manifest Docker Hub password secret'

  manifest_verify_step=$(step_containing "$merge_job" 'verify-image-manifest.sh')
  if [[ -z $manifest_verify_step ]]; then
    fail 'daemon multi-arch manifest verifier step'
  else
    require_regex "$manifest_verify_step" 'matrix\.platforms' 'per-image manifest platform assertion'
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
  require_regex "$release_job" "if:.*github\\.ref_type[[:space:]]*==[[:space:]]*['\"]tag['\"].*startsWith\\(github\\.ref_name,[[:space:]]*['\"]v['\"]\\)" \
    'version-tag-only release guard'
  require_regex "$release_job" '\./scripts/build-installer-assets\.sh[[:space:]]+\./upload' \
    'shared installer asset builder in release job'
  require_regex "$release_job" 'AGENT_COMPOSE_FRONTEND_VERSION:.*vars\.AGENT_COMPOSE_FRONTEND_VERSION' \
    'release frontend default version input'
  require_regex "$release_job" 'AGENT_COMPOSE_FRONTEND_VERSIONS:.*vars\.AGENT_COMPOSE_FRONTEND_VERSIONS' \
    'release supported frontend versions input'
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
[[ -f $ARCHLINUX_GUEST_DOCKERFILE ]] || fail 'Arch Linux guest Dockerfile'
if [[ -f $TASKFILE ]]; then
  taskfile_source=$(<"$TASKFILE")
  require_regex "$taskfile_source" 'image:agent-compose-guest-archlinux:' \
    'Arch Linux guest image task'
  require_regex "$taskfile_source" 'GUEST_IMAGE_DOCKERFILE=.*Dockerfile\.agent-compose-guest-archlinux' \
    'Arch Linux guest Dockerfile selection in task'
  require_regex "$taskfile_source" 'agent-compose-guest:archlinux' \
    'Arch Linux guest default local tag'
  require_regex "$taskfile_source" 'BUILD_PLATFORM=.*linux/amd64' \
    'Arch Linux guest amd64 build platform'
fi
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
  require_regex "$guest_dockerfile_source" 'FROM[^[:space:]]*[[:space:]]+[^[:space:]]*golang:\$\{GO_VERSION\}-alpine[[:space:]]+AS[[:space:]]+grpcurl-builder' \
    'isolated grpcurl builder stage in default guest image'
  require_regex "$guest_dockerfile_source" 'go[[:space:]]+install[[:space:]]+github\.com/fullstorydev/grpcurl/cmd/grpcurl@"\$\{GRPCURL_VERSION\}"' \
    'versioned grpcurl build in isolated builder stage'
  require_regex "$guest_dockerfile_source" 'COPY[[:space:]]+--from=grpcurl-builder[[:space:]]+/out/grpcurl[[:space:]]+/usr/local/bin/grpcurl' \
    'standalone grpcurl binary copied into default guest image'
  grpcurl_builder_copy_count=$(grep -Ec '^[[:space:]]*COPY[[:space:]]+--from=grpcurl-builder' <<<"$guest_dockerfile_source")
  [[ $grpcurl_builder_copy_count -eq 1 ]] || \
    fail 'only the standalone grpcurl binary copied from grpcurl builder stage'
  forbid_regex "$guest_dockerfile_source" '/usr/local/go' \
    'Go toolchain path in default guest image'
  forbid_regex "$guest_dockerfile_source" 'protoc-gen-go' \
    'protobuf Go generators in default guest image'
  forbid_regex "$guest_dockerfile_source" 'GOPATH=' \
    'Go workspace environment in default guest image'
  forbid_regex "$guest_dockerfile_source" 'PATH=[^[:space:]]*/usr/local/go/bin' \
    'Go toolchain path in default guest image'
  require_regex "$guest_dockerfile_source" 'ARG[[:space:]]+PI_AGENT_VERSION=[0-9]+\.[0-9]+\.[0-9]+' \
    'pinned Pi coding agent version in default guest image'
  require_regex "$guest_dockerfile_source" '"@earendil-works/pi-coding-agent@\$\{PI_AGENT_VERSION\}"' \
    'versioned Pi coding agent install in default guest image'
  require_regex "$guest_dockerfile_source" 'pi[[:space:]]+--version' \
    'Pi coding agent build-time smoke in default guest image'

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

for daemon_dockerfile in "$ROOT_DIR/Dockerfile" "$ROOT_DIR/Dockerfile.agent-compose-local"; do
  [[ -f $daemon_dockerfile ]] || {
    fail "daemon Dockerfile $daemon_dockerfile"
    continue
  }
  daemon_dockerfile_source=$(<"$daemon_dockerfile")
  forbid_regex "$daemon_dockerfile_source" '^[[:space:]]*ENV[[:space:]]+SANDBOX_ROOT=' \
    "sandbox root override in $(basename "$daemon_dockerfile"); daemon images must use the DATA_ROOT-derived application default"
done

daemon_dockerfile_source=$(<"$ROOT_DIR/Dockerfile")
require_regex "$daemon_dockerfile_source" '^ARG[[:space:]]+GITHUB_MIRROR=https://github\.com$' \
  'default GitHub download mirror in daemon Dockerfile'
require_regex "$daemon_dockerfile_source" '\$\{GITHUB_MIRROR\}/boxlite-ai/boxlite/releases/' \
  'configurable BoxLite release download mirror'
require_regex "$daemon_dockerfile_source" '\$\{GITHUB_MIRROR\}/superradcompany/microsandbox/releases/' \
  'configurable Microsandbox release download mirror'
forbid_regex "$daemon_dockerfile_source" 'https://github\.com/(boxlite-ai/boxlite|superradcompany/microsandbox)/releases/' \
  'hard-coded GitHub native runtime release downloads'

image_verifier_source=$(<"$ROOT_DIR/scripts/verify-agent-compose-image.sh")
if [[ -f $ARCHLINUX_GUEST_DOCKERFILE ]]; then
  archlinux_guest_source=$(<"$ARCHLINUX_GUEST_DOCKERFILE")
  require_regex "$archlinux_guest_source" 'FROM[[:space:]]+\$\{REGISTRY_MIRROR\}/library/archlinux:\$\{ARCHLINUX_TAG\}' \
    'configurable Arch Linux base image'
  require_regex "$archlinux_guest_source" 'pacman[[:space:]]+-Syu' \
    'Arch Linux package installation'
  require_regex "$archlinux_guest_source" 'cat[[:space:]]+/etc/pacman\.d/mirrorlist' \
    'Arch Linux package mirror fallback'
  require_regex "$archlinux_guest_source" 'nodejs-lts-jod' \
    'Node.js 22 LTS in Arch Linux guest image'
  require_regex "$archlinux_guest_source" 'ARG[[:space:]]+PI_AGENT_VERSION=[0-9]+\.[0-9]+\.[0-9]+' \
    'pinned Pi coding agent version in Arch Linux guest image'
  require_regex "$archlinux_guest_source" '"@earendil-works/pi-coding-agent@\$\{PI_AGENT_VERSION\}"' \
    'versioned Pi coding agent install in Arch Linux guest image'
  require_regex "$archlinux_guest_source" '"pi-mcp-adapter@\$\{PI_MCP_ADAPTER_VERSION\}"' \
    'versioned Pi MCP adapter install in Arch Linux guest image'
  require_regex "$archlinux_guest_source" '/usr/bin/pi' \
    'stable Pi executable path in Arch Linux guest image'
  require_regex "$archlinux_guest_source" '/usr/local/share/agent-compose/pi-mcp-adapter/index\.ts' \
    'stable Pi MCP adapter path in Arch Linux guest image'
  forbid_regex "$archlinux_guest_source" '^[[:space:]]*base-devel[[:space:]]*\\' \
    'full Arch Linux development package group'
  require_regex "$archlinux_guest_source" 'allow-scripts=.*@anthropic-ai/claude-code.*opencode-ai' \
    'required npm install scripts in Arch Linux guest image'
  require_regex "$archlinux_guest_source" 'BUILDPLATFORM.*!=.*TARGETPLATFORM' \
    'cross-platform pacman sandbox compatibility guard'
  require_regex "$archlinux_guest_source" "sed -i 's/\^DisableSandboxSyscalls/#DisableSandboxSyscalls/'" \
    'pacman syscall sandbox restoration'
  require_regex "$archlinux_guest_source" 'COPY[[:space:]]+runtime/javascript[[:space:]]+/tmp/agent-compose-runtime' \
    'runtime source in Arch Linux guest image'
  require_regex "$archlinux_guest_source" 'agent-compose-runtime-sdk\.tgz' \
    'offline runtime SDK in Arch Linux guest image'
  require_regex "$archlinux_guest_source" 'arm64\|aarch64.*claude-code-linux-arm64' \
    'architecture-aware Claude Code package in Arch Linux guest image'
  require_regex "$archlinux_guest_source" 'ENTRYPOINT[[:space:]]+\["/usr/bin/catatonit",[[:space:]]*"--"\]' \
    'catatonit entrypoint in Arch Linux guest image'
  require_regex "$archlinux_guest_source" 'CMD[[:space:]]+\["/usr/local/bin/agent-compose-env"' \
    'long-running default command in Arch Linux guest image'
  for provider_cli in codex claude gemini opencode pi; do
    require_regex "$archlinux_guest_source" "$provider_cli[[:space:]]+--version" \
      "$provider_cli build-time validation in Arch Linux guest image"
  done
  runtime_cleanup_line=$(awk '/rm -rf \/tmp\/agent-compose-runtime[[:space:]]*&&|rm -rf \/tmp\/agent-compose-runtime[[:space:]]*$/ { print NR; exit }' "$ARCHLINUX_GUEST_DOCKERFILE")
  provider_validation_end_line=$(awk '/pi --version/ { print NR; exit }' "$ARCHLINUX_GUEST_DOCKERFILE")
  if [[ -z $runtime_cleanup_line || -z $provider_validation_end_line ]] ||
    ((runtime_cleanup_line <= provider_validation_end_line)); then
    fail 'Arch Linux guest runtime cleanup after provider build-time validation'
  fi
fi

for provider_dockerfile in "$GUEST_DOCKERFILE" "$ARCHLINUX_GUEST_DOCKERFILE" "$DEVBOX_DOCKERFILE"; do
  [[ -f $provider_dockerfile ]] || {
    fail "provider-capable guest Dockerfile $provider_dockerfile"
    continue
  }
  provider_source=$(<"$provider_dockerfile")
  for provider_package in \
    '@openai/codex' \
    '@anthropic-ai/claude-code' \
    '@google/gemini-cli' \
    'opencode-ai' \
    '@earendil-works/pi-coding-agent'; do
    require_regex "$provider_source" "$provider_package" \
      "provider package $provider_package in $(basename "$provider_dockerfile")"
  done
done

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
    BUILD_PLATFORM= \
    REGISTRY_MIRROR= GOPROXY= GO_VERSION= GRPCURL_VERSION= \
    PYPI_INDEX_URL= PYPI_TRUSTED_HOST= ARCHLINUX_TAG= ARCHLINUX_MIRROR= \
    CODEX_VERSION= CLAUDE_CODE_VERSION= GEMINI_CLI_VERSION= OPENCODE_VERSION= \
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
  for omitted in REGISTRY_MIRROR GOPROXY GO_VERSION GRPCURL_VERSION PYPI_INDEX_URL PYPI_TRUSTED_HOST \
    ARCHLINUX_TAG ARCHLINUX_MIRROR CODEX_VERSION CLAUDE_CODE_VERSION GEMINI_CLI_VERSION OPENCODE_VERSION \
    PI_AGENT_VERSION PI_MCP_ADAPTER_VERSION; do
    forbid_regex "$guest_log" "^$omitted=" "empty guest $omitted build argument"
  done
fi

if ! run_guest_builder BUILD_PLATFORM=linux/amd64; then
  fail 'guest image helper platform override invocation'
else
  guest_log=$(<"$FAKE_DOCKER_LOG")
  require_regex "$guest_log" '^--platform$' 'guest build platform flag'
  require_regex "$guest_log" '^linux/amd64$' 'guest build platform value'
fi

if ! run_guest_builder \
  REGISTRY_MIRROR=registry.example.invalid \
  GOPROXY=https://go-proxy.example.invalid,direct \
  GO_VERSION=1.99.0 \
  GRPCURL_VERSION=v9.9.1 \
  PYPI_INDEX_URL=https://python.example.invalid/simple \
  PYPI_TRUSTED_HOST=python.example.invalid \
  ARCHLINUX_TAG=base-test \
  ARCHLINUX_MIRROR=https://arch.example.invalid \
  CODEX_VERSION=9.1.0 \
  CLAUDE_CODE_VERSION=9.2.0 \
  GEMINI_CLI_VERSION=9.3.0 \
  OPENCODE_VERSION=9.4.0 \
  PI_AGENT_VERSION=9.5.0 \
  PI_MCP_ADAPTER_VERSION=9.6.0; then
  fail 'guest image helper override build invocation'
else
  guest_log=$(<"$FAKE_DOCKER_LOG")
  for forwarded in \
    'REGISTRY_MIRROR=registry.example.invalid' \
    'GOPROXY=https://go-proxy.example.invalid,direct' \
    'GO_VERSION=1.99.0' \
    'GRPCURL_VERSION=v9.9.1' \
    'PYPI_INDEX_URL=https://python.example.invalid/simple' \
    'PYPI_TRUSTED_HOST=python.example.invalid' \
    'ARCHLINUX_TAG=base-test' \
    'ARCHLINUX_MIRROR=https://arch.example.invalid' \
    'CODEX_VERSION=9.1.0' \
    'CLAUDE_CODE_VERSION=9.2.0' \
    'GEMINI_CLI_VERSION=9.3.0' \
    'OPENCODE_VERSION=9.4.0' \
    'PI_AGENT_VERSION=9.5.0' \
    'PI_MCP_ADAPTER_VERSION=9.6.0'; do
    require_regex "$guest_log" "^$forwarded$" "guest $forwarded build argument"
  done
fi

if ((failures > 0)); then
  printf 'test-image-ci-contract: %d contract check(s) failed\n' "$failures" >&2
  exit 1
fi

printf 'test-image-ci-contract: all checks passed\n'
