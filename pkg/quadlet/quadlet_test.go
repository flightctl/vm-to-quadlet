package quadlet

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// opts returns default Options for tests.
func opts() Options {
	return DefaultOptions()
}

// ── helpers ────────────────────────────────────────────────────────────────

func findFile(files []UnitFile, name string) *UnitFile {
	for i := range files {
		if files[i].Name == name {
			return &files[i]
		}
	}
	return nil
}

func requireFile(t *testing.T, files []UnitFile, name string) *UnitFile {
	t.Helper()
	f := findFile(files, name)
	require.NotNil(t, f, "expected file %q in output but it was missing; got: %v", name, fileNames(files))
	return f
}

func requireNoFile(t *testing.T, files []UnitFile, name string) {
	t.Helper()
	f := findFile(files, name)
	require.Nil(t, f, "expected file %q to be absent but it was present", name)
}

func fileNames(files []UnitFile) []string {
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	return names
}

// simplePod returns a minimal pod with a single compute container.
func simplePod(name string) *k8sv1.Pod {
	return &k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: k8sv1.PodSpec{
			Containers: []k8sv1.Container{
				{
					Name:  "compute",
					Image: "quay.io/kubevirt/virt-launcher:v1.8.0",
				},
			},
		},
	}
}

// ── RenderContainerUnit ────────────────────────────────────────────────────

func TestRenderContainerUnit(t *testing.T) {
	t.Run("minimal unit only emits Container section", func(t *testing.T) {
		out := RenderContainerUnit(ContainerUnit{
			Container: ContainerSection{Image: "my-image"},
		})
		require.Contains(t, out, "[Container]")
		require.Contains(t, out, "Image=my-image")
		require.NotContains(t, out, "[Unit]")
		require.NotContains(t, out, "[Service]")
		require.NotContains(t, out, "[Install]")
	})

	t.Run("multi-value directives emit one line each", func(t *testing.T) {
		out := RenderContainerUnit(ContainerUnit{
			Container: ContainerSection{
				Image:        "img",
				Environments: []string{"FOO=1", "BAR=2"},
				Volumes:      []string{"vol1:/mnt/a", "vol2:/mnt/b"},
				AddDevices:   []string{"/dev/kvm", "/dev/net/tun"},
			},
		})
		require.Equal(t, 2, strings.Count(out, "Environment="))
		require.Equal(t, 2, strings.Count(out, "Volume="))
		require.Equal(t, 2, strings.Count(out, "AddDevice="))
	})

	t.Run("Entrypoint is emitted between Image and Exec", func(t *testing.T) {
		out := RenderContainerUnit(ContainerUnit{
			Container: ContainerSection{
				Image:      "quay.io/test:latest",
				Entrypoint: "/bin/sh",
				Exec:       "/init.sh",
			},
		})
		require.Contains(t, out, "Entrypoint=/bin/sh")
		imagePos := strings.Index(out, "Image=")
		entryPos := strings.Index(out, "Entrypoint=")
		execPos := strings.Index(out, "Exec=")
		require.Less(t, imagePos, entryPos)
		require.Less(t, entryPos, execPos)
	})

	t.Run("Entrypoint omitted when empty", func(t *testing.T) {
		out := RenderContainerUnit(ContainerUnit{
			Container: ContainerSection{Image: "quay.io/test:latest"},
		})
		require.NotContains(t, out, "Entrypoint=")
	})

	t.Run("NoNewPrivileges emits boolean directive", func(t *testing.T) {
		out := RenderContainerUnit(ContainerUnit{
			Container: ContainerSection{NoNewPrivileges: true},
		})
		require.Contains(t, out, "NoNewPrivileges=true")
	})

	t.Run("NoNewPrivileges=false emits nothing", func(t *testing.T) {
		out := RenderContainerUnit(ContainerUnit{
			Container: ContainerSection{NoNewPrivileges: false},
		})
		require.NotContains(t, out, "NoNewPrivileges")
	})

	t.Run("Unit section with After and Requires", func(t *testing.T) {
		out := RenderContainerUnit(ContainerUnit{
			Unit: UnitSection{
				Description: "my description",
				After:       []string{"foo.service", "bar.service"},
				Requires:    []string{"foo.service"},
			},
			Container: ContainerSection{Image: "img"},
		})
		require.Contains(t, out, "[Unit]")
		require.Contains(t, out, "Description=my description")
		require.Equal(t, 2, strings.Count(out, "After="))
		require.Equal(t, 1, strings.Count(out, "Requires="))
	})

	t.Run("Service section for oneshot", func(t *testing.T) {
		out := RenderContainerUnit(ContainerUnit{
			Container: ContainerSection{Image: "img"},
			Service: ServiceSection{
				Type:            "oneshot",
				RemainAfterExit: true,
			},
		})
		require.Contains(t, out, "[Service]")
		require.Contains(t, out, "Type=oneshot")
		require.Contains(t, out, "RemainAfterExit=yes")
	})

	t.Run("Install section", func(t *testing.T) {
		out := RenderContainerUnit(ContainerUnit{
			Container: ContainerSection{Image: "img"},
			Install:   InstallSection{WantedBy: []string{"default.target"}},
		})
		require.Contains(t, out, "[Install]")
		require.Contains(t, out, "WantedBy=default.target")
	})

	t.Run("EnvironmentFile directive", func(t *testing.T) {
		out := RenderContainerUnit(ContainerUnit{
			Container: ContainerSection{
				Image:            "img",
				EnvironmentFiles: []string{"myvm-compute.env"},
			},
		})
		require.Contains(t, out, "EnvironmentFile=myvm-compute.env")
	})

	t.Run("Mount directives", func(t *testing.T) {
		out := RenderContainerUnit(ContainerUnit{
			Container: ContainerSection{
				Mounts: []string{
					"type=tmpfs,dst=/var/run/kubevirt-private",
					"type=image,src=quay.io/example:latest,dst=/mnt/img,readonly",
				},
			},
		})
		require.Equal(t, 2, strings.Count(out, "Mount="))
		require.Contains(t, out, "Mount=type=tmpfs,dst=/var/run/kubevirt-private")
		require.Contains(t, out, "Mount=type=image,src=quay.io/example:latest,dst=/mnt/img,readonly")
	})
}

