# kubevirt-vm-to-quadlet

Converts a [KubeVirt](https://kubevirt.io) `VirtualMachine` YAML into native
[Quadlet](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html)
unit files for running VMs under Podman + systemd — no Kubernetes cluster required.

## How it works

```
VirtualMachine YAML
      │
      ▼
 pkg/transformer          (github.com/vladikr/kubevirt-vm-to-pod)
 VM → *k8sv1.Pod
      │
      ▼
 pkg/quadlet              (this repo)
 Pod → Quadlet unit files
```

The transformer layer (shared with
[kubevirt-vm-to-pod](https://github.com/vladikr/kubevirt-vm-to-pod)) calls
KubeVirt's own `RenderLaunchManifest` to produce the same Pod spec a cluster
would generate. The quadlet layer converts that Pod spec into `.container`,
`.volume`, and `.pod` systemd unit files using
`podman kube quadlet --format json`.

## Requirements

- Podman ≥ 5.x with `kube quadlet` support
- systemd (user or system session)
- KVM-capable host for actual VM execution

## Installation

### From source

```bash
# From the workspace root (~/dev/) containing go.work:
go build -buildvcs=false -o kubevirt-vm-to-quadlet ./vm-to-quadlet/cmd/vmToQuadlet
```

Or using the Makefile:

```bash
cd vm-to-quadlet
make build
```

### Container image

```bash
# Build (from ~/dev/ — the go.work root):
make image IMAGE=quay.io/<org>/kubevirt-vm-to-quadlet TAG=latest

# Push:
make push IMAGE=quay.io/<org>/kubevirt-vm-to-quadlet TAG=latest
```

## Usage

```bash
# From a file:
kubevirt-vm-to-quadlet --vm-file my-vm.yaml --output-dir ~/.config/containers/systemd/my-vm

# From stdin:
cat my-vm.yaml | kubevirt-vm-to-quadlet --output-dir ~/.config/containers/systemd/my-vm

# With VNC and serial console proxies:
kubevirt-vm-to-quadlet \
  --vm-file my-vm.yaml \
  --vnc-proxy \
  --console-proxy \
  --output-dir ~/.config/containers/systemd/my-vm
```

Then start the VM:

```bash
systemctl --user daemon-reload
systemctl --user start virt-launcher-<vmname>-pod.service
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--vm-file` | stdin | Path to VirtualMachine YAML |
| `--output-dir` | stdout | Directory to write Quadlet unit files |
| `--launcher-image` | `quay.io/kubevirt/virt-launcher:v1.8.4` | virt-launcher image (see [passt version notes](docs/reference.md#appendix--virt-launcher-image-and-passt-version)) |
| `--podman-bin` | `podman` | Path to the podman binary |
| `--vnc-proxy` | false | Inject a socat sidecar forwarding the VNC socket to `--vnc-port` |
| `--vnc-port` | `5900` | TCP port for the VNC proxy |
| `--vnc-image` | `docker.io/alpine/socat:latest` | Image for the VNC proxy sidecar |
| `--console-proxy` | false | Inject a socat sidecar forwarding the serial console socket to `--console-port` |
| `--console-port` | `2222` | TCP port for the serial console proxy |
| `--console-image` | `docker.io/alpine/socat:latest` | Image for the serial console proxy sidecar |
| `--passt-workarounds` | false | Inject libvirt hook with mrg_rxbuf and portForward patches (see [passt version notes](docs/reference.md#appendix--virt-launcher-image-and-passt-version)) |

## Publishing ports and drop-ins

Ports are not published by default. Add a systemd drop-in after generation:

```bash
mkdir -p ~/.config/containers/systemd/my-vm/virt-launcher-<vmname>-pod.d
cat > ~/.config/containers/systemd/my-vm/virt-launcher-<vmname>-pod.d/ports.conf << EOF
[Pod]
PublishPort=3389:3389
PublishPort=5900:5900
EOF
systemctl --user daemon-reload
```

## Development

```bash
# Run tests (requires podman in PATH):
make test

# Override podman binary:
make test PODMAN_BIN=/path/to/custom/podman

# Lint:
make lint
```

## Module structure

This repo is one half of a Go workspace:

```
~/dev/
├── go.work
├── kubevirt-vm-to-pod/   # transformer (shared preprocess layer)
└── vm-to-quadlet/        # this repo
```

Once `kubevirt-vm-to-pod` is published as a Go module, the workspace
dependency can be replaced with a standard `require` directive and the
container image can be built without copying both repos into the build context.
