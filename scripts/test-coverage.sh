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
go_coverpkg="${GO_COVERPKG:-${AGENT_COMPOSE_GO_COVERPKG:-./cmd/...,./pkg/...}}"
go_cover_packages=(./cmd/... ./pkg/... ./proto/health/v1 ./proto/health/v1/healthv1connect ./proto/agentcompose/v2 ./proto/agentcompose/v2/agentcomposev2connect)
go_exclude_regex="${GO_COVER_EXCLUDE_REGEX:-${AGENT_COMPOSE_GO_COVER_EXCLUDE_REGEX:-/(boxlite|boxlite_cgo|boxlite_guest_cgo|boxlite_runtime|boxlite_stub|docker_runtime|microsandbox_runtime|microsandbox_runtime_stub|local_docker_oci|env_path)\\.go:|/proto/.*/.*(\\.pb|\\.connect)\\.go:}}"

mkdir -p "$coverage_root" "$go_cache"
rm -rf "$coverage_root"/*

run_shape() {
  local shape="$1"
  echo "==> Running $shape tests with coverage"
  (
    cd "$root"
    CGO_ENABLED="$go_cgo_enabled" GOCACHE="$go_cache" ./scripts/run-go-test-shape.sh "$shape" \
      -covermode=count \
      -coverpkg="$go_coverpkg" \
      -coverprofile="$coverage_root/go-$shape.raw.out"
    filter_go_profile "$coverage_root/go-$shape.raw.out" "$coverage_root/go-$shape.out"
  )
  (
    cd "$root/runtime/javascript"
    TEST_SHAPE="$shape" \
      COVERAGE_DIR="$coverage_root/js-$shape" \
      npm run test
  )
}

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

run_combined_js_coverage() {
  echo "==> Calculating combined JavaScript coverage"
  (
    cd "$root/runtime/javascript"
    TEST_SHAPE=all \
      COVERAGE_DIR="$coverage_root/js-combined" \
      npm run test
  )
}

go_counts() {
  local profile="$1"
  awk '
    /^mode:/ { next }
    {
      key = $1
      statements[key] = $2 + 0
      if (($3 + 0) > 0) {
        covered[key] = 1
      }
    }
    END {
      for (key in statements) {
        total += statements[key]
        if (covered[key]) {
          covered_total += statements[key]
        }
      }
      printf "%d %d\n", covered_total, total
    }
  ' "$profile"
}

js_counts() {
  local summary="$1"
  node -e '
    const fs = require("node:fs");
    const summary = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    const statements = summary.total.statements;
    console.log(`${statements.covered} ${statements.total}`);
  ' "$summary"
}

combined_go_counts() {
  local profiles=("$@")
  awk '
    /^mode:/ { next }
    {
      key = $1
      statements[key] = $2 + 0
      if (($3 + 0) > 0) {
        covered[key] = 1
      }
    }
    END {
      for (key in statements) {
        total += statements[key]
        if (covered[key]) {
          covered_total += statements[key]
        }
      }
      printf "%d %d\n", covered_total, total
    }
  ' "${profiles[@]}"
}

combined_js_counts() {
  node -e '
    const fs = require("node:fs");
    let covered = 0;
    let total = 0;
    for (const file of process.argv.slice(1)) {
      const summary = JSON.parse(fs.readFileSync(file, "utf8"));
      covered += summary.total.statements.covered;
      total += summary.total.statements.total;
    }
    console.log(`${covered} ${total}`);
  ' "$@"
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

shape_coverage() {
  local shape="$1"
  read -r go_covered go_total < <(go_counts "$coverage_root/go-$shape.out")
  read -r js_covered js_total < <(js_counts "$coverage_root/js-$shape/coverage-summary.json")
  local covered=$((go_covered + js_covered))
  local total=$((go_total + js_total))
  format_pct "$covered" "$total"
}

run_shape unit
run_shape integration
run_shape e2e
run_combined_js_coverage

unit_pct="$(shape_coverage unit)"
integration_pct="$(shape_coverage integration)"
e2e_pct="$(shape_coverage e2e)"

read -r go_combined_covered go_combined_total < <(combined_go_counts "$coverage_root/go-unit.out" "$coverage_root/go-integration.out" "$coverage_root/go-e2e.out")
read -r js_combined_covered js_combined_total < <(js_counts "$coverage_root/js-combined/coverage-summary.json")
combined_covered=$((go_combined_covered + js_combined_covered))
combined_total=$((go_combined_total + js_combined_total))
combined_pct="$(format_pct "$combined_covered" "$combined_total")"

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