func TestRenderVolumeUnit(t *testing.T) {
	t.Run("minimal volume unit emits Volume section", func(t *testing.T) {
		out := RenderVolumeUnit(VolumeUnit{})
		require.Contains(t, out, "[Volume]")
		require.NotContains(t, out, "[Unit]")
	})

	t.Run("volume unit with description emits Unit section", func(t *testing.T) {
		out := RenderVolumeUnit(VolumeUnit{
			Unit: UnitSection{Description: "my disk volume"},
		})
		require.Contains(t, out, "[Unit]")
		require.Contains(t, out, "Description=my disk volume")
		require.Contains(t, out, "[Volume]")
	})

	t.Run("image-backed volume emits Driver and Image", func(t *testing.T) {
		out := RenderVolumeUnit(VolumeUnit{
			Volume: VolumeSection{
				Driver: "image",
				Image:  "myvm-disk.image",
			},
		})
		require.Contains(t, out, "Driver=image")
		require.Contains(t, out, "Image=myvm-disk.image")
	})

	t.Run("VolumeName is emitted when set", func(t *testing.T) {
		out := RenderVolumeUnit(VolumeUnit{
			Volume: VolumeSection{VolumeName: "my-custom-vol"},
		})
		require.Contains(t, out, "VolumeName=my-custom-vol")
	})

	t.Run("empty Driver and Image are omitted", func(t *testing.T) {
		out := RenderVolumeUnit(VolumeUnit{})
		require.NotContains(t, out, "Driver=")
		require.NotContains(t, out, "Image=")
		require.NotContains(t, out, "VolumeName=")
	})
}

// ── Convert ────────────────────────────────────────────────────────────────

func TestConvert_BasicCompute(t *testing.T) {
	pod := simplePod("myvm")

	files, err := Convert(pod, opts()); require.NoError(t, err)

	t.Run("emits compute container file", func(t *testing.T) {
		f := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, f.Content, "[Container]")
		require.Contains(t, f.Content, "Image=quay.io/kubevirt/virt-launcher:v1.8.0")
	})

	t.Run("a .pod unit is always generated", func(t *testing.T) {
		requireFile(t, files, "myvm.pod")
	})

	t.Run(".pod unit is wired to default.target", func(t *testing.T) {
		f := requireFile(t, files, "myvm.pod")
		require.Contains(t, f.Content, "WantedBy=default.target")
	})

	t.Run("compute container is wired to the pod service", func(t *testing.T) {
		f := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, f.Content, "WantedBy=myvm-pod.service")
		require.NotContains(t, f.Content, "WantedBy=default.target")
	})

	t.Run("Network=podman by default (bridge networking) is set on the .pod unit", func(t *testing.T) {
		f := requireFile(t, files, "myvm.pod")
		require.Contains(t, f.Content, "Network=podman")
	})

	t.Run("Network= emitted in .pod unit when explicitly set in options", func(t *testing.T) {
		o2 := opts()
		o2.Network = "host"
		files2, err := Convert(pod, o2); require.NoError(t, err)
		f2 := requireFile(t, files2, "myvm.pod")
		require.Contains(t, f2.Content, "Network=host")
		// not on the compute container
		c2 := requireFile(t, files2, "myvm-compute.container")
		require.NotContains(t, c2.Content, "Network=host")
	})

	t.Run("compute env file is generated when env vars are present", func(t *testing.T) {
		podWithEnv := simplePod("myvm")
		podWithEnv.Spec.Containers[0].Env = []k8sv1.EnvVar{
			// A value with special chars triggers the upstream to write an env file.
			{Name: "STANDALONE_VMI", Value: `{"kind":"VirtualMachineInstance"}`},
		}
		filesWithEnv, err := Convert(podWithEnv, opts()); require.NoError(t, err)
		requireFile(t, filesWithEnv, "myvm-compute.env")
	})

	t.Run("compute container references its env file when env vars exist", func(t *testing.T) {
		podWithEnv := simplePod("myvm")
		podWithEnv.Spec.Containers[0].Env = []k8sv1.EnvVar{
			{Name: "FOO", Value: "bar"},
		}
		filesWithEnv, err := Convert(podWithEnv, opts()); require.NoError(t, err)
		f := requireFile(t, filesWithEnv, "myvm-compute.container")
		// Simple env vars are emitted inline as Environment= — that is correct
		// Quadlet syntax and does not require an EnvironmentFile=.
		require.Contains(t, f.Content, "Environment=FOO=bar")
	})
}

