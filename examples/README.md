# Examples

Ready-to-use `VirtualMachine` YAML files for `kubevirt-vm-to-quadlet`.

| File | Guest | Disk source | Use case |
|---|---|---|---|
| `fedora-containerdisk.yaml` | Fedora 40 | Inline `containerDisk` | Quick smoke-test; disk is ephemeral (lost on pod restart) |
| `fedora-datavolume.yaml` | Fedora 40 | `dataVolumeTemplate` (registry) | Persistent Fedora VM; image imported from containerdisk on first boot |
| `windows-install.yaml` | Windows 11 | ISO + virtio-win ISO (hostDisk) | Fresh Windows installation; requires ISO files on the host |
| `windows-vm.yaml` | Windows 11 | Installed disk image (hostDisk) | Running Windows VM after installation is complete |
| `cirros-demo.yaml` | CirrOS | Local qcow2 (hostDisk) | Minimal demo to verify the toolchain end-to-end |

## Quick start

```bash
# Fedora — ephemeral (no data persistence):
kubevirt-vm-to-quadlet fedora-containerdisk.yaml --output-dir ~/.config/containers/systemd/fedora-vm
systemctl --user daemon-reload && systemctl --user start fedora-vm-pod

# Fedora — persistent (disk survives pod restarts):
kubevirt-vm-to-quadlet fedora-datavolume.yaml --output-dir ~/.config/containers/systemd/fedora-vm
systemctl --user daemon-reload && systemctl --user start fedora-vm-pod

# Windows — installation phase (edit hostDisk paths first):
kubevirt-vm-to-quadlet windows-install.yaml --output-dir ~/.config/containers/systemd/windows-vm
systemctl --user daemon-reload && systemctl --user start windows-vm-pod

# Windows — post-install (edit hostDisk path to point at the installed image):
kubevirt-vm-to-quadlet windows-vm.yaml --output-dir ~/.config/containers/systemd/windows-vm
```

## Publishing ports

Add a drop-in file alongside the generated units to publish guest ports on the
host without regenerating the Quadlet files:

```ini
# ~/.config/containers/systemd/fedora-vm/fedora-vm-pod.kube
[Pod]
PublishPort=2222:22
```

Then reload: `systemctl --user daemon-reload && systemctl --user restart fedora-vm-pod`
