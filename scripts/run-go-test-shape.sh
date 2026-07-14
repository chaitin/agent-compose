#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ "${AGENT_COMPOSE_GO_TOOLCHAIN_READY:-0}" != "1" ]]; then
  export AGENT_COMPOSE_GO_TOOLCHAIN_READY=1
  exec "$root/scripts/with-go-toolchain.sh" "$0" "$@"
fi

shape="${1:?usage: run-go-test-shape.sh <unit|integration|e2e> [go-test-args...]}"
shift || true

extra_packages=()
base_packages=(./cmd/... ./pkg/... ./proto/health/v1 ./proto/health/v1/healthv1connect ./proto/agentcompose/v2 ./proto/agentcompose/v2/agentcomposev2connect)
case "$shape" in
  unit)
    packages=("${base_packages[@]}")
    ;;
  integration)
    packages=("${base_packages[@]}")
    ;;
  e2e)
    packages=("${base_packages[@]}")
    extra_packages=(./test/e2e)
    ;;
  *)
    echo "unknown Go test shape: $shape" >&2
    exit 2
    ;;
esac

tests="$(
  go test -list '^Test' "${packages[@]}" |
    awk -v shape="$shape" '
      /^Test/ {
        is_integration = index($0, "Integration") > 0
        is_e2e = index($0, "E2E") > 0
        if (shape == "unit" && !is_integration && !is_e2e) {
          print $0
        } else if (shape == "integration" && is_integration) {
          print $0
        } else if (shape == "e2e" && is_e2e) {
          print $0
        }
      }
    '
)"

if [[ -z "$tests" ]]; then
  echo "no Go $shape tests found" >&2
  exit 1
fi

pattern="^($(printf '%s\n' "$tests" | paste -sd '|' -))$"
go test -run "$pattern" "$@" "${packages[@]}"

if [[ ${#extra_packages[@]} -gt 0 ]]; then
  go test -run '^TestE2E' "${extra_packages[@]}"
fi