func TestConvert_EnvVars(t *testing.T) {
	pod := simplePod("myvm")
	pod.Spec.Containers[0].Env = []k8sv1.EnvVar{
		{Name: "STANDALONE_VMI", Value: `{"kind":"VirtualMachineInstance"}`},
		{Name: "VIRSH_DEFAULT_CONNECT_URI", Value: "qemu+unix:///session?socket=/var/run/libvirt/virtqemud-sock"},
		{Name: "XDG_CACHE_HOME", Value: "/var/run/kubevirt-private"},
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)

	t.Run("complex env vars (special chars) are placed in the .env file by upstream", func(t *testing.T) {
		env := requireFile(t, files, "myvm-compute.env")
		require.Contains(t, env.Content, `STANDALONE_VMI={"kind":"VirtualMachineInstance"}`)
		require.Contains(t, env.Content, "VIRSH_DEFAULT_CONNECT_URI=qemu+unix:///session?socket=/var/run/libvirt/virtqemud-sock")
	})

	t.Run("STANDALONE_VMI JSON blob does not appear inline in the container unit", func(t *testing.T) {
		cu := requireFile(t, files, "myvm-compute.container")
		require.NotContains(t, cu.Content, "STANDALONE_VMI")
	})

	t.Run("simple env vars may remain inline as Environment= in the container unit", func(t *testing.T) {
		// The upstream converter keeps simple KEY=/path values inline; that is
		// valid Quadlet INI syntax.  We do not post-process them out.
		cu := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, cu.Content, "XDG_CACHE_HOME=/var/run/kubevirt-private")
	})
}

func TestConvert_PVCVolume(t *testing.T) {
	pod := simplePod("myvm")
	pod.Spec.Volumes = []k8sv1.Volume{
		{
			Name: "datadisk",
			VolumeSource: k8sv1.VolumeSource{
				PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
					ClaimName: "test-vm-data",
				},
			},
		},
	}
	pod.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
		{Name: "datadisk", MountPath: "/var/run/kubevirt-private/vmi-disks/datadisk"},
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)

	t.Run("PVC volume generates a .volume file", func(t *testing.T) {
		requireFile(t, files, "myvm-datadisk.volume")
	})

	t.Run(".volume file contains Volume section", func(t *testing.T) {
		f := requireFile(t, files, "myvm-datadisk.volume")
		require.Contains(t, f.Content, "[Volume]")
	})

	t.Run("compute container mounts the named volume", func(t *testing.T) {
		f := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, f.Content,
			"Volume=myvm-datadisk.volume:/var/run/kubevirt-private/vmi-disks/datadisk")
	})

	t.Run("readonly PVC mount appends :ro", func(t *testing.T) {
		podRO := simplePod("ro")
		podRO.Spec.Volumes = []k8sv1.Volume{
			{Name: "rodisk", VolumeSource: k8sv1.VolumeSource{
				PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{ClaimName: "ro-pvc"},
			}},
		}
		podRO.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
			{Name: "rodisk", MountPath: "/mnt/ro", ReadOnly: true},
		}
		files2, err := Convert(podRO, opts()); require.NoError(t, err)
		f := requireFile(t, files2, "ro-compute.container")
		require.Contains(t, f.Content, "Volume=ro-rodisk.volume:/mnt/ro:ro")
	})
}

