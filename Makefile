# Version is derived from the git tag — `git describe` gives v0.5.0 at the
# tag and v0.5.0-3-gabc1234 three commits later. There is no version
# constant to hand-edit; `git tag vX.Y.Z` is the single source of truth.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: install build test version

# Install to $GOBIN/$GOPATH/bin with the version baked in.
install:
	go install -ldflags "$(LDFLAGS)" .

# Build a local ./plancheck binary with the version baked in.
build:
	go build -ldflags "$(LDFLAGS)" -o plancheck .

test:
	go test ./...

# Print the version that a build would stamp.
version:
	@echo $(VERSION)
