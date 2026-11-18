#!/usr/bin/env bash
# Builds the release artifacts of monorelease into dist/:
#
#   monorelease_<version>_<os>_<arch>.tar.gz   linux, darwin (amd64, arm64)
#   monorelease_<version>_windows_<arch>.zip   windows (amd64, arm64)
#   checksums.txt                              sha256 of every archive
#   RELEASE_NOTES.md                           the notes of the release, as text for the GitHub release page
#
# and the Docker image (monorelease:<version>, plus :latest for a stable version).
#
# The sources come from the release tag (monorelease@<version>), not from the working tree, so the
# artifacts are exactly what was released. scripts/release.sh runs it for you; to run it yourself after
# a release, add --upload to attach the files to the GitHub release (it is created if it does not exist).
#
#   scripts/build-release.sh                       latest release tag
#   scripts/build-release.sh --version 0.2.0       a specific release
#   scripts/build-release.sh --snapshot            the current working tree, version "snapshot" (to try the build)
#   scripts/build-release.sh --upload              also upload the files to the GitHub release (needs `gh auth login`)
#   scripts/build-release.sh --no-docker           archives only
#   scripts/build-release.sh --push ghcr.io/re-dream-it/monorelease
#                                                  push a multi-architecture image (needs `docker login`)
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(pwd)

VERSION=""
SNAPSHOT=false
DOCKER=true
UPLOAD=false
PUSH_IMAGE=""
PLATFORMS="linux/amd64,linux/arm64"
OUT="dist"
TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)   VERSION="${2:?--version needs a value}"; shift 2 ;;
    --snapshot)  SNAPSHOT=true; shift ;;
    --no-docker) DOCKER=false; shift ;;
    --upload)    UPLOAD=true; shift ;;
    --push)      PUSH_IMAGE="${2:?--push needs an image name, e.g. ghcr.io/re-dream-it/monorelease}"; shift 2 ;;
    --platforms) PLATFORMS="${2:?--platforms needs a value}"; shift 2 ;;
    -h|--help)   usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

command -v go >/dev/null || { echo "go not found" >&2; exit 1; }
if $UPLOAD; then
  # fail before the long build, not after it
  $SNAPSHOT && { echo "--upload needs a release, not --snapshot" >&2; exit 1; }
  command -v gh >/dev/null || { echo "gh not found: install the GitHub CLI (brew install gh) and run 'gh auth login'" >&2; exit 1; }
  gh auth status >/dev/null 2>&1 || { echo "gh is not logged in: run 'gh auth login' (or set GITHUB_TOKEN)" >&2; exit 1; }
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
SRC="$TMP/src"
mkdir -p "$SRC"

# --- sources
if $SNAPSHOT; then
  VERSION="${VERSION:-snapshot}"
  echo "== sources: the working tree (snapshot)"
  # tracked and untracked files that are not ignored (so no .git, dist/ or DESIGN.md), as they are on disk
  git ls-files -z --cached --others --exclude-standard | while IFS= read -r -d '' f; do
    [[ -e "$f" ]] && printf '%s\0' "$f"
  done | tar -C "$ROOT" --null -T - -cf - | tar -C "$SRC" -xf -
else
  if [[ -z "$VERSION" ]]; then
    VERSION=$(git tag --list 'monorelease@[0-9]*' --sort=-v:refname | head -n1 | sed 's/^monorelease@//')
  fi
  [[ -n "$VERSION" ]] || { echo "no monorelease@* tag found: release first (scripts/release.sh), or use --snapshot" >&2; exit 1; }
  TAG="monorelease@$VERSION"
  git rev-parse -q --verify "refs/tags/$TAG" >/dev/null || { echo "tag $TAG does not exist" >&2; exit 1; }
  echo "== sources: tag $TAG ($(git rev-parse --short "$TAG^{commit}"))"
  git archive "$TAG" | tar -C "$SRC" -xf -
fi
LDFLAGS="-s -w -X main.version=${VERSION}"