func TestConvert_HostPathVolume(t *testing.T) {
	t.Run("hostPath under /dev becomes AddDevice", func(t *testing.T) {
		pod := simplePod("myvm")
		pod.Spec.Volumes = []k8sv1.Volume{
			{Name: "kvm", VolumeSource: k8sv1.VolumeSource{
				HostPath: &k8sv1.HostPathVolumeSource{Path: "/dev/kvm"},
			}},
		}
		pod.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
			{Name: "kvm", MountPath: "/dev/kvm"},
		}

		files, err := Convert(pod, opts()); require.NoError(t, err)
		f := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, f.Content, "AddDevice=/dev/kvm")
		require.NotContains(t, f.Content, "Volume=/dev/kvm")
	})

	t.Run("hostPath outside /dev becomes a bind Volume", func(t *testing.T) {
		pod := simplePod("myvm")
		pod.Spec.Volumes = []k8sv1.Volume{
			{Name: "cgroup", VolumeSource: k8sv1.VolumeSource{
				HostPath: &k8sv1.HostPathVolumeSource{Path: "/sys/fs/cgroup"},
			}},
		}
		pod.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
			{Name: "cgroup", MountPath: "/sys/fs/cgroup", ReadOnly: true},
		}

		files, err := Convert(pod, opts()); require.NoError(t, err)
		f := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, f.Content, "Volume=/sys/fs/cgroup:/sys/fs/cgroup:ro")
		require.NotContains(t, f.Content, "AddDevice=")
	})

	t.Run("hostPath bind mount gets :z for SELinux relabeling", func(t *testing.T) {
		pod := simplePod("myvm")
		pod.Spec.Volumes = []k8sv1.Volume{
			{Name: "hostdir", VolumeSource: k8sv1.VolumeSource{
				HostPath: &k8sv1.HostPathVolumeSource{Path: "/data"},
			}},
		}
		pod.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
			{Name: "hostdir", MountPath: "/mnt/data"},
		}
		files, err := Convert(pod, opts()); require.NoError(t, err)
		f := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, f.Content, "Volume=/data:/mnt/data:z\n")
	})

	t.Run("hostPath read-only bind mount gets :z,ro", func(t *testing.T) {
		pod := simplePod("myvm")
		pod.Spec.Volumes = []k8sv1.Volume{
			{Name: "hostdir", VolumeSource: k8sv1.VolumeSource{
				HostPath: &k8sv1.HostPathVolumeSource{Path: "/data"},
			}},
		}
		pod.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
			{Name: "hostdir", MountPath: "/mnt/data", ReadOnly: true},
		}
		files, err := Convert(pod, opts()); require.NoError(t, err)
		f := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, f.Content, "Volume=/data:/mnt/data:z,ro\n")
	})

	t.Run("kernel-managed /sys paths do not get :z", func(t *testing.T) {
		pod := simplePod("myvm")
		pod.Spec.Volumes = []k8sv1.Volume{
			{Name: "cgroup", VolumeSource: k8sv1.VolumeSource{
				HostPath: &k8sv1.HostPathVolumeSource{Path: "/sys/fs/cgroup"},
			}},
		}
		pod.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
			{Name: "cgroup", MountPath: "/sys/fs/cgroup", ReadOnly: true},
		}
		files, err := Convert(pod, opts()); require.NoError(t, err)
		f := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, f.Content, "Volume=/sys/fs/cgroup:/sys/fs/cgroup:ro\n")
		require.NotContains(t, f.Content, ":z")
	})
}

func TestConvert_EmptyDirVolume(t *testing.T) {
	// Default-medium emptyDir (no Memory override) must generate a shared
	// named volume, not a per-container tmpfs mount.  The preprocess step
	// no longer forces Memory medium, so default emptyDir flows through
	// unchanged and becomes a Podman anonymous named volume.
	pod := simplePod("myvm")
	pod.Spec.Volumes = []k8sv1.Volume{
		{Name: "private", VolumeSource: k8sv1.VolumeSource{
			EmptyDir: &k8sv1.EmptyDirVolumeSource{},
		}},
	}
	pod.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
		{Name: "private", MountPath: "/var/run/kubevirt-private"},
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)
	f := requireFile(t, files, "myvm-compute.container")

	t.Run("emptyDir becomes a shared named volume (not a per-container tmpfs)", func(t *testing.T) {
		require.Contains(t, f.Content, "Volume=myvm-private-empty.volume:/var/run/kubevirt-private")
		require.NotContains(t, f.Content, "Mount=type=tmpfs")
	})

	t.Run("default-medium emptyDir produces a companion .volume file", func(t *testing.T) {
		// podman kube quadlet emits a named tmpfs .volume unit for all emptyDir
		// volumes (regardless of medium) so the volume is shared across all
		// containers in the pod with consistent options.
		vf := requireFile(t, files, "myvm-private-empty.volume")
		require.Contains(t, vf.Content, "Type=tmpfs")
	})
}

func TestConvert_EmptyDirVolume_MemoryMedium(t *testing.T) {
	// Memory-medium emptyDir must produce a named volume backed by tmpfs
	// (via a companion .volume file), not a per-container Mount=type=tmpfs.
	// Using a named volume ensures the tmpfs is shared across all containers
	// in the pod that mount this emptyDir — matching Kubernetes semantics.
	pod := simplePod("myvm")
	pod.Spec.Volumes = []k8sv1.Volume{
		{Name: "private", VolumeSource: k8sv1.VolumeSource{
			EmptyDir: &k8sv1.EmptyDirVolumeSource{Medium: k8sv1.StorageMediumMemory},
		}},
	}
	pod.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
		{Name: "private", MountPath: "/var/run/kubevirt-private"},
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)
	f := requireFile(t, files, "myvm-compute.container")

	t.Run("container unit uses named volume, not per-container tmpfs", func(t *testing.T) {
		require.Contains(t, f.Content, "Volume=myvm-private-empty.volume:/var/run/kubevirt-private")
		require.NotContains(t, f.Content, "Mount=type=tmpfs")
	})

	t.Run("companion .volume file is generated with tmpfs options", func(t *testing.T) {
		vf := requireFile(t, files, "myvm-private-empty.volume")
		require.Contains(t, vf.Content, "Type=tmpfs")
	})
}

