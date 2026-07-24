#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [[ "${AGENT_COMPOSE_GO_TOOLCHAIN_READY:-0}" != "1" ]]; then
  export AGENT_COMPOSE_GO_TOOLCHAIN_READY=1
  exec "$root/scripts/with-go-toolchain.sh" "$0" "$@"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

profile_counts() {
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
  ' "$1"
}

assert_equal() {
  local want="$1"
  local got="$2"
  local description="$3"
  if [[ "$want" != "$got" ]]; then
    echo "$description: got $got, want $want" >&2
    exit 1
  fi
}

module="$tmp/go-module"
mkdir -p "$module/shape-one" "$module/shape-two"
(
  cd "$module"
  go mod init example.test/coveragecontract >/dev/null
  cat > coverage.go <<'EOF'
package coveragecontract

func choose(first bool) int {
	if first {
		return 1
	}
	return 2
}
EOF
  cat > coverage_test.go <<'EOF'
package coveragecontract

import "testing"

func TestFirst(t *testing.T) {
	if choose(true) != 1 {
		t.Fatal("unexpected first result")
	}
}

func TestSecond(t *testing.T) {
	if choose(false) != 2 {
		t.Fatal("unexpected second result")
	}
}
EOF

  go test -run '^TestFirst$' -covermode=count -coverpkg=./... . -test.gocoverdir="$module/shape-one" >/dev/null
  go test -run '^TestSecond$' -covermode=count -coverpkg=./... . -test.gocoverdir="$module/shape-two" >/dev/null
  go tool covdata textfmt -i="$module/shape-one" -o "$module/shape-one.out"
  go tool covdata textfmt -i="$module/shape-two" -o "$module/shape-two.out"
  go tool covdata textfmt -i="$module/shape-one,$module/shape-two" -o "$module/combined.out"

  go tool cover -func="$module/shape-one.out" >/dev/null
  go tool cover -func="$module/shape-two.out" >/dev/null
  go tool cover -func="$module/combined.out" >/dev/null

  read -r one_covered one_total < <(profile_counts "$module/shape-one.out")
  read -r two_covered two_total < <(profile_counts "$module/shape-two.out")
  read -r combined_covered combined_total < <(profile_counts "$module/combined.out")
  assert_equal "$one_total" "$two_total" "shape coverage denominators differ"
  assert_equal "$one_total" "$combined_total" "combined coverage denominator was added instead of merged"
  if ((combined_covered <= one_covered || combined_covered <= two_covered)); then
    echo "combined coverage did not union statements from both shapes" >&2
    exit 1
  fi

  read -r range num_stmt count < <(sed -n '2p' "$module/shape-one.out")
  {
    echo "mode: count"
    printf '%s %s %s\n' "$range" "$num_stmt" "$count"
    printf '%s %s %s\n' "$range" "$((num_stmt + 1))" "$count"
  } > "$module/conflict.out"
  if go tool cover -func="$module/conflict.out" >/dev/null 2>&1; then
    echo "conflicting Go coverage profile was accepted" >&2
    exit 1
  fi
)

sdk_summary="$tmp/sdk-integration/coverage-summary.json"
(
  cd "$root/runtime/agent-compose-runtime-sdk"
  TEST_SHAPE=integration COVERAGE_DIR="$tmp/sdk-integration" npm run test:coverage >/dev/null
)
read -r sdk_covered sdk_total < <(
  node -e '
    const fs = require("node:fs");
    const path = require("node:path");
    const summary = require(process.argv[1]);
    const sourceRoot = path.resolve(process.argv[2]);
    const sourceFiles = [];
    const visit = (directory) => {
      for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
        const file = path.join(directory, entry.name);
        if (entry.isDirectory()) {
          visit(file);
        } else if (entry.isFile() && entry.name.endsWith(".ts")) {
          sourceFiles.push(file);
        }
      }
    };
    visit(sourceRoot);
    const missing = sourceFiles.filter((file) => !(file in summary));
    if (missing.length > 0) {
      console.error(`SDK coverage summary omitted source files: ${missing.join(", ")}`);
      process.exit(1);
    }
    console.log(`${summary.total.statements.covered} ${summary.total.statements.total}`);
  ' "$sdk_summary" "$root/runtime/agent-compose-runtime-sdk/src"
)
assert_equal 0 "$sdk_covered" "zero-test SDK shape recorded covered statements"
if ((sdk_total == 0)); then
  echo "zero-test SDK shape omitted the source denominator" >&2
  exit 1
fi

listed_e2e_tests="$(go test -list '^Test' ./test/e2e | awk '/^Test/ { print }')"
listed_e2e_count="$(printf '%s\n' "$listed_e2e_tests" | awk 'NF { count++ } END { print count + 0 }')"
assert_equal 8 "$listed_e2e_count" "unexpected test/e2e package test count"

e2e_output="$(go test -run '^Test' -v ./test/e2e)"
while IFS= read -r test_name; do
  if ! grep -Fq "=== RUN   $test_name" <<<"$e2e_output"; then
    echo "E2E package test was not scheduled: $test_name" >&2
    exit 1
  fi
done <<<"$listed_e2e_tests"

echo "coverage contract checks passed"
