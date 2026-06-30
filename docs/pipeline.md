# Conversion pipeline — decision reference

Every decision made in the three "ours" stages of the pipeline. Steps 4
(`RenderLaunchManifest`) and 6 (`quadlet.Convert`) are thin wrappers around
external APIs and are not documented here.

```
Step 3  PrepareForRendering    pkg/standalone/pre_render.go
Step 4  RenderLaunchManifest   pkg/kubevirt/kubevirt.go          ← KubeVirt, not documented here
Step 5  AdaptForStandalone     pkg/standalone/post_render.go
Step 6  quadlet.Convert        pkg/quadlet/converter.go          ← vendored Podman, not documented here
Step 7  ApplyPostConvertFixups pkg/standalone/post_convert.go
```

---

## Step 3 — PrepareForRendering

Transforms a `VirtualMachine` into a `PreparedVM` (VMI + PVC cache) ready for
`RenderLaunchManifest`. No Pod spec exists yet.

### Validation

Checked before anything else. Hard errors abort the conversion:

| Condition | Why it is an error |
|---|---|
| `vol.ConfigMap != nil` | The converter has no access to the Kubernetes API at conversion time and therefore cannot read the ConfigMap's keys or data. The config content is simply unknown. Alternatives that do not require API resolution: `cloudInitNoCloud` with inline `userData`/`networkData`, or a `hostDisk` volume pointing to a pre-placed file on the host. |
| `vol.Secret != nil` | Same reason as ConfigMap: the secret's contents are not available without the API. Alternatives: `cloudInitNoCloud` with inline data, or a `hostDisk` volume. |
| `vol.ServiceAccount != nil` | Service account tokens are issued and rotated by the Kubernetes token controller; the token value is only known at runtime inside a cluster. There is no offline equivalent. |

Warnings printed to stderr (conversion continues):

| Condition | Why it is a warning, not an error |
|---|---|
| `vol.DataVolume != nil` and `vol.DataVolume.Name` not in `vm.Spec.DataVolumeTemplates` | In a cluster, CDI provisions the volume from a source. In standalone mode we treat it as a reference to a pre-existing named Podman volume and skip init-container provisioning. The VM will start successfully if the operator has already populated the volume with `disk.img`; it will fail at QEMU startup otherwise. We warn rather than error because the volume may legitimately have been pre-populated. |
| `net.Multus != nil` | Multus requires CNI plugins and a Multus daemonset, neither of which is present in a plain Podman environment. The interface is silently rewritten to passt, which is the only binding that works in rootless Podman. We warn rather than error because the resulting VM is functional — only the network attachment changes. |

### Default namespace

If `vm.ObjectMeta.Namespace == ""` it is set to `"default"`. KubeVirt's
`TemplateService` derives domain names and PVC lookup keys from the namespace;
an empty string produces malformed resource names.

### PVC cache stubs

`RenderLaunchManifest` queries the Kubernetes API for each
`PersistentVolumeClaim` referenced by the VM to determine its `VolumeMode`
(`Filesystem` or `Block`). It uses this to decide how to expose the disk to
QEMU: `Filesystem` mode produces a file path (`/path/to/disk.img`) while
`Block` mode produces a raw block device path. In standalone mode no API
exists, so we pre-populate a `cache.Indexer` with stubs that satisfy this
lookup.

A stub is created for every volume where:

- `vol.PersistentVolumeClaim != nil` → stub `ClaimName = vol.PersistentVolumeClaim.ClaimName`
- `vol.DataVolume != nil` → stub `ClaimName = vol.DataVolume.Name`

Other volume types (`emptyDir`, `hostDisk`, `cloudInitNoCloud`, etc.) are not
looked up by `RenderLaunchManifest`, so they need no stub.

Each stub carries:

- **`VolumeMode: Filesystem`** — Podman named volumes are directories on the
  host filesystem, not block devices. Claiming `Block` mode would cause
  KubeVirt to configure the disk as a raw block passthrough, which a Podman
  named volume cannot provide.
- **`AccessMode: ReadWriteOnce`** — the conventional single-node default;
  `RenderLaunchManifest` does not make scheduling decisions based on this
  field in the code paths we exercise, but it must be non-empty.

### Fake ClusterConfig

`RenderLaunchManifest` requires a `*virtconfig.ClusterConfig`. A minimal fake
is constructed using `testutils.NewFakeClusterConfigUsingKV` with two feature
gates explicitly enabled:

