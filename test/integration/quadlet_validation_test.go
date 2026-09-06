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
	// StopTimeout= in [Pod] is NOT supported — validates the PodmanArgs fix.
	{tag: "v5.3.0", description: "oldest available"},

	// RHEL 9.7 ships Podman 5.6.0. This is the version from the EDM-5571 bug.
	// StopTimeout= in [Pod] is NOT supported until 5.7.0.
	{tag: "v5.6.0", description: "RHEL 9.7"},

	// Latest stable release. Ensures generated files remain valid as Podman
	// evolves (both PodmanArgs and the newer StopTimeout= are accepted).
	{tag: "latest", description: "latest stable"},
}

// quadletBinCandidates lists the paths where the quadlet generator binary may
// be installed. The first one found is used.
var quadletBinCandidates = []string{
	"/usr/libexec/podman/quadlet",
	"/usr/lib/podman/quadlet",
}

var _ = Describe("Quadlet file validation across Podman versions", Ordered, func() {
	// Shared state: generated once, used by every test entry.
	var (
		quadletFiles []quadlet.UnitFile
		tmpDir       string
	)

	BeforeAll(func() {
		// Generate Quadlet files from a sample pod spec that exercises the
		// TerminationGracePeriodSeconds → stop-timeout code path (EDM-5571).
		//
		// The converter's preConvertFixups() always sets
		// TerminationGracePeriodSeconds=120 regardless of input, so every
		// generated .pod file will contain a stop-timeout directive.
		grace := int64(120)
		pod := &k8sv1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-vm"},
			Spec: k8sv1.PodSpec{
				TerminationGracePeriodSeconds: &grace,
				Containers: []k8sv1.Container{
					{
						Name:  "compute",
						Image: "quay.io/kubevirt/virt-launcher:v1.8.0",
					},
				},
			},
		}

		var err error
		quadletFiles, err = quadlet.Convert(pod, quadlet.DefaultOptions())
		Expect(err).NotTo(HaveOccurred())
		Expect(quadletFiles).NotTo(BeEmpty(), "Convert() must produce at least one Quadlet file")

		// Sanity check: verify the EDM-5571 fix is in place.
		for _, f := range quadletFiles {
			if strings.HasSuffix(f.Name, ".pod") {
				Expect(f.Content).NotTo(ContainSubstring("StopTimeout="),
					"EDM-5571 regression: .pod file %q must not contain StopTimeout=", f.Name)
				Expect(f.Content).To(ContainSubstring("--stop-timeout"),
					"EDM-5571: .pod file %q must use PodmanArgs=--stop-timeout", f.Name)
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
