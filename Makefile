BIN       := vm-to-quadlet
CMD       := ./cmd/vm-to-quadlet
IMAGE     ?= quay.io/kubevirt/kubevirt-vm-to-quadlet
TAG       ?= latest
# Tests require the custom podman fork that includes "kube quadlet" with emptyDir
# and other fixes. Override to use a different binary.
PODMAN_BIN ?= $(HOME)/dev/podman/bin/podman

.PHONY: build test lint image push clean

build:
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o $(BIN) $(CMD)

test:
	PODMAN_BIN=$(PODMAN_BIN) go test -v -count=1 ./...

lint:
	go vet ./...

image:
	$(PODMAN_BIN) build \
		-f Containerfile \
		-t $(IMAGE):$(TAG) \
		.

push:
	$(PODMAN_BIN) push $(IMAGE):$(TAG)

clean:
	rm -f $(BIN)
