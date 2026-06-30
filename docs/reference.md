# Conversion reference

This document describes every transformation applied to a KubeVirt
`VirtualMachine` YAML on its way to native Quadlet unit files.  The pipeline
has two distinct stages.

---

## Stage 1 — VM spec → Pod spec

Sources: `pkg/standalone/pre_render.go`, `pkg/kubevirt/kubevirt.go`, `pkg/standalone/post_render.go`

`standalone.PrepareForRendering` (step 3) applies all pre-render fixups, then
`kubevirt.NewTemplateService` + `templateSvc.RenderLaunchManifest` (step 4) produce
the raw Pod spec, then `standalone.AdaptForStandalone` (step 5) applies post-render
fixups.

### 1.1 Pre-render fixups (before `RenderLaunchManifest`)

| What | Why |
|---|---|
| Stub PVC cache entries for every `persistentVolumeClaim` and `dataVolume` volume | `RenderLaunchManifest` looks up each PVC via the Kubernetes API; in standalone mode there is no API, so minimal stubs (Filesystem mode, ReadWriteOnce) are injected into a fake in-memory cache |
| Namespace defaults to `"default"` when absent | KubeVirt requires a non-empty namespace for domain naming (`<namespace>_<name>`) |
| `defaults.SetVirtualMachineDefaults` | Applies KubeVirt cluster defaults to the VM object |
| `vmCtrl.SetupVMIFromVM` | Derives a `VirtualMachineInstance` from the `VirtualMachine` |
| `defaults.SetDefaultVirtualMachineInstance` | Sets VMI defaults (CPU topology, clock, …) |
| `mutators.ApplyNewVMIMutations` | Runs KubeVirt admission webhook mutations (clock, features, …) |
| `vmispec.SetDefaultNetworkInterface` | Ensures at least one network interface is present |
| `util.SetDefaultVolumeDisk` | Assigns disks to volumes that have no explicit disk mapping |
| `vmCtrl.AutoAttachInputDevice` | Adds a USB tablet or PS/2 mouse if no input device is specified |
| `forcePasstBinding` | Replaces every interface binding (masquerade, bridge, Multus, …) with `passtBinding`; ensures a `Pod` network source exists.  Masquerade is not used because it causes a nil-panic in `PreCloudInitIso` when there is no cloud-init volume |
| VMI UID set to namespace | `virt-launcher-monitor` resolves the QEMU PID file as `<uid>_<name>.pid`; offline conversion produces no UID so the namespace is used as a stable stand-in |

### 1.2 Post-render fixups (after `RenderLaunchManifest`)

Applied in the order they appear in `AdaptForStandalone()`.

#### `cleanupForStandalone`

| Pod field | Change | Why |
|---|---|---|
| `spec.securityContext.sysctls` | Add `net.ipv4.ip_unprivileged_port_start=0` | passt drops privileges after startup; without this it cannot bind ports < 1024 even as root.  This sysctl is network-namespace-scoped and does not affect the host |
| `spec.readinessGates` | Removed | Kubernetes condition types are never set by Podman; any unresolved gate keeps the pod permanently unready |
| `metadata.ownerReferences` | Removed | Kubernetes GC metadata; confuses Podman output |
| `spec.nodeSelector` | Strip `cpumanager` and `kubevirt.io/schedulable` keys | KubeVirt node-tainting labels that have no meaning outside a cluster |
| `spec.restartPolicy` | Set to `OnFailure` | Allows Podman to retry on containerdisk race conditions |
| Init containers with `restartPolicy: Always` | Promoted to regular containers | KubeVirt 1.28+ emits native sidecars as init containers with `restartPolicy: Always`; Podman does not support this and they would block the init pipeline |
| Container/init-container resource `limits`/`requests` | Strip keys containing `/` | Device-plugin extended resources (e.g. `nvidia.com/gpu`) reference a scheduler that does not exist in Podman; built-in resources (`cpu`, `memory`, `hugepages-*`) are preserved |

#### `addConsoleProxySidecar` _(opt-in, `--console-proxy`)_

Adds a `console-proxy` sidecar (`alpine/socat` by default, override with
`--console-image`) that forwards the serial console Unix socket
(`/var/run/kubevirt-private/default/virt-serial0`) to TCP `--console-port`
(default: `2222`) inside the pod network namespace.

