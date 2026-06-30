package standalone

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	virtv1 "kubevirt.io/api/core/v1"
)

// AdaptForStandalone is step 5: applies all post-render Pod mutations needed
// to run the KubeVirt-generated pod with Podman in standalone mode (no cluster).
func AdaptForStandalone(pod *k8sv1.Pod, prepared *PreparedVM, opts Options) (*k8sv1.Pod, error) {
	vmi := prepared.VMI
	vm := prepared.VM

	pod.TypeMeta = metav1.TypeMeta{
		Kind:       "Pod",
		APIVersion: "v1",
	}

	// Convert generateName to name for standalone pods (required by podman kube play).
	if pod.ObjectMeta.GenerateName != "" && pod.ObjectMeta.Name == "" {
		pod.ObjectMeta.Name = pod.ObjectMeta.GenerateName[:len(pod.ObjectMeta.GenerateName)-1]
		pod.ObjectMeta.GenerateName = ""
	}

	if opts.AddSerialProxy {
		addConsoleProxySidecar(pod, opts.SerialPort, opts.SerialImage)
	}

	if opts.AddVNCProxy {
		injectVNCProxy(pod, opts.VNCPort, opts.VNCImage)
	}

	mountHostDevices(pod, vmi)

	injectDataVolumeInitContainers(pod, vmi, vm, opts.LauncherImage)

	cleanupForStandalone(pod, vmi)

	injectVirtHandlerDirInit(pod, opts.LauncherImage)

	if opts.PasstWorkarounds {
		injectPasstBinaryPatcher(pod, opts)
	}

	// Inject persistent PVC-backed volumes for state that must survive pod
	// restarts. All other runtime paths under /var/run/kubevirt-private are
	// ephemeral emptyDir (tmpfs). The two sub-paths below overlay on top of
	// that tmpfs with persistent named Podman volumes:
	//
	//   - UEFI NVRAM: preserves EFI variables and boot order.
	//   - swtpm-localca: TPM CA certificate chain; losing it resets the TPM
	//     identity and breaks TPM-bound operations (e.g. BitLocker, Windows
	//     activation).
	//
	// Using PersistentVolumeClaim here means the quadlet converter generates a
	// plain named Podman volume (no tmpfs options), which persists across pod
	// restarts while the tmpfs-backed emptyDir is cleared on every restart.
	injectPersistentStateVolumes(pod, vmi)

	// Add persistence warning annotations for volumes that require special setup.
	addPersistenceWarnings(pod, vm)

	injectComputeReadinessProbe(pod, vmi)

	vmiJSON, err := json.Marshal(vmi)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal VMI: %v", err)
	}
	for i, c := range pod.Spec.Containers {
		if c.Name == "compute" {
			pod.Spec.Containers[i].Env = append(c.Env,
				k8sv1.EnvVar{Name: "STANDALONE_VMI", Value: string(vmiJSON)},
				k8sv1.EnvVar{Name: "VIRSH_DEFAULT_CONNECT_URI", Value: "qemu+unix:///session?socket=/var/run/libvirt/virtqemud-sock"},
			)
			break
		}
	}

	return pod, nil
}

// injectComputeReadinessProbe adds a readiness probe to the compute container
// that checks the VM domain state via virsh. Using a readiness probe (rather
// than a liveness probe) means the health check surfaces in podman events for
// observability without triggering a container restart — virt-launcher-monitor
// already owns the VM restart lifecycle.
//
// The domain name follows the libvirt convention used by virt-launcher:
// <namespace>_<vm-name>. VIRSH_DEFAULT_CONNECT_URI is already injected into
// the container env so virsh picks up the right virtqemud socket automatically.
func injectComputeReadinessProbe(pod *k8sv1.Pod, vmi *virtv1.VirtualMachineInstance) {
	domainName := fmt.Sprintf("%s_%s", vmi.Namespace, vmi.Name)
	probe := &k8sv1.Probe{
		ProbeHandler: k8sv1.ProbeHandler{
			Exec: &k8sv1.ExecAction{
				Command: []string{
					"/bin/sh", "-c",
					fmt.Sprintf(`test "$(virsh domstate %s)" = "running"`, domainName),
				},
			},
		},
		// Give virt-launcher, virtqemud, and the guest OS time to start before
		// the first check. 120 s covers the common case; adjust per workload.
		InitialDelaySeconds: 120,
		PeriodSeconds:       30,
		TimeoutSeconds:      10,
		FailureThreshold:    3,
	}
	for i, c := range pod.Spec.Containers {
		if c.Name == "compute" {
			if c.LivenessProbe != nil || c.ReadinessProbe != nil {
				break
			}
			pod.Spec.Containers[i].ReadinessProbe = probe
			break
		}
	}
}

