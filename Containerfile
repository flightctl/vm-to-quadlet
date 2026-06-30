FROM registry.access.redhat.com/ubi9/go-toolset:1.25.9-1778675823 AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -buildvcs=false \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/vm-to-quadlet \
    ./cmd/vm-to-quadlet

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

COPY --from=builder /out/vm-to-quadlet /usr/local/bin/vm-to-quadlet

ENTRYPOINT ["/usr/local/bin/vm-to-quadlet"]