#### `injectVNCProxy` _(opt-in, `--vnc-proxy`)_

Adds a `vnc-proxy` sidecar (`alpine/socat` by default, override with
`--vnc-image`) that forwards the VNC Unix socket
(`/var/run/kubevirt-private/default/virt-vnc`) to TCP `--vnc-port`
(default: `5900`) inside the pod network namespace.  Publishing the port
from the `.pod` drop-in makes VNC reachable from the host.

#### `mountHostDevices`

| Added volume | Host path | Container path | Type |
|---|---|---|---|
| `kvm` | `/dev/kvm` | `/dev/kvm` | `AddDevice` |
| `tun` | `/dev/net/tun` | `/dev/net/tun` | `AddDevice` |
| `vhost-net` | `/dev/vhost-net` | `/dev/vhost-net` | `AddDevice` |
| `cgroup` | `/sys/fs/cgroup` | `/sys/fs/cgroup` | `Volume` read-only |
| `nvidia<N>`, `nvidiactl`, `nvidia-uvm*`, `nvidia-modeset` | `/dev/nvidia*` | same | `AddDevice` (NVIDIA GPUs only) |
| `dri-card<N>`, `dri-render<N>` | `/dev/dri/card*`, `/dev/dri/renderD*` | same | `AddDevice` (AMD/Intel GPUs) |
| `vfio`, `vfio-group-<N>` | `/dev/vfio/*` | same | `AddDevice` (PCI passthrough via vfio-pci) |

#### `injectDataVolumeInitContainers`

For each `dataVolumeTemplate` that has a matching volume in the VMI spec, a
`virt-launcher`-image init container is prepended that:

- **Plain storage**: creates an empty raw disk file (`disk.img`) if absent;
  grows it with `truncate` if the requested size increased.
- **Registry source** (`source.registry.url`): on first boot, pulls the
  containerdisk OCI image (added as a Kubernetes `image` volume to the pod),
  and converts the embedded qcow2/img to raw via `qemu-img convert`.
  Subsequent boots only handle size changes.

#### `injectVirtHandlerDirInit`

Prepends a `virt-handler-dir-init` init container (runs as uid 107 / `qemu`)
that runs `mkdir -p` on:

- `/var/run/kubevirt-private/libvirt/qemu`
- `/var/run/kubevirt/sockets`

In a real cluster `virt-handler` pre-creates these directories with `qemu`
ownership before starting `virt-launcher`.  In standalone mode the init
container replaces that role; without it `virt-launcher` fails with
`EACCES` after it drops privileges.

#### `injectPersistentStateVolumes`

Adds a single `PersistentVolumeClaim` volume (`vm-state`, claim
`<vm-name>-vm-state`) with three SubPath mounts on the compute container,
overlaying paths inside the `private` emptyDir with persistent named Podman
volume storage:

| SubPath | Mount path | Persists |
|---|---|---|
| `nvram` | `/var/run/kubevirt-private/libvirt/qemu/nvram` | UEFI EFI variables / boot order |
| `swtpm` | `/var/run/kubevirt-private/libvirt/qemu/swtpm` | TPM NV storage (`tpm2-00.permall`) |
| `swtpm-localca` | `/var/run/kubevirt-private/var/lib/swtpm-localca` | TPM CA chain (BitLocker, Windows activation) |

Two intermediate emptyDir volumes (`private-libvirt`, `private-libvirt-qemu`)
are also added so the qemu user (UID 107) can write into the SubPath directories.

#### `populateInterfaceStatus`

Sets `VMI.Status.Interfaces[i].PodInterfaceName = "eth<i>"` for each interface.
In a cluster `virt-handler` sets this; for standalone mode it is required so
`virt-launcher`'s passt integration finds its target interface.

#### `injectComputeReadinessProbe`

Adds a readiness probe to the compute container:

```
/bin/sh -c 'test "$(virsh domstate <namespace>_<name>)" = "running"'
```

`InitialDelaySeconds=120`, `PeriodSeconds=30`, `TimeoutSeconds=10`,
`FailureThreshold=3`.  Using a readiness probe (not liveness) surfaces VM
health in `podman events` without triggering a container restart — that is
`virt-launcher-monitor`'s responsibility.