| Feature gate | Reason |
|---|---|
| `ImageVolume` | Required for `containerDisk`-backed volumes to pass KubeVirt validation |
| `HostDisk` | Required for `hostDisk` volumes (e.g. ISO files on the host) to pass KubeVirt validation |

The `Passt` feature gate is intentionally absent — it was removed in KubeVirt
v1.8 (always-on); including it would cause a validation error.

### KubeVirt defaults pipeline

The following KubeVirt-owned functions are called in order, replicating what
the admission webhook and virt-controller do in a live cluster. The exact
values each function sets are KubeVirt's internal logic — see the KubeVirt
source at `pkg/defaults/` and `pkg/virt-controller/watch/vm/` for details.

1. `defaults.SetVirtualMachineDefaults(vm, clusterConfig, nil)`
2. `vmCtrl.SetupVMIFromVM(vm)` — derives the `VirtualMachineInstance` from the `VirtualMachine` spec
3. `defaults.SetDefaultVirtualMachineInstance(clusterConfig, vmi)`
4. `mutators.ApplyNewVMIMutations(vmi, clusterConfig)` — runs admission mutators
5. `vmispec.SetDefaultNetworkInterface(clusterConfig, &vmi.Spec)` — adds a default `masquerade` interface if none is declared
6. `util.SetDefaultVolumeDisk(&vmi.Spec)` — assigns a disk bus/target to volumes that declare no disk
7. `vmCtrl.AutoAttachInputDevice(vmi)` — prepends a USB tablet to `domain.devices.inputs` if no input device is declared

### Force passt binding

Every interface in `vmi.Spec.Domain.Devices.Interfaces` is rewritten:

- `InterfaceBindingMethod` is replaced with `PasstBinding: &virtv1.InterfacePasstBinding{}`
- `Masquerade`, `Bridge`, `DeprecatedSlirp`, and `SRIOV` are set to `nil`

Every network that corresponds to an interface is rewritten to
`Pod: &virtv1.PodNetwork{}` with `Multus` set to `nil`. If no matching network
exists, the interface is renamed to `"default"` and a pod network named
`"default"` is added. If no pod network exists in the spec at all, one is
prepended before this loop runs.

Reason: passt is user-space networking that runs entirely inside the pod and
requires no host-level privileges, making it the only binding compatible with
rootless Podman. `Masquerade` depends on `virt-handler` setting up iptables NAT
rules on the host at runtime — `virt-handler` does not run in standalone mode
and rootless containers cannot manipulate host network rules regardless.
`Bridge` requires CNI plugins and privileged network operations that are equally
unavailable. These bindings are not degraded gracefully; they fail at QEMU
startup.

### VMI UID

Libvirt names the QEMU domain `<namespace>_<vmname>` (e.g. `default_windows-vm`)
and writes the QEMU PID file to `/run/libvirt/qemu/run/default_windows-vm.pid`.

`virt-launcher-monitor` constructs the PID file path independently as
`/run/libvirt/qemu/run/<vmi.UID>_<vmi.Name>.pid`. In a cluster `vmi.UID` is
the Kubernetes-assigned UUID, which matches what libvirt uses because
virt-launcher tells libvirt to name the domain `<uid>_<name>`.

In standalone mode `vmi.UID == ""` and there is no cluster to assign one. If
left empty the monitor looks for `_windows-vm.pid` (wrong path) and times out.
Setting `vmi.UID = vmi.Namespace` makes the monitor construct
`default_windows-vm.pid`, which is exactly the path libvirt writes — the two
sides agree on the same file.

### Interface status

`vmi.Status.Interfaces` is populated with one entry per interface in
`vmi.Spec.Domain.Devices.Interfaces`, each with
`PodInterfaceName = "eth<index>"` (index 0, 1, …). In a cluster this is done
at runtime by `virt-handler`; here it is done offline, after `forcePasstBinding`
has finalised the interface list, so the marshaled `STANDALONE_VMI` env var
carries correct interface information from first boot.

---

## Step 5 — AdaptForStandalone

Mutates the `*k8sv1.Pod` produced by `RenderLaunchManifest` so it can run
under rootless Podman. Changes are applied in the order listed below.

### TypeMeta