func TestConvert_EmptyDirVolume_SharedAcrossSidecar(t *testing.T) {
	// A sidecar that mounts the same emptyDir as the compute container must
	// reference the identical named volume so both containers can read and
	// write shared state (e.g. the VNC Unix socket written by compute and
	// read by socat sidecar).
	pod := simplePod("myvm")
	pod.Spec.Volumes = []k8sv1.Volume{
		{Name: "private", VolumeSource: k8sv1.VolumeSource{
			EmptyDir: &k8sv1.EmptyDirVolumeSource{},
		}},
	}
	pod.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
		{Name: "private", MountPath: "/var/run/kubevirt-private"},
	}
	pod.Spec.Containers = append(pod.Spec.Containers, k8sv1.Container{
		Name:  "vnc-proxy",
		Image: "docker.io/alpine/socat:latest",
		VolumeMounts: []k8sv1.VolumeMount{
			{Name: "private", MountPath: "/var/run/kubevirt-private"},
		},
	})

	files, err := Convert(pod, opts()); require.NoError(t, err)

	t.Run("both containers reference the same named volume", func(t *testing.T) {
		compute := requireFile(t, files, "myvm-compute.container")
		sidecar := requireFile(t, files, "myvm-vnc-proxy.container")
		require.Contains(t, compute.Content, "Volume=myvm-private-empty.volume:/var/run/kubevirt-private")
		require.Contains(t, sidecar.Content, "Volume=myvm-private-empty.volume:/var/run/kubevirt-private")
	})

	t.Run("no --volumes-from pattern in any unit", func(t *testing.T) {
		for _, f := range files {
			require.NotContains(t, f.Content, "--volumes-from",
				"shared emptyDir must not require --volumes-from; use named volume instead")
		}
	})
}

func TestConvert_ImageVolume(t *testing.T) {
	pod := simplePod("myvm")
	pod.Spec.Volumes = []k8sv1.Volume{
		{Name: "import-src-osdisk", VolumeSource: k8sv1.VolumeSource{
			Image: &k8sv1.ImageVolumeSource{
				Reference:  "quay.io/containerdisks/fedora:40",
				PullPolicy: k8sv1.PullIfNotPresent,
			},
		}},
	}
	pod.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
		{Name: "import-src-osdisk", MountPath: "/var/run/import-source/osdisk", ReadOnly: true},
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)
	f := requireFile(t, files, "myvm-compute.container")

	t.Run("no separate .volume or .image unit is generated", func(t *testing.T) {
		requireNoFile(t, files, "myvm-import-src-osdisk.volume")
		requireNoFile(t, files, "myvm-import-src-osdisk.image")
	})

	t.Run("read-only image mount has no rw option (images are ro by default)", func(t *testing.T) {
		// type=image is read-only by default; no ",ro" or ",rw" should appear
		require.Contains(t, f.Content,
			"Mount=type=image,source=quay.io/containerdisks/fedora:40,dst=/var/run/import-source/osdisk\n")
		require.NotContains(t, f.Content, ",rw")
		require.NotContains(t, f.Content, ",ro")
		require.NotContains(t, f.Content, "Volume=myvm-import-src-osdisk")
	})

	t.Run("no image service dependencies in container unit", func(t *testing.T) {
		require.NotContains(t, f.Content, "-image.service")
	})

	t.Run("read-write image volume mount omits the ro suffix", func(t *testing.T) {
		pod2 := simplePod("myvm")
		pod2.Spec.Volumes = []k8sv1.Volume{
			{Name: "disk0", VolumeSource: k8sv1.VolumeSource{
				Image: &k8sv1.ImageVolumeSource{Reference: "quay.io/test/disk:latest"},
			}},
		}
		pod2.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
			{Name: "disk0", MountPath: "/mnt/disk", ReadOnly: false},
		}
		files2, err := Convert(pod2, opts()); require.NoError(t, err)
		f2 := requireFile(t, files2, "myvm-compute.container")
		// ReadOnly=false → mount is writable, so ",rw" is appended
		require.Contains(t, f2.Content, "Mount=type=image,source=quay.io/test/disk:latest,dst=/mnt/disk,rw\n")
		require.NotContains(t, f2.Content, ",ro")
	})
}

func TestConvert_SecurityContext(t *testing.T) {
	falseVal := false
	uid := int64(107)

	pod := simplePod("myvm")
	pod.Spec.Containers[0].SecurityContext = &k8sv1.SecurityContext{
		Capabilities: &k8sv1.Capabilities{
			Add:  []k8sv1.Capability{"NET_BIND_SERVICE"},
			Drop: []k8sv1.Capability{"ALL"},
		},
		AllowPrivilegeEscalation: &falseVal,
		RunAsUser:                &uid,
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)
	f := requireFile(t, files, "myvm-compute.container")

	t.Run("capability add", func(t *testing.T) {
		require.Contains(t, f.Content, "AddCapability=NET_BIND_SERVICE")
	})

	t.Run("capability drop", func(t *testing.T) {
		require.Contains(t, f.Content, "DropCapability=ALL")
	})

	t.Run("AllowPrivilegeEscalation=false maps to NoNewPrivileges", func(t *testing.T) {
		require.Contains(t, f.Content, "NoNewPrivileges=true")
	})

	t.Run("RunAsUser maps to User", func(t *testing.T) {
		require.Contains(t, f.Content, "User=107")
	})
}