#### Env var injection

| Env var | Value | Consumer |
|---|---|---|
| `STANDALONE_VMI` | JSON-encoded `VirtualMachineInstance` | `virt-launcher` bootstrap, passt hook |
| `VIRSH_DEFAULT_CONNECT_URI` | `qemu+unix:///session?socket=/var/run/libvirt/virtqemud-sock` | `virsh` inside the container |

#### `addPersistenceWarnings`

Adds `kubectl.kubernetes.io/last-applied-configuration`-style annotations to
the pod warning that PVC and hostDisk volumes only persist locally on this host.

---

## Stage 2 — Pod spec → Quadlet units (converter)

Sources: `pkg/quadlet/converter.go` (steps 6a+6), `pkg/standalone/post_convert.go` (step 7)

### 2.1 Pre-convert fixups (`preConvertFixups` — step 6a)

| Pod field | Change | Why |
|---|---|---|
| `kind` / `apiVersion` | Set to `Pod` / `v1` | Required by `podman kube quadlet` |
| `spec.terminationGracePeriodSeconds` | Set to `120` | Translated to `StopTimeout=120` in the container unit so `virt-launcher` has time to send ACPI shutdown before Podman sends SIGKILL |

### 2.2 In-process kube quadlet conversion (step 6)

The pod is passed to the vendored `internal/third_party/kube/quadlet.Convert`
in-process (no external binary). The pod is round-tripped through YAML to
bridge the `k8s.io/api/core/v1.Pod` type into the podman-vendored type system.

#### Pod unit (`.pod`)

- One `.pod` unit is always generated, wired to `default.target`.
- `Network=podman` is set by default so KubeVirt's passt finds `eth0` (pasta
  mode exposes the host physical interface instead, which breaks it).
- `sysctls` from `spec.securityContext.sysctls` become `Sysctl=` directives.
- All emptyDir volume names are listed as `Volume=` on the pod's infra container
  so they are created before any user container starts.

#### Container units (`.container`)

One `.container` file per init and regular container, named
`<prefix>-<container-name>.container`.

| Pod field | Quadlet directive | Notes |
|---|---|---|
| `spec.containers[].image` | `Image=` | |
| `spec.containers[].command[0]` | `Entrypoint=` | Only the first element; remaining args go into `Exec=` |
| `spec.containers[].command[1:]` | `Exec=` | |
| `spec.containers[].env` | `Environment=` or `EnvironmentFile=` | Complex values (quotes, special chars) go into a companion `.env` file |
| `spec.containers[].resources.limits.memory` | `Memory=` | Bytes |
| `spec.containers[].securityContext.capabilities.add` | `AddCapability=` | |
| `spec.containers[].securityContext.capabilities.drop` | `DropCapability=` | |
| `spec.containers[].securityContext.allowPrivilegeEscalation=false` | `NoNewPrivileges=true` | |
| `spec.containers[].securityContext.runAsUser` | `User=` | |
| `spec.restartPolicy=OnFailure` | `Restart=on-failure` | |
| `spec.terminationGracePeriodSeconds` | `StopTimeout=` | |
| `spec.containers[].readinessProbe` (exec) | `HealthCmd=` / `HealthInterval=` / … | |
| Pod-level sysctls | `Sysctl=` on the `.pod` unit (not the container) | |

Init containers additionally get:

```ini
[Service]
Type=oneshot
RemainAfterExit=yes
```

And a dependency chain: each init `.container` has `After=` / `Requires=` on
the previous init's `.service`, and the compute container depends on the last
init service.

#### Volume mapping

