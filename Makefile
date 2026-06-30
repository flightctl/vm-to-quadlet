BIN   := vm-to-quadlet
CMD   := ./cmd/vm-to-quadlet
IMAGE ?= quay.io/kubevirt/kubevirt-vm-to-quadlet
TAG   ?= latest

.PHONY: build test lint image push clean

build:
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o $(BIN) $(CMD)

test:
	go test -v -count=1 ./...

lint:
	go vet ./...

image:
	podman build -f Containerfile -t $(IMAGE):$(TAG) .

push:
	podman push $(IMAGE):$(TAG)

clean:
	rm -f $(BIN)
