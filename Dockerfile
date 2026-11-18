# monorelease on Alpine: a small image with the binary and git.
#
#   docker build -t monorelease .
#   docker run --rm -v "$PWD":/repo -w /repo monorelease plan
#
# In CI systems that run the job script inside the image (GitLab CI), clear the entrypoint:
#   image: { name: monorelease, entrypoint: [""] }
#
# scripts/build-release.sh builds this image for a release (multi-architecture with --push).

# The Go compiler always runs natively and cross-compiles for the target platform, so building
# an arm64 image on an amd64 machine (or the reverse) does not compile under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# set by BuildKit
ARG TARGETOS
ARG TARGETARCH
# docker build --build-arg VERSION=1.2.3 ... puts the version into "monorelease --version"
ARG VERSION=
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/monorelease ./cmd/monorelease

FROM alpine:3.20
ARG VERSION=
LABEL org.opencontainers.image.title="monorelease" \
      org.opencontainers.image.description="Versioning and release tagging for monorepos" \
      org.opencontainers.image.source="https://github.com/re-dream-it/mono-release" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"
# git 2.31+ is required; ca-certificates for pushing over https and for the release APIs
RUN apk add --no-cache git ca-certificates \
 && git config --system --add safe.directory '*'
COPY --from=build /out/monorelease /usr/local/bin/monorelease
COPY docker/action-entrypoint.sh /usr/local/bin/action-entrypoint
ENTRYPOINT ["monorelease"]