`Kind: Pod` and `APIVersion: v1` are set unconditionally. `RenderLaunchManifest`
returns a struct with empty TypeMeta; the vendored `podman kube quadlet`
conversion code requires both fields to be present to recognise the input as a
Pod object.

### generateName → name

KubeVirt sets `pod.GenerateName = "virt-launcher-<vmname>-"` and leaves
`pod.Name = ""`. Podman does not support `generateName`; the trailing `-` is
stripped and the result is used as `pod.Name`.

### Sidecar proxies (optional)

| Flag | Container name | socat command |
|---|---|---|
| `--console-proxy` | `console-proxy` | `TCP-LISTEN:<port>,fork,reuseaddr UNIX:/var/run/kubevirt-private/default/virt-serial0` |
| `--vnc-proxy` | `vnc-proxy` | `TCP-LISTEN:<port>,fork,reuseaddr UNIX:/var/run/kubevirt-private/default/virt-vnc` |

Both sidecars need access to the Unix sockets under `/var/run/kubevirt-private`.
The volume name is resolved by inspecting the compute container's existing
`VolumeMounts` and finding the entry whose `MountPath` is exactly
`/var/run/kubevirt-private`. If no such mount exists the sidecar is silently
skipped.

### Host device mounts

These mounts never appear in the `VirtualMachine` YAML — the VM spec describes
the guest (CPU, memory, disks, interfaces) and has no concept of host device
nodes. The mapping from guest requirements to host devices is KubeVirt
infrastructure. In a real cluster `virt-handler` sets up the node device
environment before the pod starts; `RenderLaunchManifest` (step 4) does not
include them. Since `virt-handler` does not run in standalone mode, step 5 is
the only place they are added:

| Volume name | Host path | Type |
|---|---|---|
| Volume name | Host path | Type | Why unconditional |
|---|---|---|---|
| `kvm` | `/dev/kvm` | `CharDev` | QEMU hardware acceleration; without it QEMU falls back to software emulation, which is unusably slow for a full OS |
| `tun` | `/dev/net/tun` | `CharDev` | Required by passt; step 3 forces passt on all interfaces so this is always needed |
| `vhost-net` | `/dev/vhost-net` | `CharDev` | virtio-net vhost offload, also required by passt |
| `cgroup` | `/sys/fs/cgroup` | `Directory` (read-only) | QEMU and virtqemud read cgroup v2 paths for memory locking and resource accounting regardless of workload |

Unlike the four unconditional devices above, GPUs and VFIO host devices are
declared in the VM YAML as abstract resource requests (e.g. `nvidia.com/gpu`).
In a Kubernetes cluster a device plugin resolves those resource names to actual
device file paths and injects them into the pod at scheduling time;
`RenderLaunchManifest` never adds them. In standalone mode no device plugin
runs, so step 5 must do that resolution itself.

GPU devices are added when `vmi.Spec.Domain.Devices.GPUs` is non-empty. The
vendor is inferred from the resource name string (case-insensitive) because
that is the only information available without a device plugin:

| String match in device name | Mounts added |
|---|---|
| `"nvidia"` | `/dev/nvidiaN` for each GPU; `/dev/nvidiactl`, `/dev/nvidia-uvm`, `/dev/nvidia-uvm-tools`, `/dev/nvidia-modeset` once (index 0) |
| `"amd"` or `"radeon"` | `/dev/dri/cardN` and `/dev/dri/renderD<128+N>` |
| `"intel"` | `/dev/dri/cardN` and `/dev/dri/renderD<128+N>` |
| anything else | `/dev/dri/cardN` and a stderr warning |

VFIO passthrough uses the same principle: in a cluster the device plugin assigns
specific IOMMU groups to the pod. In standalone mode, `/sys/bus/pci/drivers/vfio-pci` is scanned: each entry containing
`":"` (PCI address) has its `iommu_group` symlink resolved; the unique IOMMU
group numbers (sorted) become `/dev/vfio/<group>` mounts plus `/dev/vfio/vfio`.
A warning is emitted to stderr if `hostDevices` are requested but no
vfio-pci bound devices are found.

### DataVolume init containers

In a real cluster, CDI (Containerized Data Importer) handles DataVolume
provisioning: it runs an importer pod that pulls the source, converts the disk
to raw format, and writes it into the PVC before the VM pod starts.
`RenderLaunchManifest` simply assumes the PVC already has `disk.img` in it.