func TestConvert_PodLevelSysctls(t *testing.T) {
	pod := simplePod("myvm")
	pod.Spec.SecurityContext = &k8sv1.PodSecurityContext{
		Sysctls: []k8sv1.Sysctl{
			{Name: "net.ipv4.ip_unprivileged_port_start", Value: "0"},
		},
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)

	t.Run("pod sysctl appears in the .pod unit (not the container)", func(t *testing.T) {
		f := requireFile(t, files, "myvm.pod")
		require.Contains(t, f.Content, "--sysctl net.ipv4.ip_unprivileged_port_start=0")
		c := requireFile(t, files, "myvm-compute.container")
		require.NotContains(t, c.Content, "Sysctl=")
	})
}

func TestConvert_Ports(t *testing.T) {
	pod := simplePod("myvm")
	pod.Spec.Containers[0].Ports = []k8sv1.ContainerPort{
		{ContainerPort: 22, Protocol: k8sv1.ProtocolTCP},
		{ContainerPort: 8080, Protocol: k8sv1.ProtocolTCP},
		{ContainerPort: 53, Protocol: k8sv1.ProtocolUDP},
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)
	f := requireFile(t, files, "myvm-compute.container")

	// Ports are not published in the generated unit — users add drop-in files
	// (e.g. virt-launcher-myvm-compute.container.d/ports.conf) to publish ports.
	t.Run("ports from pod spec are not emitted as PublishPort directives", func(t *testing.T) {
		// The upstream emits port info as comments; ensure no actual directive.
		for _, line := range strings.Split(f.Content, "\n") {
			trimmed := strings.TrimSpace(line)
			require.False(t, strings.HasPrefix(trimmed, "PublishPort="),
				"unexpected PublishPort= directive: %s", line)
		}
	})
}

func TestConvert_MemoryLimit(t *testing.T) {
	pod := simplePod("myvm")
	pod.Spec.Containers[0].Resources = k8sv1.ResourceRequirements{
		Limits: k8sv1.ResourceList{
			k8sv1.ResourceMemory: resource.MustParse("2Gi"),
		},
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)
	f := requireFile(t, files, "myvm-compute.container")

	t.Run("memory limit maps to Memory= in bytes (not MemoryLimit=)", func(t *testing.T) {
		require.Contains(t, f.Content, "Memory=2147483648")
		require.NotContains(t, f.Content, "MemoryLimit=")
	})
}

func TestConvert_RestartPolicy(t *testing.T) {
	t.Run("RestartPolicyOnFailure maps to Restart=on-failure", func(t *testing.T) {
		pod := simplePod("myvm")
		pod.Spec.RestartPolicy = k8sv1.RestartPolicyOnFailure
		files, err := Convert(pod, opts()); require.NoError(t, err)
		f := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, f.Content, "Restart=on-failure")
	})

	t.Run("RestartPolicyNever produces no Restart directive", func(t *testing.T) {
		pod := simplePod("myvm")
		pod.Spec.RestartPolicy = k8sv1.RestartPolicyNever
		files, err := Convert(pod, opts()); require.NoError(t, err)
		f := requireFile(t, files, "myvm-compute.container")
		require.NotContains(t, f.Content, "Restart=")
	})
}

