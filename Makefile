# Version from the nearest monorelease@* tag: 0.1.0, or 0.1.0-3-g1a2b3c4 between releases. Empty without tags ("dev").
VERSION ?= $(shell git describe --tags --match 'monorelease@[0-9]*' --dirty 2>/dev/null | sed 's/^monorelease@//')
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test release dist clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o monorelease ./cmd/monorelease

test:
	go vet ./...
	go test ./...

# tag the next version, update CHANGELOG.md, push and create the GitHub release
release:
	scripts/release.sh

# archives for every platform, checksums, release notes and the Docker image of the latest release into dist/
dist:
	scripts/build-release.sh

clean:
	rm -rf monorelease dist
