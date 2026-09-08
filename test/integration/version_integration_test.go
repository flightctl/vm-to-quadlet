//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var _ = Describe("Version command integration", func() {
	It("prints version info with the real git commit baked in by the Containerfile", func(ctx context.Context) {
		// Determine the expected short commit hash from the current working tree
		// so the assertion stays correct regardless of which commit is checked out.
		commitOut, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
		Expect(err).NotTo(HaveOccurred(), "git rev-parse --short HEAD must succeed")
		expectedCommit := strings.TrimSpace(string(commitOut))
		Expect(expectedCommit).NotTo(BeEmpty())

		By("building a container image from the repo Containerfile")
		ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				FromDockerfile: testcontainers.FromDockerfile{
					Context:    "../../",
					Dockerfile: "Containerfile",
				},
				Cmd:        []string{"version"},
				WaitingFor: wait.ForExit().WithExitTimeout(2 * time.Minute),
			},
			Started: true,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			if ctr != nil {
				_ = ctr.Terminate(context.Background())
			}
		})

		By("reading the container's stdout")
		logs, err := ctr.Logs(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer logs.Close()

		var buf bytes.Buffer
		_, err = io.Copy(&buf, logs)
		Expect(err).NotTo(HaveOccurred())
		output := buf.String()

		GinkgoWriter.Printf("--- version output ---\n%s--- end ---\n", output)

		By("asserting the output contains the expected version string")
		Expect(output).To(ContainSubstring("vm-to-quadlet version"),
			"output must contain 'vm-to-quadlet version'")

		By("asserting the output contains the real git commit hash")
		Expect(output).To(ContainSubstring(expectedCommit),
			"output must contain the short commit hash %q", expectedCommit)

		By("asserting the commit is not the 'unknown' placeholder")
		Expect(output).NotTo(ContainSubstring("commit unknown"),
			"commit must not be 'unknown' — ldflags injection failed")
	})
})
