// Vendored from github.com/containers/podman (fork at ~/dev/podman).
// Import paths rewritten from go.podman.io/podman/v6 -> github.com/flightctl/vm-to-quadlet.
// No other changes.
package quadlet

import (
	"fmt"

	v1 "github.com/flightctl/vm-to-quadlet/internal/third_party/k8s.io/api/core/v1"
	"github.com/flightctl/vm-to-quadlet/internal/third_party/systemd/parser"
	"github.com/flightctl/vm-to-quadlet/internal/third_party/systemd/quadlet"
	"github.com/flightctl/vm-to-quadlet/internal/third_party/util"
)

// applyResources maps CPU and memory resource limits/requests onto unit.
func applyResources(unit *parser.UnitFile, resources v1.ResourceRequirements) {
	applyMemoryLimit(unit, resources)
	applyMemoryRequest(unit, resources)
	applyCPULimit(unit, resources)
}

func applyMemoryLimit(unit *parser.UnitFile, resources v1.ResourceRequirements) {
	if resources.Limits == nil {
		return
	}
	mem, ok := resources.Limits[v1.ResourceMemory]
	if !ok || mem.IsZero() {
		return
	}
	// LOCAL DIVERGENCE: use PodmanArgs=--memory instead of the native
	// Memory= key.  Memory= in the [Container] group is only supported
	// from Podman 5.7.0 (Nov 2025).  RHEL 9.7 ships Podman 5.6.0, where
	// the Quadlet generator rejects the key with "unsupported key
	// 'Memory' in group 'Container'", blocking VM deployment.
	// PodmanArgs=--memory is supported on all Podman versions that
	// support Quadlet .container files.  (EDM-5571)
	// Emit raw bytes — unambiguous, no suffix needed.
	unit.Add(quadlet.ContainerGroup, quadlet.KeyPodmanArgs,
		fmt.Sprintf("--memory=%d", mem.Value()))
}

func applyMemoryRequest(unit *parser.UnitFile, resources v1.ResourceRequirements) {
	if resources.Requests == nil {
		return
	}
	mem, ok := resources.Requests[v1.ResourceMemory]
	if !ok || mem.IsZero() {
		return
	}
	unit.Add(quadlet.ContainerGroup, quadlet.KeyPodmanArgs,
		fmt.Sprintf("--memory-reservation=%d", mem.Value()))
}

func applyCPULimit(unit *parser.UnitFile, resources v1.ResourceRequirements) {
	if resources.Limits == nil {
		return
	}
	cpu, ok := resources.Limits[v1.ResourceCPU]
	if !ok || cpu.IsZero() {
		return
	}
	milliCPU := cpu.MilliValue()
	period, quota := util.CoresToPeriodAndQuota(float64(milliCPU) / 1000)
	unit.Add(quadlet.ContainerGroup, quadlet.KeyPodmanArgs,
		fmt.Sprintf("--cpu-quota=%d", quota))
	unit.Add(quadlet.ContainerGroup, quadlet.KeyPodmanArgs,
		fmt.Sprintf("--cpu-period=%d", period))
}