// injectPersistentStateVolumes mirrors KubeVirt's backend-storage design: a
// single PVC ("vm-state") holds all persistent sub-state under
// /var/run/kubevirt-private that must survive pod restarts, using SubPath
// mounts to direct each piece to the correct location.
//
// Three sub-paths are persisted (matching upstream rendervolumes.go):
//
//	nvram        → UEFI NVRAM / EFI variables (boot order, Secure Boot state)
//	swtpm-localca → swtpm CA certificate chain (issues the TPM EK certificate)
//	swtpm        → swtpm NV storage (tpm2-00.permall, EK, persistent handles)
//
// All three live inside the private emptyDir (tmpfs) and would be wiped on
// every pod restart without this overlay. Losing swtpm resets the TPM
// identity; losing nvram resets EFI variables. Both break BitLocker and
// Windows activation on restart.
//
// Two intermediate emptyDir volumes (private-libvirt, private-libvirt-qemu)
// are also added, matching KubeVirt's approach for non-root VMIs: without them,
// the directories at those paths would be owned by root:fsGroup with 0755,
// blocking write access by the qemu user (uid 107).
func injectPersistentStateVolumes(pod *k8sv1.Pod, vmi *virtv1.VirtualMachineInstance) {
	const vmStateVol = "vm-state"
	claimName := vmi.Name + "-vm-state"

	pod.Spec.Volumes = append(pod.Spec.Volumes,
		k8sv1.Volume{
			Name: vmStateVol,
			VolumeSource: k8sv1.VolumeSource{
				PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
					ClaimName: claimName,
				},
			},
		},
		// Intermediate emptyDir volumes so the qemu user (uid 107) can write
		// into the sub-paths that the vm-state PVC is mounted at.
		k8sv1.Volume{
			Name:         "private-libvirt",
			VolumeSource: k8sv1.VolumeSource{EmptyDir: &k8sv1.EmptyDirVolumeSource{}},
		},
		k8sv1.Volume{
			Name:         "private-libvirt-qemu",
			VolumeSource: k8sv1.VolumeSource{EmptyDir: &k8sv1.EmptyDirVolumeSource{}},
		},
	)

	mounts := []k8sv1.VolumeMount{
		// Intermediate emptyDir mounts (must come before the SubPath PVC mounts
		// so the directories exist with correct ownership when PVC is overlaid).
		{Name: "private-libvirt", MountPath: "/var/run/kubevirt-private/libvirt"},
		{Name: "private-libvirt-qemu", MountPath: "/var/run/kubevirt-private/libvirt/qemu"},
		// Persistent state sub-paths from the single vm-state PVC.
		{Name: vmStateVol, MountPath: "/var/run/kubevirt-private/libvirt/qemu/nvram", SubPath: "nvram"},
		{Name: vmStateVol, MountPath: "/var/run/kubevirt-private/libvirt/qemu/swtpm", SubPath: "swtpm"},
		{Name: vmStateVol, MountPath: "/var/run/kubevirt-private/var/lib/swtpm-localca", SubPath: "swtpm-localca"},
	}

	for i, c := range pod.Spec.Containers {
		if c.Name == "compute" {
			pod.Spec.Containers[i].VolumeMounts = append(c.VolumeMounts, mounts...)
			break
		}
	}
}

// privateVolumeName returns the name of the volume that the compute container
// mounts at /var/run/kubevirt-private. This is the canonical way to locate
// the private emptyDir regardless of how KubeVirt names it.
//
// The "compute" container is always present at this point: it is produced by
// RenderLaunchManifest (step 4) under that exact hardcoded name and this
// function is called before any cleanup that touches containers. The empty
// return is a defensive fallback that should never be reached in practice.
func privateVolumeName(pod *k8sv1.Pod) string {
	const privatePath = "/var/run/kubevirt-private"
	for _, c := range pod.Spec.Containers {
		if c.Name != "compute" {
			continue
		}
		for _, m := range c.VolumeMounts {
			if m.MountPath == privatePath {
				return m.Name
			}
		}
	}
	return ""
}