func TestConvert_InitContainers(t *testing.T) {
	pod := simplePod("myvm")
	pod.Spec.Volumes = []k8sv1.Volume{
		{Name: "datadisk", VolumeSource: k8sv1.VolumeSource{
			PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{ClaimName: "myvm-data"},
		}},
	}
	script := `DISK="/var/run/kubevirt-private/vmi-disks/datadisk/disk.img"
if [ ! -f "$DISK" ]; then qemu-img create -f raw "$DISK" 10737418240; fi`
	pod.Spec.InitContainers = []k8sv1.Container{
		{
			Name:    "init-disk-datadisk",
			Image:   "quay.io/kubevirt/virt-launcher:v1.8.0",
			Command: []string{"/bin/sh", "-c"},
			Args:    []string{script},
			VolumeMounts: []k8sv1.VolumeMount{
				{Name: "datadisk", MountPath: "/var/run/kubevirt-private/vmi-disks/datadisk"},
			},
		},
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)

	t.Run("init container produces its own .container file", func(t *testing.T) {
		requireFile(t, files, "myvm-init-disk-datadisk.container")
	})

	t.Run("init container unit is oneshot with RemainAfterExit", func(t *testing.T) {
		f := requireFile(t, files, "myvm-init-disk-datadisk.container")
		require.Contains(t, f.Content, "Type=oneshot")
		require.Contains(t, f.Content, "RemainAfterExit=yes")
	})

	t.Run("init container is wanted by compute service", func(t *testing.T) {
		f := requireFile(t, files, "myvm-init-disk-datadisk.container")
		require.Contains(t, f.Content, "WantedBy=myvm-compute.service")
	})

	t.Run("shell script is written as a .sh file", func(t *testing.T) {
		f := requireFile(t, files, "myvm-init-disk-datadisk.sh")
		require.Equal(t, script, f.Content)
	})

	t.Run("init container overrides ENTRYPOINT and sets Exec=/init.sh", func(t *testing.T) {
		f := requireFile(t, files, "myvm-init-disk-datadisk.container")
		// Entrypoint= overrides the image's built-in entrypoint (e.g. virt-launcher)
		// so that Exec=/init.sh is run directly as a command, not as its arguments.
		require.Contains(t, f.Content, "Entrypoint=/bin/sh")
		require.Contains(t, f.Content, "Exec=/init.sh")
		require.NotContains(t, f.Content, "base64")
		require.NotContains(t, f.Content, "_INIT_SCRIPT")
	})

	t.Run("script is bind-mounted using a relative path (self-contained)", func(t *testing.T) {
		f := requireFile(t, files, "myvm-init-disk-datadisk.container")
		require.Contains(t, f.Content,
			"Volume=./myvm-init-disk-datadisk.sh:/init.sh:ro,z")
	})

	t.Run("init container mounts PVC volume", func(t *testing.T) {
		f := requireFile(t, files, "myvm-init-disk-datadisk.container")
		require.Contains(t, f.Content,
			"Volume=myvm-datadisk.volume:/var/run/kubevirt-private/vmi-disks/datadisk")
	})

	t.Run("compute container depends on init service", func(t *testing.T) {
		f := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, f.Content, "After=myvm-init-disk-datadisk.service")
		require.Contains(t, f.Content, "Requires=myvm-init-disk-datadisk.service")
	})

	t.Run("no After/Requires on compute when there are no init containers", func(t *testing.T) {
		pod2 := simplePod("clean")
		files2, err := Convert(pod2, opts()); require.NoError(t, err)
		f := requireFile(t, files2, "clean-compute.container")
		require.NotContains(t, f.Content, "After=")
		require.NotContains(t, f.Content, "Requires=")
	})

}

func TestConvert_MultipleInitContainers(t *testing.T) {
	pod := simplePod("myvm")
	pod.Spec.Volumes = []k8sv1.Volume{
		{Name: "osdisk", VolumeSource: k8sv1.VolumeSource{
			PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{ClaimName: "os-pvc"},
		}},
		{Name: "datadisk", VolumeSource: k8sv1.VolumeSource{
			PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{ClaimName: "data-pvc"},
		}},
	}
	pod.Spec.InitContainers = []k8sv1.Container{
		{Name: "import-disk-osdisk", Image: "quay.io/kubevirt/virt-launcher:v1.8.0",
			Command: []string{"/bin/sh", "-c"}, Args: []string{"echo import"}},
		{Name: "init-disk-datadisk", Image: "quay.io/kubevirt/virt-launcher:v1.8.0",
			Command: []string{"/bin/sh", "-c"}, Args: []string{"echo init"}},
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)

	t.Run("two init containers produce two .container files", func(t *testing.T) {
		requireFile(t, files, "myvm-import-disk-osdisk.container")
		requireFile(t, files, "myvm-init-disk-datadisk.container")
	})

	t.Run("compute After lists both init services", func(t *testing.T) {
		f := requireFile(t, files, "myvm-compute.container")
		require.Contains(t, f.Content, "After=myvm-import-disk-osdisk.service")
		require.Contains(t, f.Content, "After=myvm-init-disk-datadisk.service")
	})
}

func TestConvert_SidecarContainer(t *testing.T) {
	pod := simplePod("myvm")
	pod.Spec.Containers = append(pod.Spec.Containers, k8sv1.Container{
		Name:    "some-sidecar",
		Image:   "quay.io/example/sidecar:latest",
		Command: []string{"/sidecar", "--port=9090"},
	})

	files, err := Convert(pod, opts()); require.NoError(t, err)

	t.Run("sidecar gets its own .container file", func(t *testing.T) {
		requireFile(t, files, "myvm-some-sidecar.container")
	})

	t.Run("sidecar does not get an env file", func(t *testing.T) {
		requireNoFile(t, files, "myvm-some-sidecar.env")
	})

	t.Run("sidecar joins the pod (shares network and IPC namespace)", func(t *testing.T) {
		f := requireFile(t, files, "myvm-some-sidecar.container")
		require.Contains(t, f.Content, "Pod=myvm.pod")
		require.NotContains(t, f.Content, "Network=container:")
	})

	t.Run("sidecar is started by the pod service (no explicit compute dependency)", func(t *testing.T) {
		f := requireFile(t, files, "myvm-some-sidecar.container")
		require.Contains(t, f.Content, "WantedBy=myvm-pod.service")
		require.NotContains(t, f.Content, "After=myvm-compute.service")
		require.NotContains(t, f.Content, "Requires=myvm-compute.service")
	})
}

