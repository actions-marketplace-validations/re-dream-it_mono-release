#!/usr/bin/env bash
# Releases mono-release with mono-release: works out the next version from the commits, tags it,
# regenerates CHANGELOG.md, pushes, and creates the GitHub release.
#
#   scripts/release.sh                      show the plan and ask before releasing
#   scripts/release.sh --yes                do not ask
#   scripts/release.sh --no-artifacts       tag and push only: do not build and upload the binaries
#   scripts/release.sh --image NAME         push the Docker image to NAME (default: ghcr.io/<owner>/monorelease)
#   scripts/release.sh --no-image           do not build and push the Docker image
#
# After the tag is pushed it builds the release artifacts (scripts/build-release.sh), uploads them to the
# GitHub release, and pushes a multi-architecture Docker image to ghcr.io (this needs the write:packages scope
# once: gh auth refresh -h github.com -s write:packages; without it the image is skipped with a warning). A GitHub token is read from GITHUB_TOKEN or from `gh auth token`; without one the GitHub
# release page is skipped, and so is the upload.
set -euo pipefail
cd "$(dirname "$0")/.."

ASSUME_YES=false
ARTIFACTS=true
PUSH_IMAGE=true
IMAGE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --yes)          ASSUME_YES=true; shift ;;
    --no-artifacts) ARTIFACTS=false; shift ;;
    --image)        IMAGE="${2:?--image needs an image name}"; shift 2 ;;
    --no-image)     PUSH_IMAGE=false; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

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

$ARTIFACTS || exit 0

# the binaries and the image of the version that was just tagged, attached to its GitHub release
build_flags=()
have_gh=false
if command -v gh >/dev/null && gh auth status >/dev/null 2>&1; then
  have_gh=true
  build_flags+=(--upload)
else
  echo "warning: gh is missing or not logged in: the artifacts are built but not uploaded" >&2
fi

image_flags=(--no-docker)
if $PUSH_IMAGE; then
  if [[ -z "$IMAGE" ]] && $have_gh; then
    IMAGE="ghcr.io/$(gh repo view --json owner --jq .owner.login | tr '[:upper:]' '[:lower:]')/monorelease"
  fi
  if [[ -z "$IMAGE" ]]; then
    echo "warning: no image name (pass --image NAME): the Docker image is not pushed" >&2
  elif [[ "$IMAGE" == ghcr.io/* ]] && ! grep -q '"ghcr.io"' "${DOCKER_CONFIG:-$HOME/.docker}/config.json" 2>/dev/null \
       && ! env -u GITHUB_TOKEN gh auth status 2>&1 | grep -q 'write:packages'; then
    echo "warning: the gh token cannot push to ghcr.io, so the Docker image is skipped. Add the scope once, then" >&2
    echo "    gh auth refresh -h github.com -s write:packages" >&2
    echo "    scripts/build-release.sh --push $IMAGE" >&2
  else
    image_flags=(--push "$IMAGE")
  fi
fi
echo "== artifacts"
scripts/build-release.sh "${build_flags[@]}" "${image_flags[@]}"
