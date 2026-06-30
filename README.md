# kubevirt-vm-to-quadlet

Converts a [KubeVirt](https://kubevirt.io) `VirtualMachine` YAML into native
[Quadlet](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html)
unit files for running VMs under Podman + systemd — no Kubernetes cluster required.

## How it works

```
VirtualMachine YAML
      │  step 3: pkg/standalone.PrepareForRendering
      ▼
 pre-render fixups (standalone tweaks, PVC stubs)
      │  step 4: pkg/kubevirt.NewTemplateService → RenderLaunchManifest
      ▼
 *k8sv1.Pod  (KubeVirt-generated Pod spec)
      │  step 5: pkg/standalone.AdaptForStandalone
      ▼
 post-render fixups (init containers, emptyDir→tmpfs, proxies)
      │  step 6: pkg/quadlet.Convert  (vendored podman kube quadlet)
      ▼
 raw Quadlet unit files
      │  step 7: pkg/standalone.ApplyPostConvertFixups
      ▼
 final .container / .volume / .pod unit files
```

See [docs/pipeline.md](docs/pipeline.md) for a full description of every
decision made in steps 3, 5, and 7.

The VM→Pod conversion logic is adapted from
[kubevirt-vm-to-pod](https://github.com/vladikr/kubevirt-vm-to-pod) and calls
KubeVirt's own `RenderLaunchManifest` to produce the same Pod spec a cluster
would generate. The Quadlet conversion uses an in-process vendored fork of
`podman kube quadlet` extended with KubeVirt-specific volume and network handling.

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
| `--passt-workarounds` | false | Patch `passt.avx2` at pod startup to fix the mrg_rxbuf crash with 2+ vCPU guests (needed for virt-launcher images predating passt `0^20260611.ga9c61ff`) |

## Known limitations

| Feature | Status |
|---|---|
| `spec.domain.devices.filesystems` (virtiofs) | Not supported. `RenderLaunchManifest` generates the domain XML but no `virtiofsd` sidecar is started; the guest will fail to mount the filesystem. |
| USB device passthrough | Not supported. The required `/dev/bus/usb/...` host device nodes are not mounted; QEMU will fail to start if USB passthrough is configured. |
| `configMap` / `secret` volumes | Not supported. The converter cannot read their contents without the Kubernetes API. Use `cloudInitNoCloud` with inline data or a `hostDisk` volume instead. |
| `serviceAccount` volumes | Not supported. Service account tokens are issued by the Kubernetes token controller only. |
| Multus networks | Rewritten to passt. Multus CNI plugins are not available in plain Podman. |
| SR-IOV interfaces | Rewritten to passt (see above). |

## Windows guests and passt networking

When using `--passt-workarounds` with a Windows guest, Windows may show
**"Unidentified network"** even after the VirtIO drivers are installed. This is
a known ARP issue with passt: Windows cannot resolve the gateway MAC address,
so it marks the interface as unidentified and blocks outbound traffic.

**Fix (one-time, run after every fresh Windows install):**

1. In the VM, press **Ctrl+F10** to open a command prompt, or open **PowerShell**
   as Administrator.
2. Run:
   ```powershell
   New-NetNeighbor -InterfaceAlias "Ethernet" -IPAddress "10.88.0.1" -LinkLayerAddress "9a-55-9a-55-9a-55" -State Permanent
   ```
   This adds a permanent ARP entry for the Podman bridge gateway (`10.88.0.1`)
   with passt's well-known MAC address. Windows immediately recognises the
   network and marks the interface as connected.

The entry persists across reboots — you only need to run this once per
installation, not on every VM start.

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
