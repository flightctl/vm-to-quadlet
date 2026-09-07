//go:build integration

package integration_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/flightctl/vm-to-quadlet/pkg/quadlet"
)

// podmanVersion pairs a quay.io/podman/stable image tag with a human-readable
// description used in test names and log output.
type podmanVersion struct {
	tag         string // image tag, e.g. "v5.6.0"
	description string // e.g. "RHEL 9.7 equivalent"
}

// targetVersions lists the Podman versions every generated Quadlet file must
// validate against. Add new entries when a new Podman release introduces
// Quadlet syntax changes.
var targetVersions = []podmanVersion{
	// Oldest available tag on quay.io/podman/stable.
	// StopTimeout=/ExitPolicy=/HostName=/Memory= in [Pod]/[Container] are
	// NOT supported — validates the PodmanArgs fix.
	{tag: "v5.3.0", description: "oldest available"},

	// RHEL 9.7 ships Podman 5.6.0. This is the version from the EDM-5571 bug.
	// The unsupported keys are NOT supported until 5.7.0.
	{tag: "v5.6.0", description: "RHEL 9.7"},

	// Latest stable release. Ensures generated files remain valid as Podman
	// evolves (both PodmanArgs and the newer native keys are accepted).
	{tag: "latest", description: "latest stable"},
}

// quadletBinCandidates lists the paths where the quadlet generator binary may
// be installed. The first one found is used.
var quadletBinCandidates = []string{
	"/usr/libexec/podman/quadlet",
	"/usr/lib/podman/quadlet",
}

