#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

# The generated sources are committed because proto/ is a module of its own: a
# module tag carries only what is in git, and a client that go-gets it has no
# buf to regenerate with. Leaving them ignored would publish a tag whose
# connect stubs reference message types that are not there.
generated_files=(
  proto/agentcompose/v2/agentcompose.pb.go
  proto/health/v1/health.pb.go
)

for generated_file in "${generated_files[@]}"; do
  if [[ ! -s "$generated_file" ]]; then
    echo "generated protobuf source is missing: $generated_file" >&2
    exit 1
  fi
  if git check-ignore --quiet "$generated_file"; then
    echo "generated protobuf source must not be ignored: $generated_file" >&2
    exit 1
  fi
  if ! git ls-files --error-unmatch "$generated_file" >/dev/null 2>&1; then
    echo "generated protobuf source must be tracked: $generated_file" >&2
    exit 1
  fi
done

# Regenerating must change nothing. git status rather than git diff: a new
# .proto brings a new generated package, and forgetting to add it leaves an
# untracked file that git diff cannot see.
if command -v buf >/dev/null 2>&1; then
  buf generate
  drift="$(git status --porcelain -- proto)"
  if [[ -n "$drift" ]]; then
    echo "$drift" >&2
    echo "proto/ is out of date - run 'buf generate' and commit the result" >&2
    exit 1
  fi
fi

# The module has to build from what git carries, with no generation step,
# which is the only thing a go-get client can do.
if ! git ls-files --error-unmatch proto/go.mod >/dev/null 2>&1; then
  echo "proto must be its own module: proto/go.mod is not tracked" >&2
  exit 1
fi

# The images still regenerate before building. That is now redundant with the
# committed sources rather than load-bearing, but while the step is there it
# has to keep using the pinned buf.
for dockerfile in Dockerfile Dockerfile.agent-compose-local; do
  if ! grep -Eq '^ARG BUF_VERSION=v1\.68\.1$' "$dockerfile" ||
    ! grep -Fq 'GOBIN=/usr/local/bin go install github.com/bufbuild/buf/cmd/buf@${BUF_VERSION}' "$dockerfile"; then
    echo "Dockerfile must install the pinned configurable buf version into PATH: $dockerfile" >&2
    exit 1
  fi
  if ! grep -Fq 'RUN buf generate' "$dockerfile"; then
    echo "Dockerfile must generate protobuf sources: $dockerfile" >&2
    exit 1
  fi
  # The replace in go.mod cannot resolve without the proto module files, and
  # they have to land before the download layer, not with the sources.
  if ! grep -Fq 'COPY proto/go.mod proto/go.sum ./proto/' "$dockerfile"; then
    echo "Dockerfile must stage the proto module files before downloading: $dockerfile" >&2
    exit 1
  fi
done

echo "generated protobuf contract passed"