# --- archives
rm -rf "${ROOT:?}/$OUT"
mkdir -p "$ROOT/$OUT"
echo "== binaries ($VERSION)"
for target in $TARGETS; do
  os=${target%/*}
  arch=${target#*/}
  name="monorelease_${VERSION}_${os}_${arch}"
  stage="$TMP/stage/$name"
  mkdir -p "$stage"
  bin=monorelease
  [[ "$os" == "windows" ]] && bin=monorelease.exe
  (cd "$SRC" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "$LDFLAGS" -o "$stage/$bin" ./cmd/monorelease)
  for extra in LICENSE README.md CHANGELOG.md; do
    [[ -f "$SRC/$extra" ]] && cp "$SRC/$extra" "$stage/"
  done
  if [[ "$os" == "windows" ]]; then
    command -v zip >/dev/null || { echo "zip not found (needed for the windows archives)" >&2; exit 1; }
    (cd "$TMP/stage" && zip -qr "$ROOT/$OUT/$name.zip" "$name")
    echo "   $OUT/$name.zip"
  else
    tar -C "$TMP/stage" -czf "$ROOT/$OUT/$name.tar.gz" "$name"
    echo "   $OUT/$name.tar.gz"
  fi
done

# --- checksums
(
  cd "$ROOT/$OUT"
  if command -v sha256sum >/dev/null; then sha256sum ./*.tar.gz ./*.zip; else shasum -a 256 ./*.tar.gz ./*.zip; fi \
    | sed 's# \./# #' > checksums.txt
)
echo "   $OUT/checksums.txt"

# --- release notes, taken from the tag with a binary built for this machine
if ! $SNAPSHOT; then
  host_bin="$TMP/monorelease-host"
  (cd "$SRC" && CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$host_bin" ./cmd/monorelease)
  if "$host_bin" notes monorelease "$VERSION" > "$ROOT/$OUT/RELEASE_NOTES.md" 2>"$TMP/notes.err"; then
    echo "   $OUT/RELEASE_NOTES.md"
  else
    echo "warning: could not generate the release notes: $(cat "$TMP/notes.err")" >&2
    rm -f "$ROOT/$OUT/RELEASE_NOTES.md"
  fi
fi

# --- Docker image
if $DOCKER; then
  command -v docker >/dev/null || { echo "docker not found: install it or use --no-docker" >&2; exit 1; }
  image="${PUSH_IMAGE:-monorelease}"
  tags=(-t "$image:$VERSION")
  # "latest" only follows stable releases
  if ! $SNAPSHOT && [[ "$VERSION" != *-* ]]; then tags+=(-t "$image:latest"); fi
  echo "== docker image ${tags[*]}"
  if [[ -n "$PUSH_IMAGE" ]]; then
    docker buildx build --platform "$PLATFORMS" --build-arg "VERSION=$VERSION" "${tags[@]}" --push "$SRC"
    echo "   pushed $image for $PLATFORMS"
  else
    docker build -q --build-arg "VERSION=$VERSION" "${tags[@]}" "$SRC" >/dev/null
    echo "   built for this machine's platform only; use --push <image> for a multi-architecture image"
    echo "   $(docker run --rm "$image:$VERSION" --version)"
  fi
fi

# --- GitHub release: create it when missing, then attach the files
if $UPLOAD; then
  echo "== GitHub release $TAG"
  files=("$ROOT/$OUT"/*.tar.gz "$ROOT/$OUT"/*.zip "$ROOT/$OUT/checksums.txt")
  if gh release view "$TAG" >/dev/null 2>&1; then
    gh release upload "$TAG" "${files[@]}" --clobber
    echo "   uploaded ${#files[@]} files to the existing release"
  else
    create=(--title "monorelease $VERSION" --verify-tag)
    [[ "$VERSION" == *-* ]] && create+=(--prerelease)
    if [[ -f "$ROOT/$OUT/RELEASE_NOTES.md" ]]; then create+=(--notes-file "$ROOT/$OUT/RELEASE_NOTES.md"); else create+=(--notes ""); fi
    gh release create "$TAG" "${create[@]}" "${files[@]}"
    echo "   created the release with ${#files[@]} files"
  fi
fi

echo
echo "== done: $OUT/"
ls -1 "$ROOT/$OUT" | sed 's/^/   /'
if ! $SNAPSHOT && ! $UPLOAD; then
  echo
  echo "Upload to the GitHub release of $TAG: the files in $OUT/ (the notes are in RELEASE_NOTES.md), or rerun with --upload."
fi
