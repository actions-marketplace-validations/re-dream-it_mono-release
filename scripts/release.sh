#!/usr/bin/env bash
# Releases mono-release with mono-release: works out the next version from the commits, tags it,
# regenerates CHANGELOG.md, pushes, and creates the GitHub release.
#
#   scripts/release.sh          show the plan and ask before releasing
#   scripts/release.sh --yes    do not ask
#
# A GitHub token is read from GITHUB_TOKEN or from `gh auth token`; without one the GitHub release is skipped.
set -euo pipefail
cd "$(dirname "$0")/.."

ASSUME_YES=false
[[ "${1:-}" == "--yes" ]] && ASSUME_YES=true

branch=$(git rev-parse --abbrev-ref HEAD)
[[ "$branch" == "main" ]] || { echo "release from main, not from '$branch'" >&2; exit 1; }
[[ -z "$(git status --porcelain)" ]] || { echo "the working tree is not clean: commit or stash first" >&2; exit 1; }

echo "== tests"
go vet ./...
go test ./...

echo "== build"
make build >/dev/null
BIN=./monorelease

echo "== plan"
$BIN plan
if [[ "$($BIN plan --json | grep -c '"component"')" == "0" ]]; then
  echo "nothing to release"
  exit 0
fi
if ! $ASSUME_YES; then
  read -r -p "Release? [y/N] " answer
  [[ "$answer" == "y" || "$answer" == "Y" ]] || { echo "aborted"; exit 1; }
fi

# the commits must be on the remote before a tag points at them
git push origin HEAD

flags=(--push)
export GITHUB_TOKEN="${GITHUB_TOKEN:-$(gh auth token 2>/dev/null || true)}"
if [[ -z "$GITHUB_TOKEN" ]]; then
  echo "warning: no GitHub token (GITHUB_TOKEN or 'gh auth login'): the GitHub release will not be created" >&2
  flags+=(--no-releases)
fi

echo "== release"
$BIN release "${flags[@]}"

# CHANGELOG.md was regenerated from the tags; commit it. [skip release]: it must not cause another release.
if ! git diff --quiet -- CHANGELOG.md 2>/dev/null || [[ -n "$(git ls-files --others --exclude-standard -- CHANGELOG.md)" ]]; then
  git add CHANGELOG.md
  git commit -q -m "docs: update changelog [skip release]"
  git push origin HEAD
  echo "== changelog committed"
fi