| Kubernetes source | Quadlet output |
|---|---|
| `hostPath` under `/dev/` or type `BlockDev`/`CharDev` | `AddDevice=<host>:<container>` (no mount options) |
| `hostPath` under `/sys/` or `/proc/` | `Volume=<host>:<container>` — no `:z` (kernel virtual FS must not be SELinux-relabeled) |
| Any other `hostPath` | `Volume=<host>:<container>:z[,ro][,rslave/rshared/private][,subpath=…]` |
| `persistentVolumeClaim` | `Volume=<prefix>-<volName>.volume:<mountPath>[:ro]` — a companion `<prefix>-<volName>.volume` unit is generated with an empty `[Volume]` section (Podman creates the named volume on first use) |
| `emptyDir` (any medium) | `Volume=<prefix>-<volName>-empty.volume:<mountPath>` — companion `<prefix>-<volName>-empty.volume` unit with `Type=tmpfs`, `Device=tmpfs`, `Options=nodev,mode=0777`; volatile (clears on every pod start) |
| `image` (OCI image volume) | `Mount=type=image,source=<ref>,dst=<path>[,rw]` — read-only by default; `rw` added unless the mount is explicitly read-only; no separate `.volume` or `.image` unit |
| `configMap` / `secret` | `Volume=<configMapDir>/<name>:<mountPath>:ro,z` — the actual files must be present on disk at `ScriptDir`; not handled automatically |

#### Passt workaround hook injection — step 7 (`standalone.ApplyPostConvertFixups`, `--passt-workarounds`)

When enabled, post-processes the compute `.container` file to add:

```ini
Volume=<scriptDir>/<vmName>-libvirt-hook.sh:/etc/libvirt/hooks/qemu:z,exec
SecurityLabelDisable=true
```

`SecurityLabelDisable=true` is required because the hook does XML manipulation
via SCM_RIGHTS fd-passing between QEMU and passt, which the host MAC policy
blocks when the container is SELinux-confined.  An additional
`<vmName>-libvirt-hook.sh` file is written alongside the Quadlet units.

The hook applies two patches at `prepare begin` time:

1. **`portForward` patch** — replaces empty `<portForward proto='tcp'/>` elements
   generated by KubeVirt's `passtBinding` with explicit `<range>` entries
   derived from `STANDALONE_VMI`.  Without this passt uses `--tcp-ports all`,
   exhausting `fs.file-max`.  Not needed when ports are declared in the VM
   interface spec.
2. **`mrg_rxbuf` patch** — injects a `<qemu:commandline>` argument
   `-set device.ua-default.mrg_rxbuf=off` to prevent a passt assertion failure
   with 2+ vCPU Windows guests.

These workarounds are not needed with `passt >= 0^20260611.ga9c61ff`
(KubeVirt PR [#18235](https://github.com/kubevirt/kubevirt/pull/18235)).

---

## Appendix — virt-launcher image and passt version

The default virt-launcher image (`quay.io/kubevirt/virt-launcher:v1.8.4`) ships
`passt 0^20250512.g8ec1341-2.el9`, which has two known issues in standalone mode:

1. **`mrg_rxbuf` crash** — with 2+ vCPUs, Windows' virtio-net driver posts many
   small RX buffers that exceed passt's `max_num_sg` limit, causing an assertion
   failure and network disconnection.

2. **UDP CPU spike** — passt consumes ~96% CPU and logs
   `"Invalid endpoint on UDP recvfrom()"` ([bug #185](https://bugs.passt.top/show_bug.cgi?id=185)).

Both are fixed in `passt >= 0^20260611.ga9c61ff` (KubeVirt PR
[#18235](https://github.com/kubevirt/kubevirt/pull/18235)).

**Options:**

- Use `--passt-workarounds` to apply runtime XML patches that mitigate both
  issues without changing the image (see the passt workaround hook section above).
- Use `--launcher-image` to supply a custom `virt-launcher` image that already
  includes the fixed passt version, for example one built with:

  ```bash
  curl -Lo /tmp/passt.rpm 'http://mirror.stream.centos.org/9-stream/AppStream/x86_64/os/Packages/passt-0%5E20260611.ga9c61ff-1.el9.x86_64.rpm'
  printf 'FROM quay.io/kubevirt/virt-launcher:v1.8.4\nUSER root\nCOPY passt.rpm /tmp/\nRUN rpm -Uvh --nodeps /tmp/passt.rpm && rm /tmp/passt.rpm\n' > /tmp/Containerfile
  podman build -t <your-registry>/virt-launcher:v1.8.4-passt-fixed -f /tmp/Containerfile /tmp
  ```

  Once KubeVirt cuts a release that includes PR #18235, `--launcher-image` can
  point to that official image instead.
