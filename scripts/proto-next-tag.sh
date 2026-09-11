#!/usr/bin/env bash
set -euo pipefail

# Decides which proto module tag a main commit should carry. Prints the tag
# when the commit changes proto/ since the newest proto/vX.Y.Z tag, and prints
# nothing when there is nothing to publish. The Proto tag workflow creates and
# pushes the tag; this script only decides, so it can be tested without a
# remote.
#
# Versions only move forward. A commit the newest tag already contains - a CI
# run for an older commit finishing late - is skipped rather than tagged: a
# higher version must never carry less of the contract than a lower one.

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <commit>" >&2
  exit 2
fi

if ! commit=$(git rev-parse --verify --quiet "$1^{commit}"); then
  echo "proto-next-tag: unknown commit: $1" >&2
  exit 1
fi

latest=$(git tag --list 'proto/v*' --sort=-version:refname |
  grep -E '^proto/v[0-9]+\.[0-9]+\.[0-9]+$' | head -n 1 || true)
if [[ -z $latest ]]; then
  echo "proto-next-tag: no proto/vX.Y.Z tag to continue from" >&2
  exit 1
fi
latest_commit=$(git rev-parse "$latest^{commit}")

if ! git merge-base --is-ancestor "$latest_commit" "$commit"; then
  if git merge-base --is-ancestor "$commit" "$latest_commit"; then
    echo "proto-next-tag: $latest already contains ${commit:0:12}" >&2
    exit 0
  fi
  echo "proto-next-tag: ${commit:0:12} does not descend from $latest" >&2
  exit 1
fi

if git diff --quiet "$latest_commit" "$commit" -- ':(top)proto'; then
  echo "proto-next-tag: proto/ unchanged since $latest" >&2
  exit 0
fi

IFS=. read -r major minor patch <<<"${latest#proto/v}"
printf 'proto/v%s.%s.%s\n' "$major" "$minor" "$((patch + 1))"
