#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mod_dir="$(mktemp -d)"
trap 'rm -rf "$mod_dir"' EXIT
cp "$root/sdk/go/go.mod" "$mod_dir/go.mod"
cp "$root/sdk/go/go.sum" "$mod_dir/go.sum"
"$root/scripts/with-go-toolchain.sh" go -C "$root/sdk/go" mod edit -modfile="$mod_dir/go.mod" -replace="github.com/chaitin/agent-compose/proto=$root/proto"
if [[ "${1:-test}" == "lint" ]]; then
  cd "$root/sdk/go"
  GOFLAGS="-mod=mod -modfile=$mod_dir/go.mod" "$root/scripts/with-go-toolchain.sh" golangci-lint run -c "$root/.golangci.yml" --allow-parallel-runners ./...
else
  "$root/scripts/with-go-toolchain.sh" go -C "$root/sdk/go" test -mod=mod -modfile="$mod_dir/go.mod" -race ./...
fi