The `ImageVolume` and `HostDisk` feature gates in our fake ClusterConfig only
affect KubeVirt's *validation layer* — they allow certain volume types to pass
the admission check. They do not cause anything to provision the disk. CDI does
not run in standalone mode, so without these init containers a DataVolume-backed
disk would be missing and QEMU would fail to start.

For each volume in `vmi.Spec.Volumes` where `vol.DataVolume != nil`, a
`dataVolumeTemplate` with matching name must exist in
`vm.Spec.DataVolumeTemplates` and its storage size must be > 0. Volumes without
a matching template are skipped (they reference pre-existing named volumes).

The disk target path inside the container is always
`/var/run/kubevirt-private/vmi-disks/<volName>/disk.img`.

**Blank / plain storage** (`dvt.Spec.Source.Registry == nil`):
Container name `init-disk-<volSlug>`. Script logic:
- If `disk.img` does not exist: `qemu-img create -f raw disk.img <sizeBytes>`
- If `disk.img` exists and size < requested: `qemu-img resize -f raw disk.img <sizeBytes>`
- If `disk.img` exists and size > requested: warning to stderr, no action

**Registry source** (`dvt.Spec.Source.Registry.URL != nil`):
Container name `import-disk-<volSlug>`. An OCI `Image` volume is added for the
image reference (stripping `docker://` or `oci://` prefix, `PullIfNotPresent`),
mounted read-only at `/var/run/import-source/<volName>`. Script logic:
- If `disk.img` does not exist: find the first `*.img` or `*.qcow2` file inside
  the source mount (max depth 2), then `qemu-img convert -O raw <source> disk.img`
  followed by a resize if needed
- If `disk.img` exists: same grow/shrink logic as blank mode

All disk init containers run with `AllowPrivilegeEscalation: false` and
`Capabilities: drop ALL`.

### Standalone cleanup

Applied by `cleanupForStandalone`:

| Change | Reason |
|---|---|
| Init containers with `RestartPolicy=Always` are moved to `pod.Spec.Containers` (with `RestartPolicy` cleared) | Podman does not support sidecar-style init containers; they block the init chain indefinitely |
| Resource requests/limits whose name contains `"/"` are removed from all containers and init containers | Podman has no device plugin manager; slash-qualified resource names cause a validation error |
| Sysctl `net.ipv4.ip_unprivileged_port_start=0` added to `pod.Spec.SecurityContext.Sysctls` | Allows passt to forward privileged ports (< 1024) within the pod's isolated network namespace; scoped to the pod netns, no host impact |
| `pod.Spec.ReadinessGates` set to `nil` | References Kubernetes condition types that are never set in standalone mode; unresolved gates prevent the pod from transitioning to Ready |

### virt-handler directory init

An init container named `virt-handler-dir-init` is **prepended** (runs first).
It scans `pod.Spec.Volumes` for all `EmptyDir` volumes and looks up each one
in a static map of required subdirectories:

| Volume name suffix | Subdirectories created |
|---|---|
| `private` | `libvirt/qemu` |

The mount path inside the init container is `/emptydir/<volumeName>`.
The init container runs as UID `107` (qemu). The emptyDir volumes are
tmpfs-backed so no wipe is needed — the init container only creates the
subdirectory tree that virt-launcher expects before dropping to UID 107.

### passt binary patcher (optional — `--passt-workarounds`)

An init container named `passt-binary-patcher` is **appended** (runs last in
the init chain). A new `EmptyDir` volume named `passt-bin` is added to the pod.

The init container:
1. Mounts `passt-bin` at `/passt-bin/` (read-write)
2. Copies `/usr/bin/passt.avx2` to `/passt-bin/passt.avx2.patched`
3. Reads 6 bytes at offset `164189` (0x2815d) using `dd` + `od`; if they equal
   `0f 83 d2 00 00 00` (a `jae rel32` targeting the abort path), overwrites the
   4-byte operand at offset `164191` (0x2815f) with `\x58\x00\x00\x00`
   (little-endian 88 = redirect to `0x281bb`, the truncation epilogue)
4. If the bytes do not match, a warning is printed and the unpatched copy is kept
5. Writes `/passt-bin/passt` — a one-line wrapper: `exec /passt-bin/passt.avx2.patched "$@"`

The compute container mounts `passt-bin` read-only at `/passt-bin/`.

