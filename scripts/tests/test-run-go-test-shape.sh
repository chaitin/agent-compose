#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
real_go="$(command -v go)"
mkdir -p "$tmp/scripts" "$tmp/bin" "$tmp/cmd/example" "$tmp/internal/example" "$tmp/pkg/example" "$tmp/test/e2e"
cp "$root/scripts/run-go-test-shape.sh" "$tmp/scripts/"
printf 'module example.test/shapes\n\ngo 1.26.2\n' >"$tmp/go.mod"
for directory in cmd/example internal/example; do
  printf 'package example\n' >"$tmp/$directory/example.go"
done
cat >"$tmp/pkg/example/example_test.go" <<'EOF'
package example
import "testing"
func TestUnit(t *testing.T) {
  t.Run("IntegrationChild", func(t *testing.T) { t.Log("nested-integration-ran") })
  t.Run("E2EChild", func(t *testing.T) { t.Log("nested-e2e-ran") })
}
func TestIntegrationOnly(t *testing.T) {}
func TestE2EOnly(t *testing.T) {}
func TestIntegrationE2EBoth(t *testing.T) {}
EOF
# The old enumerated -run would exceed Linux's usual 128 KiB argument limit.
awk 'BEGIN { for (i=0;i<3000;i++) printf "func TestUnitInventoryForCoverageArgumentLimit%04d(t *testing.T) {}\n",i }' >>"$tmp/pkg/example/example_test.go"
cat >"$tmp/test/e2e/public_test.go" <<'EOF'
package e2e
import "testing"
func TestPublicContract(t *testing.T) {}
EOF
cat >"$tmp/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
for argument in "$@"; do
  if ((${#argument} >= 131072)); then
    echo 'single Go argument exceeded Linux limit' >&2
    exit 126
  fi
done
exec "$REAL_GO" "$@"
EOF
chmod +x "$tmp/bin/go"
(
  cd "$tmp"
  for shape in unit integration e2e; do
    AGENT_COMPOSE_GO_TOOLCHAIN_READY=1 REAL_GO="$real_go" PATH="$tmp/bin:$PATH" GO_COVERAGE_DIR="$tmp/coverage-$shape" \
      ./scripts/run-go-test-shape.sh "$shape" -covermode=count -count=1 -v >"$shape.log"
  done
)
grep -Fq 'nested-integration-ran' "$tmp/unit.log"
grep -Fq 'nested-e2e-ran' "$tmp/unit.log"
grep -Fq '=== RUN   TestUnitInventoryForCoverageArgumentLimit2999' "$tmp/unit.log"
! grep -Eq '^=== RUN   Test(Integration|E2E)' "$tmp/unit.log"
grep -Fxq '=== RUN   TestIntegrationOnly' "$tmp/integration.log"
grep -Fxq '=== RUN   TestIntegrationE2EBoth' "$tmp/integration.log"
! grep -Eq '^=== RUN   Test(Unit|E2EOnly)' "$tmp/integration.log"
grep -Fxq '=== RUN   TestE2EOnly' "$tmp/e2e.log"
grep -Fxq '=== RUN   TestIntegrationE2EBoth' "$tmp/e2e.log"
grep -Fxq '=== RUN   TestPublicContract' "$tmp/e2e.log"
! grep -Eq '^=== RUN   Test(Unit|IntegrationOnly)' "$tmp/e2e.log"
echo 'Go shape selection preserves classification with large inventories'
