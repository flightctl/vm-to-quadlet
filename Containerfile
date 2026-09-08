FROM registry.access.redhat.com/ubi9/go-toolset:1.25.9-1778675823 AS builder

USER root
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY .git .git

RUN SOURCE_GIT_TAG=$(git describe --tags --always 2>/dev/null || echo "unknown") && \
    SOURCE_GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown") && \
    SOURCE_GIT_TREE_STATE=$(if git diff --quiet 2>/dev/null; then echo "clean"; else echo "dirty"; fi) && \
    BIN_TIMESTAMP=$(date +%Y%m%d) && \
    VERSION_PKG="github.com/flightctl/vm-to-quadlet/pkg/version" && \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w \
            -X ${VERSION_PKG}.version=${SOURCE_GIT_TAG} \
            -X ${VERSION_PKG}.commit=${SOURCE_GIT_COMMIT} \
            -X ${VERSION_PKG}.buildDate=${BIN_TIMESTAMP} \
            -X ${VERSION_PKG}.gitTreeState=${SOURCE_GIT_TREE_STATE}" \
        -o /out/vm-to-quadlet \
        ./cmd/vm-to-quadlet

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

COPY --from=builder /out/vm-to-quadlet /usr/local/bin/vm-to-quadlet

ENTRYPOINT ["/usr/local/bin/vm-to-quadlet"]