// injectVNCProxy adds a socat sidecar that forwards the VNC Unix socket
// (/var/run/kubevirt-private/default/virt-vnc) to TCP inside the pod.
func injectVNCProxy(pod *k8sv1.Pod, port int, image string) {
	privateVolName := privateVolumeName(pod)
	if privateVolName == "" {
		return
	}

	pod.Spec.Containers = append(pod.Spec.Containers, k8sv1.Container{
		Name:  "vnc-proxy",
		Image: image,
		Command: []string{
			"socat",
			fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", port),
			"UNIX:/var/run/kubevirt-private/default/virt-vnc",
		},
		VolumeMounts: []k8sv1.VolumeMount{
			{Name: privateVolName, MountPath: "/var/run/kubevirt-private"},
		},
	})
}

func addConsoleProxySidecar(pod *k8sv1.Pod, proxyPort int, image string) {
	privateVolName := privateVolumeName(pod)
	if privateVolName == "" {
		return
	}

	pod.Spec.Containers = append(pod.Spec.Containers, k8sv1.Container{
		Name:  "console-proxy",
		Image: image,
		Command: []string{
			"socat",
			fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", proxyPort),
			"UNIX:/var/run/kubevirt-private/default/virt-serial0",
		},
		VolumeMounts: []k8sv1.VolumeMount{
			{Name: privateVolName, MountPath: "/var/run/kubevirt-private"},
		},
	})
}

func mountHostDevices(pod *k8sv1.Pod, vmi *virtv1.VirtualMachineInstance) {
	hostPathCharDev := k8sv1.HostPathCharDev

	kvmDevices := []struct {
		name string
		path string
	}{
		{"kvm", "/dev/kvm"},
		{"tun", "/dev/net/tun"},
		{"vhost-net", "/dev/vhost-net"},
	}

	for _, dev := range kvmDevices {
		mountDevice(pod, dev.name, dev.path, &hostPathCharDev)
	}

	hostPathDir := k8sv1.HostPathDirectory
	pod.Spec.Volumes = append(pod.Spec.Volumes, k8sv1.Volume{
		Name: "cgroup",
		VolumeSource: k8sv1.VolumeSource{
			HostPath: &k8sv1.HostPathVolumeSource{
				Path: "/sys/fs/cgroup",
				Type: &hostPathDir,
			},
		},
	})
	for i, c := range pod.Spec.Containers {
		if c.Name == "compute" {
			pod.Spec.Containers[i].VolumeMounts = append(c.VolumeMounts, k8sv1.VolumeMount{
				Name:      "cgroup",
				MountPath: "/sys/fs/cgroup",
				ReadOnly:  true,
			})
			break
		}
	}

	if vmi != nil && vmi.Spec.Domain.Devices.GPUs != nil {
		for i, gpu := range vmi.Spec.Domain.Devices.GPUs {
			vendor := detectGPUVendor(gpu.DeviceName)

			switch vendor {
			case "nvidia":
				mountDevice(pod, fmt.Sprintf("nvidia%d", i), fmt.Sprintf("/dev/nvidia%d", i), &hostPathCharDev)
				if i == 0 {
					mountDevice(pod, "nvidiactl", "/dev/nvidiactl", &hostPathCharDev)
					mountDevice(pod, "nvidia-uvm", "/dev/nvidia-uvm", &hostPathCharDev)
					mountDevice(pod, "nvidia-uvm-tools", "/dev/nvidia-uvm-tools", &hostPathCharDev)
					mountDevice(pod, "nvidia-modeset", "/dev/nvidia-modeset", &hostPathCharDev)
				}

			case "amd", "intel":
				mountDevice(pod, fmt.Sprintf("dri-card%d", i), fmt.Sprintf("/dev/dri/card%d", i), &hostPathCharDev)
				mountDevice(pod, fmt.Sprintf("dri-render%d", i), fmt.Sprintf("/dev/dri/renderD%d", 128+i), &hostPathCharDev)

			default:
				fmt.Fprintf(os.Stderr, "Warning: Unknown GPU vendor for device %s, mounting generic DRI devices\n", gpu.DeviceName)
				mountDevice(pod, fmt.Sprintf("dri-card%d", i), fmt.Sprintf("/dev/dri/card%d", i), &hostPathCharDev)
			}
		}
	}

	if vmi != nil && vmi.Spec.Domain.Devices.HostDevices != nil {
		// In standalone mode there is no KubeVirt device plugin to resolve the
		// hostDevice resource name to a specific PCI address. Instead, mount all
		// devices already bound to the vfio-pci driver on the host. The operator
		// must bind the desired devices to vfio-pci before running the VM.
		groups, err := resolveVFIOGroups()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not scan VFIO devices: %v\n", err)
		}
		if len(groups) == 0 {
			for _, hostdev := range vmi.Spec.Domain.Devices.HostDevices {
				fmt.Fprintf(os.Stderr, "Warning: PCI hostdevice %q requested but no devices "+
					"bound to vfio-pci found. Bind the device with "+
					"\"echo <pci-addr> > /sys/bus/pci/drivers/vfio-pci/bind\" first.\n",
					hostdev.Name)
			}
		}

		mountDevice(pod, "vfio", "/dev/vfio/vfio", &hostPathCharDev)

		for _, group := range groups {
			devPath := fmt.Sprintf("/dev/vfio/%s", group)
			mountDevice(pod, fmt.Sprintf("vfio-group-%s", group), devPath, &hostPathCharDev)
		}
	}
}