// productionVMPod builds a Pod spec that exercises all the Quadlet keys a real
// production KubeVirt VM generates.  The spec mirrors the output of the full
// vm-to-quadlet pipeline for a Fedora 41 VM with:
//   - compute container (virt-launcher) with health checks, capabilities,
//     volumes, image mounts, device mounts, env vars, memory reservation
//   - volumecontainerdisk init container with memory limit, CPU quota,
//     image mount
//   - virt-handler-dir-init init container with PVC volume
//   - multiple emptyDir volumes (tmpfs-backed), a PVC volume, host paths
//   - pod-level hostname, sysctls, published ports
func productionVMPod() *k8sv1.Pod {
	grace := int64(120)
	shareProc := false
	noEscalation := false
	readOnly := false
	uid := int64(107)
	gid := int64(107)

	bidirectional := k8sv1.MountPropagationBidirectional

	return &k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vm"},
		Spec: k8sv1.PodSpec{
			Hostname:                      "test-vm",
			TerminationGracePeriodSeconds: &grace,
			ShareProcessNamespace:         &shareProc,
			SecurityContext: &k8sv1.PodSecurityContext{
				Sysctls: []k8sv1.Sysctl{
					{Name: "net.ipv4.ip_unprivileged_port_start", Value: "0"},
				},
			},
			HostAliases: []k8sv1.HostAlias{
				{IP: "127.0.0.1", Hostnames: []string{"test-vm.local"}},
			},
			DNSConfig: &k8sv1.PodDNSConfig{
				Nameservers: []string{"8.8.8.8"},
				Searches:    []string{"cluster.local"},
			},

			// --- Volumes ---
			Volumes: []k8sv1.Volume{
				// emptyDir volumes → tmpfs-backed .volume units
				{Name: "private", VolumeSource: k8sv1.VolumeSource{
					EmptyDir: &k8sv1.EmptyDirVolumeSource{Medium: k8sv1.StorageMediumMemory},
				}},
				{Name: "public", VolumeSource: k8sv1.VolumeSource{
					EmptyDir: &k8sv1.EmptyDirVolumeSource{Medium: k8sv1.StorageMediumMemory},
				}},
				{Name: "sockets", VolumeSource: k8sv1.VolumeSource{
					EmptyDir: &k8sv1.EmptyDirVolumeSource{Medium: k8sv1.StorageMediumMemory},
				}},
				{Name: "ephemeral-disks", VolumeSource: k8sv1.VolumeSource{
					EmptyDir: &k8sv1.EmptyDirVolumeSource{},
				}},
				// PVC volume → named .volume unit
				{Name: "vm-state", VolumeSource: k8sv1.VolumeSource{
					PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
						ClaimName: "test-vm-state",
					},
				}},
				// hostPath volume → bind mount
				{Name: "cgroup", VolumeSource: k8sv1.VolumeSource{
					HostPath: &k8sv1.HostPathVolumeSource{Path: "/sys/fs/cgroup"},
				}},
				// hostPath → device
				{Name: "kvm", VolumeSource: k8sv1.VolumeSource{
					HostPath: &k8sv1.HostPathVolumeSource{Path: "/dev/kvm"},
				}},
				// image volume → Mount=type=image
				{Name: "containerdisk", VolumeSource: k8sv1.VolumeSource{
					Image: &k8sv1.ImageVolumeSource{
						Reference:  "quay.io/containerdisks/fedora:41",
						PullPolicy: k8sv1.PullIfNotPresent,
					},
				}},
			},

			// --- Init containers ---
			InitContainers: []k8sv1.Container{
				{
					Name:    "virt-handler-dir-init",
					Image:   "quay.io/kubevirt/virt-launcher:v1.9.0",
					Command: []string{"/bin/bash", "-c"},
					Args:    []string{`mkdir -p /emptydir/private/libvirt/qemu`},
					SecurityContext: &k8sv1.SecurityContext{
						RunAsUser:  &uid,
						RunAsGroup: &gid,
					},
					VolumeMounts: []k8sv1.VolumeMount{
						{Name: "private", MountPath: "/emptydir/private"},
						{Name: "vm-state", MountPath: "/vm-state-init"},
					},
				},
			},

			// --- Regular containers ---
			Containers: []k8sv1.Container{
				// Compute container — the main virt-launcher.
				{
					Name:  "compute",
					Image: "quay.io/kubevirt/virt-launcher:v1.9.0",
					Command: []string{
						"/usr/bin/virt-launcher-monitor",
						"--qemu-timeout", "298s",
						"--name", "test-vm",
					},
					Env: []k8sv1.EnvVar{
						{Name: "XDG_CACHE_HOME", Value: "/var/run/kubevirt-private"},
						{Name: "POD_NAME", Value: "test-vm"},
					},
					Ports: []k8sv1.ContainerPort{
						{ContainerPort: 22, HostPort: 2222, Protocol: k8sv1.ProtocolTCP},
					},
					SecurityContext: &k8sv1.SecurityContext{
						Capabilities: &k8sv1.Capabilities{
							Add:  []k8sv1.Capability{"NET_BIND_SERVICE"},
							Drop: []k8sv1.Capability{"ALL"},
						},
						AllowPrivilegeEscalation: &noEscalation,
						RunAsUser:                &uid,
						RunAsGroup:               &gid,
						ReadOnlyRootFilesystem:   &readOnly,
					},
					Resources: k8sv1.ResourceRequirements{
						Requests: k8sv1.ResourceList{
							k8sv1.ResourceMemory: resource.MustParse("1346018368"),
						},
					},
					LivenessProbe: &k8sv1.Probe{
						ProbeHandler: k8sv1.ProbeHandler{
							Exec: &k8sv1.ExecAction{
								Command: []string{"/bin/sh", "-c", `test "$(virsh domstate default_test-vm)" = "running"`},
							},
						},
						PeriodSeconds:    30,
						TimeoutSeconds:   10,
						FailureThreshold: 3,
					},
					VolumeMounts: []k8sv1.VolumeMount{
						{Name: "private", MountPath: "/var/run/kubevirt-private"},
						{Name: "public", MountPath: "/var/run/kubevirt"},
						{Name: "sockets", MountPath: "/var/run/kubevirt/sockets"},
						{Name: "ephemeral-disks", MountPath: "/var/run/kubevirt-ephemeral-disks"},
						{Name: "cgroup", MountPath: "/sys/fs/cgroup", ReadOnly: true},
						{Name: "kvm", MountPath: "/dev/kvm"},
						{Name: "containerdisk", MountPath: "/var/run/kubevirt-image-volume/disk_0", ReadOnly: true},
						{Name: "vm-state", MountPath: "/var/run/kubevirt-private/libvirt/qemu/nvram",
							SubPath: "nvram", MountPropagation: &bidirectional},
					},
				},
				// volumecontainerdisk — init-like oneshot with memory/CPU limits.
				{
					Name:    "volumecontainerdisk",
					Image:   "quay.io/containerdisks/fedora:41",
					Command: []string{"/container-disk-binary/usr/bin/container-disk", "--no-op"},
					SecurityContext: &k8sv1.SecurityContext{
						Capabilities: &k8sv1.Capabilities{
							Drop: []k8sv1.Capability{"ALL"},
						},
						AllowPrivilegeEscalation: &noEscalation,
						RunAsUser:                &uid,
						RunAsGroup:               &gid,
					},
					Resources: k8sv1.ResourceRequirements{
						Limits: k8sv1.ResourceList{
							k8sv1.ResourceMemory: resource.MustParse("40000000"),
							k8sv1.ResourceCPU:    resource.MustParse("10m"),
						},
						Requests: k8sv1.ResourceList{
							k8sv1.ResourceMemory: resource.MustParse("1000000"),
						},
					},
					VolumeMounts: []k8sv1.VolumeMount{
						// image mount from the virt-launcher image for the container-disk binary
						{Name: "containerdisk", MountPath: "/container-disk-binary", ReadOnly: true},
					},
				},
			},
		},
	}
}

