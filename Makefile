BIN   := bin/vm-to-quadlet
CMD   := ./cmd/vm-to-quadlet
IMAGE ?= quay.io/kubevirt/kubevirt-vm-to-quadlet
TAG   ?= latest

SOURCE_GIT_TAG     := $(shell git describe --tags --always 2>/dev/null || echo "unknown")
SOURCE_GIT_COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
SOURCE_GIT_TREE_STATE := $(shell if git diff --quiet 2>/dev/null; then echo "clean"; else echo "dirty"; fi)
BIN_TIMESTAMP      := $(shell date +%Y%m%d 2>/dev/null || echo "unknown")

VERSION_PKG := github.com/flightctl/vm-to-quadlet/pkg/version
LDFLAGS := -s -w \
	-X $(VERSION_PKG).version=$(SOURCE_GIT_TAG) \
	-X $(VERSION_PKG).commit=$(SOURCE_GIT_COMMIT) \
	-X $(VERSION_PKG).buildDate=$(BIN_TIMESTAMP) \
	-X $(VERSION_PKG).gitTreeState=$(SOURCE_GIT_TREE_STATE)

.PHONY: build test lint image push clean

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) $(CMD)

test:
	go test -v -count=1 ./...

lint:
	go vet ./...

image:
	podman build -f Containerfile -t $(IMAGE):$(TAG) .

push:
	podman push $(IMAGE):$(TAG)

clean:
	rm -rf bin/