func TestConvert_FileOrdering(t *testing.T) {
	pod := simplePod("myvm")
	pod.Spec.Volumes = []k8sv1.Volume{
		{Name: "disk", VolumeSource: k8sv1.VolumeSource{
			PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{ClaimName: "disk-pvc"},
		}},
	}
	pod.Spec.InitContainers = []k8sv1.Container{
		{Name: "init-disk-disk", Image: "quay.io/kubevirt/virt-launcher:v1.8.0",
			Command: []string{"/bin/sh", "-c"}, Args: []string{"echo"}},
	}
	// Add env var with special chars so an env file is generated for the ordering test.
	pod.Spec.Containers[0].Env = []k8sv1.EnvVar{
		{Name: "STANDALONE_VMI", Value: `{"kind":"VirtualMachineInstance"}`},
	}

	files, err := Convert(pod, opts()); require.NoError(t, err)

	t.Run("volume files come before init container files", func(t *testing.T) {
		volIdx := -1
		initIdx := -1
		for i, f := range files {
			if strings.HasSuffix(f.Name, ".volume") {
				volIdx = i
			}
			if f.Name == "myvm-init-disk-disk.container" {
				initIdx = i
			}
		}
		require.Less(t, volIdx, initIdx, ".volume files should appear before init .container files")
	})

	t.Run("image-backed volume produces no .volume unit; image is inlined as Mount=type=image", func(t *testing.T) {
		imgPod := simplePod("myvm")
		imgPod.Spec.Volumes = []k8sv1.Volume{
			{Name: "containerdisk", VolumeSource: k8sv1.VolumeSource{
				Image: &k8sv1.ImageVolumeSource{Reference: "quay.io/containerdisks/fedora:40"},
			}},
		}
		imgPod.Spec.InitContainers = []k8sv1.Container{
			{Name: "init-disk", Image: "quay.io/kubevirt/virt-launcher:v1.8.0",
				Command: []string{"/bin/ls"}},
		}
		imgPod.Spec.Containers[0].VolumeMounts = []k8sv1.VolumeMount{
			{Name: "containerdisk", MountPath: "/mnt/disk", ReadOnly: true},
		}
		imgFiles, err := Convert(imgPod, opts()); require.NoError(t, err)
		// No .volume or .image files for image-backed volumes
		for _, f := range imgFiles {
			require.False(t, strings.HasSuffix(f.Name, ".volume") && strings.Contains(f.Name, "containerdisk"),
				"unexpected .volume file for image-backed volume: %s", f.Name)
			require.False(t, strings.HasSuffix(f.Name, ".image"),
				"unexpected .image file: %s", f.Name)
		}
		// The container unit should have a Mount=type=image line
		computeFile := requireFile(t, imgFiles, "myvm-compute.container")
		// ReadOnly=true → no ",rw" appended (image mounts are ro by default)
		require.Contains(t, computeFile.Content,
			"Mount=type=image,source=quay.io/containerdisks/fedora:40,dst=/mnt/disk\n")
	})

	t.Run("env file comes after compute container file", func(t *testing.T) {
		computeIdx := -1
		envIdx := -1
		for i, f := range files {
			if f.Name == "myvm-compute.container" {
				computeIdx = i
			}
			if f.Name == "myvm-compute.env" {
				envIdx = i
			}
		}
		require.Less(t, computeIdx, envIdx, "compute .container file should appear before .env file")
	})
}

func TestConvert_ExecMapping(t *testing.T) {
	t.Run("regular virt-launcher command: binary goes into Entrypoint=, args go into Exec=", func(t *testing.T) {
		pod := simplePod("myvm")
		pod.Spec.Containers[0].Command = []string{
			"/usr/bin/virt-launcher-monitor",
			"--qemu-timeout", "248s",
			"--name", "myvm",
			"--namespace", "default",
		}
		files, err := Convert(pod, opts()); require.NoError(t, err)
		f := requireFile(t, files, "myvm-compute.container")
		// The upstream exec.go fix: command[0] → Entrypoint= (the binary path),
		// command[1:] → Exec= (the arguments).  This matches Quadlet semantics
		// where Entrypoint= sets the executable and Exec= provides its arguments.
		require.Contains(t, f.Content, "Entrypoint=/usr/bin/virt-launcher-monitor")
		require.Contains(t, f.Content, "Exec=--qemu-timeout 248s --name myvm --namespace default")
	})

	t.Run("container with no command omits Entrypoint and Exec directives", func(t *testing.T) {
		pod := simplePod("myvm")
		files, err := Convert(pod, opts()); require.NoError(t, err)
		f := requireFile(t, files, "myvm-compute.container")
		require.NotContains(t, f.Content, "Entrypoint=")
		require.NotContains(t, f.Content, "Exec=")
	})
}