var _ = Describe("Quadlet file validation across Podman versions", Ordered, func() {
	// Shared state: generated once, used by every test entry.
	var (
		quadletFiles []quadlet.UnitFile
		tmpDir       string
	)

	BeforeAll(func() {
		// Generate Quadlet files from a comprehensive production-like VM pod
		// that exercises all the Quadlet keys used in real deployments.
		pod := productionVMPod()

		var err error
		quadletFiles, err = quadlet.Convert(pod, quadlet.DefaultOptions())
		Expect(err).NotTo(HaveOccurred())
		Expect(quadletFiles).NotTo(BeEmpty(), "Convert() must produce at least one Quadlet file")

		// Sanity checks: verify EDM-5571 fixes are all in place.
		for _, f := range quadletFiles {
			if strings.HasSuffix(f.Name, ".pod") {
				Expect(f.Content).NotTo(ContainSubstring("StopTimeout="),
					"EDM-5571: .pod file %q must not contain StopTimeout=", f.Name)
				Expect(f.Content).NotTo(ContainSubstring("ExitPolicy="),
					"EDM-5571: .pod file %q must not contain ExitPolicy=", f.Name)
				Expect(f.Content).NotTo(ContainSubstring("HostName="),
					"EDM-5571: .pod file %q must not contain HostName=", f.Name)
				Expect(f.Content).To(ContainSubstring("--stop-timeout"),
					"EDM-5571: .pod file %q must use PodmanArgs=--stop-timeout", f.Name)
				Expect(f.Content).To(ContainSubstring("--exit-policy"),
					"EDM-5571: .pod file %q must use PodmanArgs=--exit-policy", f.Name)
				Expect(f.Content).To(ContainSubstring("--hostname"),
					"EDM-5571: .pod file %q must use PodmanArgs=--hostname", f.Name)
			}
			if strings.HasSuffix(f.Name, ".container") {
				Expect(f.Content).NotTo(MatchRegexp(`(?m)^Memory=`),
					"EDM-5571: .container file %q must not contain Memory= key", f.Name)
			}
			// The volumecontainerdisk container has a memory limit — assert
			// it appears as PodmanArgs=--memory= unconditionally.
			if strings.Contains(f.Name, "volumecontainerdisk") {
				Expect(f.Content).To(ContainSubstring("PodmanArgs=--memory=40000000"),
					"EDM-5571: volumecontainerdisk file %q must use PodmanArgs=--memory=40000000", f.Name)
			}
		}

		// Write generated files to a temp directory for copying into containers.
		tmpDir, err = os.MkdirTemp("", "quadlet-integration-*")
		Expect(err).NotTo(HaveOccurred())

		GinkgoWriter.Println("Generated Quadlet files:")
		for _, f := range quadletFiles {
			GinkgoWriter.Printf("  %s (%d bytes)\n", f.Name, len(f.Content))
			err = os.WriteFile(filepath.Join(tmpDir, f.Name), []byte(f.Content), 0o644)
			Expect(err).NotTo(HaveOccurred())
		}
	})

	AfterAll(func() {
		if tmpDir != "" {
			os.RemoveAll(tmpDir)
		}
	})

	for _, pv := range targetVersions {
		pv := pv // capture range variable for closure

		It("validates with Podman "+pv.tag+" ("+pv.description+")", func(ctx context.Context) {
			image := "quay.io/podman/stable:" + pv.tag

			By("starting a container with " + image)
			ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
				ContainerRequest: testcontainers.ContainerRequest{
					Image:      image,
					Cmd:        []string{"sleep", "infinity"},
					WaitingFor: wait.ForExec([]string{"true"}).WithStartupTimeout(60 * time.Second),
				},
				Started: true,
			})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				if ctr != nil {
					_ = ctr.Terminate(context.Background())
				}
			})

			By("creating the Quadlet directory inside the container")
			exitCode, _, err := ctr.Exec(ctx, []string{"mkdir", "-p", "/etc/containers/systemd"})
			Expect(err).NotTo(HaveOccurred())
			Expect(exitCode).To(Equal(0))

			By("copying generated Quadlet files into the container")
			for _, f := range quadletFiles {
				localPath := filepath.Join(tmpDir, f.Name)
				containerPath := "/etc/containers/systemd/" + f.Name
				err = ctr.CopyFileToContainer(ctx, localPath, containerPath, 0o644)
				Expect(err).NotTo(HaveOccurred())
			}

			By("locating the quadlet generator binary")
			quadletBin := findQuadletBin(ctx, ctr)
			Expect(quadletBin).NotTo(BeEmpty(),
				"quadlet binary not found at any of %v", quadletBinCandidates)

			By("running " + quadletBin + " --dryrun to validate the generated files")
			exitCode, reader, err := ctr.Exec(ctx,
				[]string{quadletBin, "--dryrun"},
				tcexec.Multiplexed())
			Expect(err).NotTo(HaveOccurred())

			output, err := io.ReadAll(reader)
			Expect(err).NotTo(HaveOccurred())
			outputStr := string(output)

			GinkgoWriter.Printf("--- Podman %s quadlet --dryrun output ---\n%s--- end ---\n",
				pv.tag, outputStr)

			By("checking the quadlet generator did not report unsupported keys")
			Expect(outputStr).NotTo(ContainSubstring("unsupported key"),
				"quadlet generator reported unsupported key(s) — see output above")

			By("checking the quadlet generator exited successfully")
			Expect(exitCode).To(Equal(0),
				"quadlet --dryrun failed with exit code %d — see output above", exitCode)
		})
	}
})

// findQuadletBin probes the container for the quadlet generator binary and
// returns the first path that exists and is executable. Returns "" if none
// of the candidate paths are found.
func findQuadletBin(ctx context.Context, ctr testcontainers.Container) string {
	for _, candidate := range quadletBinCandidates {
		ec, _, err := ctr.Exec(ctx, []string{"test", "-x", candidate})
		if err == nil && ec == 0 {
			return candidate
		}
	}
	return ""
}
