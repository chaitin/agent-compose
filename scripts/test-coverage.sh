#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ "${AGENT_COMPOSE_GO_TOOLCHAIN_READY:-0}" != "1" ]]; then
  export AGENT_COMPOSE_GO_TOOLCHAIN_READY=1
  exec "$root/scripts/with-go-toolchain.sh" "$0" "$@"
fi

coverage_root="${COVERAGE_DIR:-${AGENT_COMPOSE_COVERAGE_DIR:-"$root/.cache/coverage"}}"
go_cache="${GOCACHE:-"$root/.cache/go-build"}"
go_cgo_enabled="${CGO_ENABLED:-0}"
go_coverpkg="${GO_COVERPKG:-${AGENT_COMPOSE_GO_COVERPKG:-./cmd/...,./pkg/...,./internal/...}}"
go_exclude_regex="${GO_COVER_EXCLUDE_REGEX:-${AGENT_COMPOSE_GO_COVER_EXCLUDE_REGEX:-/(boxlite|boxlite_cgo|boxlite_guest_cgo|boxlite_runtime|boxlite_stub|docker_runtime|microsandbox_runtime|microsandbox_runtime_install|microsandbox_runtime_stub|microsandbox_sandbox_init|k8s_runtime_stub|local_docker_oci|env_path)\\.go:|\\.(pb|connect)\\.go:}}"

mkdir -p "$coverage_root" "$go_cache"
rm -rf "$coverage_root"/*

filter_go_profile() {
  local input="$1"
  local output="$2"
  if [[ -z "$go_exclude_regex" ]]; then
    cp "$input" "$output"
    return
  fi
  awk -v exclude="$go_exclude_regex" '
    /^mode:/ {
      print
      next
    }
    $0 !~ exclude {
      print
    }
  ' "$input" > "$output"
}

write_go_profile() {
  local covdata_dirs="$1"
  local name="$2"
  local raw_profile="$coverage_root/go-$name.raw.out"
  local profile="$coverage_root/go-$name.out"

  go tool covdata textfmt -i="$covdata_dirs" -o "$raw_profile"
  filter_go_profile "$raw_profile" "$profile"
  go tool cover -func="$profile" >/dev/null
}

run_sdk_build() {
  echo "==> Building runtime SDK for coverage"
  (
    cd "$root/runtime/agent-compose-runtime-sdk"
    npm run build
  )
}

run_shape() {
  local shape="$1"
  local go_covdata_dir="$coverage_root/go-$shape-covdata"

  mkdir -p "$go_covdata_dir"
  echo "==> Running $shape tests with coverage"
  (
    cd "$root"
    CGO_ENABLED="$go_cgo_enabled" \
      GOCACHE="$go_cache" \
      GO_COVERAGE_DIR="$go_covdata_dir" \
      ./scripts/run-go-test-shape.sh "$shape" \
      -covermode=count \
      -coverpkg="$go_coverpkg"
  )
  write_go_profile "$go_covdata_dir" "$shape"
  (
    cd "$root/runtime/javascript"
    TEST_SHAPE="$shape" \
      COVERAGE_DIR="$coverage_root/js-$shape" \
      npm run test
  )
  (
    cd "$root/runtime/agent-compose-runtime-sdk"
    TEST_SHAPE="$shape" \
      COVERAGE_DIR="$coverage_root/sdk-$shape" \
      npm run test:coverage
  )
}

run_combined_runtime_coverage() {
  echo "==> Calculating combined runtime coverage"
  (
    cd "$root/runtime/javascript"
    TEST_SHAPE=all \
      COVERAGE_DIR="$coverage_root/js-combined" \
      npm run test
  )
  (
    cd "$root/runtime/agent-compose-runtime-sdk"
    TEST_SHAPE=all \
      COVERAGE_DIR="$coverage_root/sdk-combined" \
      npm run test:coverage
  )
}

go_counts() {
  local profile="$1"
  awk '
    /^mode:/ { next }
    {
      total += $2 + 0
      if (($3 + 0) > 0) {
        covered += $2 + 0
      }
    }
    END {
      printf "%d %d\n", covered, total
    }
  ' "$profile"
}

runtime_counts() {
  local summary="$1"
  node -e '
    const fs = require("node:fs");
    const summary = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    const statements = summary.total.statements;
    console.log(`${statements.covered} ${statements.total}`);
  ' "$summary"
}

format_pct() {
  node -e '
    const covered = Number(process.argv[1]);
    const total = Number(process.argv[2]);
    if (!total) {
      console.log("0.00");
      process.exit(0);
    }
    console.log(((covered / total) * 100).toFixed(2));
  ' "$1" "$2"
}

assert_pct() {
  local name="$1"
  local pct="$2"
  local baseline="$3"
  node -e '
    const [name, pct, baseline] = process.argv.slice(1);
    if (Number(pct) + 1e-9 < Number(baseline)) {
      console.error(`${name} coverage ${pct}% is below required ${baseline}%`);
      process.exit(1);
    }
  ' "$name" "$pct" "$baseline"
}

coverage_pct() {
  local shape="$1"
  read -r go_covered go_total < <(go_counts "$coverage_root/go-$shape.out")
  read -r js_covered js_total < <(runtime_counts "$coverage_root/js-$shape/coverage-summary.json")
  read -r sdk_covered sdk_total < <(runtime_counts "$coverage_root/sdk-$shape/coverage-summary.json")
  local covered=$((go_covered + js_covered + sdk_covered))
  local total=$((go_total + js_total + sdk_total))
  format_pct "$covered" "$total"
}

run_sdk_build
run_shape unit
run_shape integration
run_shape e2e

write_go_profile "$coverage_root/go-unit-covdata,$coverage_root/go-integration-covdata,$coverage_root/go-e2e-covdata" combined
run_combined_runtime_coverage

unit_pct="$(coverage_pct unit)"
integration_pct="$(coverage_pct integration)"
e2e_pct="$(coverage_pct e2e)"
combined_pct="$(coverage_pct combined)"

cat <<EOF

Coverage summary (statements)
  Unit:        ${unit_pct}%
  Integration: ${integration_pct}%
  E2E:         ${e2e_pct}%
  Combined:    ${combined_pct}%

Coverage artifacts: ${coverage_root}
Go CGO_ENABLED: ${go_cgo_enabled}
Go coverpkg: ${go_coverpkg}
Go coverage exclude regex: ${go_exclude_regex:-<none>}
EOF

assert_pct "Unit" "$unit_pct" 60
assert_pct "Integration" "$integration_pct" 60
assert_pct "E2E" "$e2e_pct" 60
assert_pct "Combined" "$combined_pct" 70