func mountDevice(pod *k8sv1.Pod, volumeName, devicePath string, pathType *k8sv1.HostPathType) {
	pod.Spec.Volumes = append(pod.Spec.Volumes, k8sv1.Volume{
		Name: volumeName,
		VolumeSource: k8sv1.VolumeSource{
			HostPath: &k8sv1.HostPathVolumeSource{
				Path: devicePath,
				Type: pathType,
			},
		},
	})

	for i, c := range pod.Spec.Containers {
		if c.Name == "compute" {
			pod.Spec.Containers[i].VolumeMounts = append(c.VolumeMounts, k8sv1.VolumeMount{
				Name:      volumeName,
				MountPath: devicePath,
			})
			break
		}
	}
}

// resolveVFIOGroups scans /sys/bus/pci/drivers/vfio-pci for PCI devices that
// are bound to the vfio-pci driver and returns the sorted list of unique IOMMU
// group numbers. Returns nil without error when vfio-pci has no bound devices
// or when the directory does not exist (vfio not loaded).
func resolveVFIOGroups() ([]string, error) {
	const vfioDriverDir = "/sys/bus/pci/drivers/vfio-pci"
	entries, err := os.ReadDir(vfioDriverDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	seen := map[string]bool{}
	for _, e := range entries {
		if !strings.Contains(e.Name(), ":") {
			continue
		}
		groupLink := filepath.Join(vfioDriverDir, e.Name(), "iommu_group")
		target, err := os.Readlink(groupLink)
		if err != nil {
			continue
		}
		seen[filepath.Base(target)] = true
	}

	groups := make([]string, 0, len(seen))
	for g := range seen {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	return groups, nil
}

func detectGPUVendor(deviceName string) string {
	deviceLower := strings.ToLower(deviceName)
	if strings.Contains(deviceLower, "nvidia") {
		return "nvidia"
	}
	if strings.Contains(deviceLower, "amd") || strings.Contains(deviceLower, "radeon") {
		return "amd"
	}
	if strings.Contains(deviceLower, "intel") {
		return "intel"
	}
	return "unknown"
}

// injectDataVolumeInitContainers prepends an init container for each DataVolume
// that has a matching dataVolumeTemplate. Two modes are supported:
//
//   - Plain storage: creates an empty raw disk file on first boot; grows it on
//     subsequent boots if the requested size increased.
//
//   - Registry source: on first boot, pulls the containerdisk image (added as
//     an OCI image volume) and converts the embedded qcow2/img to a raw file on
//     the PVC using qemu-img convert. Both docker:// and oci:// URL prefixes are
//     stripped to obtain the plain image reference. Subsequent boots only handle
//     size changes.
func injectDataVolumeInitContainers(pod *k8sv1.Pod, vmi *virtv1.VirtualMachineInstance, vm *virtv1.VirtualMachine, launcherImage string) {
	type dvMeta struct {
		sizeBytes   int64
		registryURL string
	}
	metaByDVName := map[string]dvMeta{}
	for _, dvt := range vm.Spec.DataVolumeTemplates {
		if dvt.Spec.Storage == nil {
			continue
		}
		m := dvMeta{}
		if qty, ok := dvt.Spec.Storage.Resources.Requests[k8sv1.ResourceStorage]; ok {
			m.sizeBytes = qty.Value()
		}
		if dvt.Spec.Source != nil && dvt.Spec.Source.Registry != nil && dvt.Spec.Source.Registry.URL != nil {
			m.registryURL = *dvt.Spec.Source.Registry.URL
		}
		metaByDVName[dvt.Name] = m
	}

	var newInits []k8sv1.Container
	allowPrivEsc := false

	for _, vol := range vmi.Spec.Volumes {
		if vol.DataVolume == nil {
			continue
		}
		m, ok := metaByDVName[vol.DataVolume.Name]
		if !ok || m.sizeBytes <= 0 {
			continue
		}

		podVolumeName := ""
		for _, pv := range pod.Spec.Volumes {
			if pv.PersistentVolumeClaim != nil && pv.PersistentVolumeClaim.ClaimName == vol.DataVolume.Name {
				podVolumeName = pv.Name
				break
			}
		}
		if podVolumeName == "" {
			continue
		}

		diskDir := fmt.Sprintf("/var/run/kubevirt-private/vmi-disks/%s", vol.Name)
		diskPath := diskDir + "/disk.img"
		volSlug := strings.ToLower(strings.ReplaceAll(vol.Name, "_", "-"))

		if m.registryURL != "" {
			imageRef := strings.TrimPrefix(strings.TrimPrefix(m.registryURL, "docker://"), "oci://")
			sourceVolName := fmt.Sprintf("import-src-%s", volSlug)
			sourceMountPath := fmt.Sprintf("/var/run/import-source/%s", vol.Name)

			pod.Spec.Volumes = append(pod.Spec.Volumes, k8sv1.Volume{
				Name: sourceVolName,
				VolumeSource: k8sv1.VolumeSource{
					Image: &k8sv1.ImageVolumeSource{
						Reference:  imageRef,
						PullPolicy: k8sv1.PullIfNotPresent,
					},
				},
			})

			script := fmt.Sprintf(`DISK=%q
SOURCE_MOUNT=%q
SIZE=%d
if [ ! -f "$DISK" ]; then
    SOURCE=$(find "$SOURCE_MOUNT" -maxdepth 2 \( -name "*.img" -o -name "*.qcow2" \) 2>/dev/null | head -1)
    if [ -z "$SOURCE" ]; then
        echo "ERROR: no disk image found in $SOURCE_MOUNT" >&2
        exit 1
    fi
    echo "Importing $SOURCE -> $DISK"
    qemu-img convert -O raw "$SOURCE" "$DISK"
    CURRENT=$(stat -c '%%s' "$DISK")
    if [ "$SIZE" -gt "$CURRENT" ]; then
        qemu-img resize -f raw "$DISK" "$SIZE"
    fi
else
    CURRENT=$(stat -c '%%s' "$DISK")
    if [ "$SIZE" -gt "$CURRENT" ]; then
        qemu-img resize -f raw "$DISK" "$SIZE"
    elif [ "$SIZE" -lt "$CURRENT" ]; then
        echo "Warning: ignoring shrink request for $DISK ($CURRENT -> $SIZE bytes)" >&2
    fi
fi`, diskPath, sourceMountPath, m.sizeBytes)

			newInits = append(newInits, k8sv1.Container{
				Name:    fmt.Sprintf("import-disk-%s", volSlug),
				Image:   launcherImage,
				Command: []string{"/bin/sh", "-c"},
				Args:    []string{script},
				VolumeMounts: []k8sv1.VolumeMount{
					{Name: podVolumeName, MountPath: diskDir},
					{Name: sourceVolName, MountPath: sourceMountPath, ReadOnly: true},
				},
				SecurityContext: &k8sv1.SecurityContext{
					AllowPrivilegeEscalation: &allowPrivEsc,
					Capabilities:             &k8sv1.Capabilities{Drop: []k8sv1.Capability{"ALL"}},
				},
			})
		} else {
			script := fmt.Sprintf(`DISK=%q
SIZE=%d
if [ ! -f "$DISK" ]; then
    qemu-img create -f raw "$DISK" "$SIZE"
else
    CURRENT=$(stat -c '%%s' "$DISK")
    if [ "$SIZE" -gt "$CURRENT" ]; then
        qemu-img resize -f raw "$DISK" "$SIZE"
    elif [ "$SIZE" -lt "$CURRENT" ]; then
        echo "Warning: ignoring shrink request for $DISK ($CURRENT -> $SIZE bytes)" >&2
    fi
fi`, diskPath, m.sizeBytes)

			newInits = append(newInits, k8sv1.Container{
				Name:    fmt.Sprintf("init-disk-%s", volSlug),
				Image:   launcherImage,
				Command: []string{"/bin/sh", "-c"},
				Args:    []string{script},
				VolumeMounts: []k8sv1.VolumeMount{
					{Name: podVolumeName, MountPath: diskDir},
				},
				SecurityContext: &k8sv1.SecurityContext{
					AllowPrivilegeEscalation: &allowPrivEsc,
					Capabilities:             &k8sv1.Capabilities{Drop: []k8sv1.Capability{"ALL"}},
				},
			})
		}
	}

	if len(newInits) > 0 {
		pod.Spec.InitContainers = append(newInits, pod.Spec.InitContainers...)
	}
}

// injectVirtHandlerDirInit prepends an init container that pre-creates the
// directory structure that virt-launcher needs to write to after dropping
// privileges to qemu (uid 107, via --run-as-nonroot).
//
// In a full KubeVirt cluster virt-handler (the per-node agent) initialises
// /var/run/kubevirt-private and /var/run/kubevirt/sockets before starting
// virt-launcher. In standalone mode virt-handler does not run, so the init
// container replaces that role.
// injectVirtHandlerDirInit prepends an init container that replicates the
// per-pod directory setup that virt-handler performs on real KubeVirt nodes,
// and also enforces Kubernetes emptyDir semantics for Podman named volumes.
//
// # virt-handler role
//
// In a full KubeVirt cluster, virt-handler (the per-node agent) initialises
// /var/run/kubevirt-private and /var/run/kubevirt/sockets before starting
// virt-launcher. In standalone mode virt-handler does not run, so the init
// container replaces that role by pre-creating subdirectories that
// virt-launcher expects to exist (e.g. libvirt/qemu, sockets) before it
// drops privileges to uid 107 (qemu).
//
// # emptyDir lifecycle
//
// In Kubernetes, emptyDir volumes are always freshly provisioned when a pod
// is scheduled; they are never shared between different pod lifetimes. Podman
// named volumes persist indefinitely, so without intervention, stale libvirt
// and QEMU runtime state (domain XML, dead monitor sockets, PID files) from a
// previous run survives into the next start and causes QEMU to crash
// immediately when virtqemud tries to reconnect to a dead process.
//
// The init container therefore:
//  1. Mounts every emptyDir volume at a flat /emptydir/<name> path (avoiding
//     nested mount conflicts with the main container's mounts).
//  2. Wipes the volume contents with find -mindepth 1 -delete, matching the
//     Kubernetes guarantee that emptyDir is fresh on every pod start.
//  3. Recreates the specific subdirectory structure that virt-launcher expects
//     before it drops privileges to uid 107 (qemu).
//
// Persistent volumes (nvram, swtpm-ca, rootdisk) do not have EmptyDir set and
// are never mounted here, so their data is never touched.
// injectVirtHandlerDirInit prepends an init container that replicates the
// per-pod directory setup that virt-handler performs on real KubeVirt nodes.
//
// In a full KubeVirt cluster, virt-handler (the per-node agent) initialises
// /var/run/kubevirt-private and /var/run/kubevirt/sockets before virt-launcher
// starts. In standalone mode virt-handler does not run, so this init container
// pre-creates the subdirectories that virt-launcher expects to exist before it
// drops privileges to uid 107 (qemu).
//
// emptyDir volumes are Quadlet tmpfs-backed named volumes. Because tmpfs is
// fresh on every pod start, no wipe is needed here — the subdirectory setup is
// the only thing required.
func injectVirtHandlerDirInit(pod *k8sv1.Pod, launcherImage string) {
	// emptyDir volumes that require specific subdirectories to exist before
	// virt-launcher starts (relative to the volume root).
	subdirsForVol := map[string][]string{
		"private": {"libvirt/qemu"},
	}

	qemuUID := int64(107)
	var mounts []k8sv1.VolumeMount
	var cmds []string

	for _, v := range pod.Spec.Volumes {
		if v.EmptyDir == nil {
			continue
		}
		subdirs, ok := subdirsForVol[volumeKey(v.Name)]
		if !ok {
			continue
		}
		mountPath := "/emptydir/" + v.Name
		mounts = append(mounts, k8sv1.VolumeMount{
			Name:      v.Name,
			MountPath: mountPath,
		})
		for _, sub := range subdirs {
			cmds = append(cmds, fmt.Sprintf("mkdir -p %s/%s", mountPath, sub))
		}
	}

	if len(cmds) == 0 {
		return
	}

	pod.Spec.InitContainers = append([]k8sv1.Container{
		{
			Name:    "virt-handler-dir-init",
			Image:   launcherImage,
			Command: []string{"/bin/bash", "-c", strings.Join(cmds, " && ")},
			VolumeMounts: mounts,
			SecurityContext: &k8sv1.SecurityContext{RunAsUser: &qemuUID},
		},
	}, pod.Spec.InitContainers...)
}

// volumeKey extracts the logical volume name used as a key into subdirsForVol.
// Quadlet-generated volume names follow the pattern <prefix>-<key>-empty; we
// want the middle segment (e.g. "private" from "virt-launcher-foo-private-empty").
func volumeKey(volumeName string) string {
	if strings.HasSuffix(volumeName, "-empty") {
		trimmed := strings.TrimSuffix(volumeName, "-empty")
		if idx := strings.LastIndex(trimmed, "-"); idx >= 0 {
			return trimmed[idx+1:]
		}
	}
	return volumeName
}

// injectPasstBinaryPatcher adds an init container that patches the passt.avx2
// binary for the mrg_rxbuf crash (passt versions predating PR #18235), and
// mounts the result into the compute container via a shared emptyDir volume.
//
// The patched binary and a thin wrapper are written to an emptyDir volume named
// "passt-bin" mounted at /passt-bin/ in both the init and compute containers.
// Step 7 (ApplyPostConvertFixups) then prepends /passt-bin to PATH in the
// compute container's environment so that libvirt's virFindFileInPath("passt")
// picks up the wrapper before /usr/bin/passt.
//
// The wrapper calls /passt-bin/passt.avx2.patched (absolute path within the
// shared volume), avoiding any dependency on the host output directory.
func injectPasstBinaryPatcher(pod *k8sv1.Pod, opts Options) {
	// Pure-sh binary patch using dd + od — no Python or perl required.
	// The virt-launcher image contains dd, od, cp, and printf but not python3.
	//
	// Offset 0x2815d (decimal 164189): start of a jae rel32 instruction that
	// branches to the abort path when the passt scattergather list overflows.
	// We overwrite the 4-byte operand at 0x2815f (164191) with 0x58000000
	// (little-endian 88) to redirect the jump to 0x281bb (the truncation epilogue).
	// This is specific to passt 0^20250512.g8ec1341. If the binary doesn't match
	// we copy it unpatched so the wrapper still works (no crash for 1-vCPU guests).
	script := "set -e\n" +
		"SRC=/usr/bin/passt.avx2\n" +
		"OUT=/passt-bin/passt.avx2.patched\n" +
		"cp \"$SRC\" \"$OUT\"\n" +
		"chmod +x \"$OUT\"\n" +
		// Read 6 bytes at 0x2815d and compare against the known jae opcode.
		"EXPECTED=0f83d2000000\n" +
		"ACTUAL=$(dd if=\"$SRC\" bs=1 skip=164189 count=6 2>/dev/null | od -A n -t x1 | tr -d ' \\n')\n" +
		"if [ \"$ACTUAL\" = \"$EXPECTED\" ]; then\n" +
		// Write the patched 4-byte operand at 0x2815f.
		"    printf '\\x58\\x00\\x00\\x00' | dd of=\"$OUT\" bs=1 seek=164191 conv=notrunc 2>/dev/null\n" +
		"    echo 'Patched passt.avx2 OK'\n" +
		"else\n" +
		"    echo \"Warning: passt.avx2 version not recognized (got $ACTUAL); using unpatched binary\" >&2\n" +
		"fi\n" +
		"printf '#!/bin/sh\\nexec /passt-bin/passt.avx2.patched \"$@\"\\n' > /passt-bin/passt\n" +
		"chmod +x /passt-bin/passt\n"

	const passtBinVol = "passt-bin"
	rootUID := int64(0)

	pod.Spec.Volumes = append(pod.Spec.Volumes, k8sv1.Volume{
		Name:         passtBinVol,
		VolumeSource: k8sv1.VolumeSource{EmptyDir: &k8sv1.EmptyDirVolumeSource{}},
	})

	pod.Spec.InitContainers = append(pod.Spec.InitContainers, k8sv1.Container{
		Name:    "passt-binary-patcher",
		Image:   opts.LauncherImage,
		Command: []string{"/bin/sh", "-c", script},
		VolumeMounts: []k8sv1.VolumeMount{
			{Name: passtBinVol, MountPath: "/passt-bin"},
		},
		SecurityContext: &k8sv1.SecurityContext{RunAsUser: &rootUID},
	})

	for i, c := range pod.Spec.Containers {
		if c.Name != "compute" {
			continue
		}
		pod.Spec.Containers[i].VolumeMounts = append(c.VolumeMounts, k8sv1.VolumeMount{
			Name:      passtBinVol,
			MountPath: "/passt-bin",
			ReadOnly:  true,
		})
		break
	}
}

func cleanupForStandalone(pod *k8sv1.Pod, vmi *virtv1.VirtualMachineInstance) {
	// Allow passt to forward any port including privileged ones (< 1024).
	// net.ipv4.ip_unprivileged_port_start is a network-namespace-scoped sysctl
	// so this only affects the pod's isolated network — it has no impact on
	// the host.
	if pod.Spec.SecurityContext == nil {
		pod.Spec.SecurityContext = &k8sv1.PodSecurityContext{}
	}
	pod.Spec.SecurityContext.Sysctls = append(
		pod.Spec.SecurityContext.Sysctls,
		k8sv1.Sysctl{Name: "net.ipv4.ip_unprivileged_port_start", Value: "0"},
	)

	// ReadinessGates reference Kubernetes condition types that never get set in
	// standalone mode. Podman treats any unresolved gate as permanently unready
	// and the pod never transitions to running — drop them entirely.
	pod.Spec.ReadinessGates = nil

	// OwnerReferences are Kubernetes garbage-collection metadata.
	pod.ObjectMeta.OwnerReferences = nil

	if pod.Spec.NodeSelector != nil {
		delete(pod.Spec.NodeSelector, virtv1.CPUManager)
		delete(pod.Spec.NodeSelector, virtv1.DeprecatedCPUManager)
		if len(pod.Spec.NodeSelector) == 0 {
			pod.Spec.NodeSelector = nil
		}
	}

	// Warn about dedicated CPU placement — CPU pinning must be configured
	// at the container runtime level (e.g., podman --cpuset-cpus).
	if vmi.Spec.Domain.CPU != nil && vmi.Spec.Domain.CPU.DedicatedCPUPlacement {
		fmt.Fprintf(os.Stderr, "Warning: VM requests dedicatedCpuPlacement. "+
			"For standalone execution, configure CPU pinning via the container runtime "+
			"(e.g., podman run --cpuset-cpus=0-3)\n")
	}

	pod.Spec.RestartPolicy = k8sv1.RestartPolicyOnFailure

	// Move restartPolicy=Always init containers to regular containers.
	// Kubernetes 1.28+ treats these as native sidecars, but Podman doesn't
	// support this and they block the init container pipeline.
	var keptInit []k8sv1.Container
	for _, c := range pod.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == k8sv1.ContainerRestartPolicyAlways {
			c.RestartPolicy = nil
			pod.Spec.Containers = append(pod.Spec.Containers, c)
		} else {
			keptInit = append(keptInit, c)
		}
	}
	pod.Spec.InitContainers = keptInit

	// Strip Kubernetes device-plugin extended resources (e.g. nvidia.com/gpu,
	// intel.com/qat). These are resolved by the device plugin scheduler at
	// runtime; Podman has no device plugin manager and will reject any resource
	// name that contains a "/" (the domain-qualified form required for all
	// extended resources). Built-in resources (cpu, memory, hugepages-*) never
	// contain "/" and are left untouched.
	stripExtendedResources := func(list k8sv1.ResourceList) k8sv1.ResourceList {
		out := make(k8sv1.ResourceList, len(list))
		for name, qty := range list {
			if !strings.Contains(string(name), "/") {
				out[name] = qty
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	for i, c := range pod.Spec.Containers {
		pod.Spec.Containers[i].Resources.Limits = stripExtendedResources(c.Resources.Limits)
		pod.Spec.Containers[i].Resources.Requests = stripExtendedResources(c.Resources.Requests)
	}
	for i, c := range pod.Spec.InitContainers {
		pod.Spec.InitContainers[i].Resources.Limits = stripExtendedResources(c.Resources.Limits)
		pod.Spec.InitContainers[i].Resources.Requests = stripExtendedResources(c.Resources.Requests)
	}
}

func addPersistenceWarnings(pod *k8sv1.Pod, vm *virtv1.VirtualMachine) {
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	spec := vm.Spec.Template.Spec
	var warnings []string

	hasPVC := false
	for _, vol := range spec.Volumes {
		if vol.PersistentVolumeClaim != nil {
			hasPVC = true
			break
		}
	}
	if hasPVC {
		warnings = append(warnings, "PVC volumes: In standalone mode, persistentVolumeClaim volumes become local Podman named volumes. They persist across pod restarts on THIS host only, but will NOT survive if you move the Pod to another machine or reinstall Podman.")
	}

	hasHostDisk := false
	for _, vol := range spec.Volumes {
		if vol.HostDisk != nil {
			hasHostDisk = true
			break
		}
	}
	if hasHostDisk {
		warnings = append(warnings, "hostDisk volumes: The specified disk image files must exist on the host filesystem at the paths defined in the VM spec. For DiskOrCreate type, the file will be created if missing.")
	}

	if len(warnings) > 0 {
		pod.Annotations["kubevirt-vm-to-pod/persistence-warning"] = strings.Join(warnings, " | ")
	}
}