The init container runs as UID `0` (root) so it can write to the emptyDir
volume before the qemu user takes over.

The patch targets passt `0^20250512.g8ec1341` (shipped in
`quay.io/kubevirt/virt-launcher:v1.8.4`). The bug causes a crash when the
scattergather list overflows with 2+ vCPU guests.

### Persistent state volumes

This mirrors KubeVirt's `backend-storage` design: a **single PVC** (`vm-state`,
claim `<vmname>-vm-state`) holds all persistent sub-state, using SubPath mounts
to place each piece at the correct path. PVC-backed volumes produce plain named
Podman volumes (no tmpfs) that persist across pod restarts even though the
parent `private` emptyDir is cleared on every restart.

| SubPath | Mount path in compute | What it holds |
|---|---|---|
| `nvram` | `/var/run/kubevirt-private/libvirt/qemu/nvram` | UEFI NVRAM / EFI variables, Secure Boot state |
| `swtpm` | `/var/run/kubevirt-private/libvirt/qemu/swtpm` | swtpm NV storage (`tpm2-00.permall`): EK, persistent handles |
| `swtpm-localca` | `/var/run/kubevirt-private/var/lib/swtpm-localca` | swtpm CA certificate chain (issues the EK certificate) |

Losing `swtpm` or `swtpm-localca` resets the TPM identity, breaking BitLocker
and Windows activation. Losing `nvram` resets EFI variables and boot order.

Two additional emptyDir volumes (`private-libvirt`, `private-libvirt-qemu`) are
also added at the intermediate directory levels — matching KubeVirt's approach
for non-root VMIs. Without them, the intermediate directories would be created
by the container runtime as `root:fsGroup 0755`, blocking write access by the
qemu user (UID 107) when the PVC SubPath is overlaid on top.

### Readiness probe

Added to the compute container only when it has no existing
`ReadinessProbe` or `LivenessProbe`:

```
command: /bin/sh -c 'test "$(virsh domstate <namespace>_<vmname>)" = "running"'
InitialDelaySeconds: 120
PeriodSeconds:       30
TimeoutSeconds:      10
FailureThreshold:    3
```

A readiness probe (not liveness) is used deliberately: it surfaces VM health in
`podman events` without triggering container restarts — `virt-launcher-monitor`
owns the VM restart lifecycle.

### Environment variables injected into compute

| Variable | Value |
|---|---|
| `STANDALONE_VMI` | JSON-marshaled `vmi` (full `VirtualMachineInstance` struct after all step-3 mutations) |
| `VIRSH_DEFAULT_CONNECT_URI` | `qemu+unix:///session?socket=/var/run/libvirt/virtqemud-sock` |

`STANDALONE_VMI` replaces the Kubernetes API watch: virt-launcher reads the VMI
spec from this env var at startup instead of connecting to the API server.
`VIRSH_DEFAULT_CONNECT_URI` points virsh (and the readiness probe) at the
session-mode virtqemud socket rather than the system socket.

---

## Step 7 — ApplyPostConvertFixups

Operates on the already-generated INI text of each Quadlet unit file.

### PublishPort injection (optional)

`PublishPort=` lines are inserted into the `.pod` unit immediately before the
`[Install]` section. Quadlet requires port publishing to be declared on the pod
unit; declaring it on a `.container` unit is silently ignored.

| Flag | Line inserted |
|---|---|
| `--vnc-proxy` | `PublishPort=<vnc-port>:<vnc-port>` |
| `--console-proxy` | `PublishPort=<console-port>:<console-port>` |

### passt PATH override (optional — `--passt-workarounds`)

Two lines are inserted into the compute `.container` unit immediately before the
`[Service]` section:

```ini
SecurityLabelDisable=true
Environment=PATH=/passt-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
```

`SecurityLabelDisable=true` disables SELinux confinement for the compute
container. Without it, SELinux blocks execution of the binaries written into the
tmpfs-backed `passt-bin` named volume at runtime.

`PATH` is set explicitly (not appended to the image's PATH) with `/passt-bin`
first. Libvirt calls `virFindFileInPath("passt")` which searches `PATH`; with
`/passt-bin` first it finds the wrapper from the patcher init container before
`/usr/bin/passt`. The wrapper calls `/passt-bin/passt.avx2.patched` using an
absolute path, so `PATH` is not consulted for the patched binary itself.
