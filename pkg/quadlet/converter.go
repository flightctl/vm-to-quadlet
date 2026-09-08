package quadlet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	k8sv1 "k8s.io/api/core/v1"

	podmanquadlet "github.com/flightctl/vm-to-quadlet/internal/third_party/kube/quadlet"
	podmanv1 "github.com/flightctl/vm-to-quadlet/internal/third_party/k8s.io/api/core/v1"
)

// bufPool reuses bytes.Buffer instances across unit-file rendering calls to
// reduce heap allocations in the hot loop inside runInProcess.
var bufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// Convert is step 6: converts a Kubernetes Pod spec to Quadlet unit files using
// the vendored in-process kube quadlet converter. The pod name is used as the
// name prefix for all generated filenames.
//
// When opts.Network is empty a dedicated <vmname>.network Quadlet unit is generated
// and Network= is set to "<vmname>.network", giving each VM an isolated bridge network.
// When opts.Network is set explicitly no .network unit is generated and the value is
// used as-is, allowing the caller to reference a shared or pre-existing network.
//
// Step 6a (preConvertFixups) runs internally before the conversion to apply
// KubeVirt-specific overrides (TypeMeta, TerminationGracePeriodSeconds).
//
// Step 7 (passt workaround hook injection) is handled separately by
// standalone.ApplyPostConvertFixups.
func Convert(pod *k8sv1.Pod, opts Options) ([]UnitFile, error) {
	pod = preConvertFixups(pod)

	var networkUnit *UnitFile
	if opts.Network == "" {
		name := pod.Name + ".network"
		networkUnit = &UnitFile{
			Name:    name,
			Content: "[Network]\n",
		}
		opts.Network = name
	}

	files, err := runInProcess(pod.Name, pod, opts)
	if err != nil {
		return nil, err
	}

	if networkUnit != nil {
		files = append(files, *networkUnit)
	}
	return files, nil
}

// runInProcess converts a Pod spec to Quadlet unit files using the vendored
// in-process converter. The k8s.io/api/core/v1.Pod from the transformer is
// round-tripped through JSON to bridge into the podman-vendored type system.
// Both types share identical json struct tags so JSON is a lossless, cheaper
// alternative to the previous YAML round-trip (which dominated the CPU profile
// due to go.yaml.in/yaml/v2 parse/emit overhead and the extra YAML↔JSON
// conversion inside sigs.k8s.io/yaml).
func runInProcess(vmName string, pod *k8sv1.Pod, opts Options) ([]UnitFile, error) {
	data, err := json.Marshal(pod)
	if err != nil {
		return nil, fmt.Errorf("marshal pod: %w", err)
	}

	var podmanPod podmanv1.Pod
	if err := json.Unmarshal(data, &podmanPod); err != nil {
		return nil, fmt.Errorf("unmarshal into podman pod: %w", err)
	}

	generated, err := podmanquadlet.Convert(&podmanPod, podmanquadlet.Options{
		NamePrefix: vmName,
		Network:    opts.Network,
	})
	if err != nil {
		return nil, fmt.Errorf("convert: %w", err)
	}

	files := make([]UnitFile, 0, len(generated))
	for _, f := range generated {
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		if err := f.Write(buf); err != nil {
			bufPool.Put(buf)
			return nil, fmt.Errorf("render %s: %w", f.Name, err)
		}
		files = append(files, UnitFile{Name: f.Name, Content: buf.String()})
		bufPool.Put(buf)
	}
	return files, nil
}

// preConvertFixups applies KubeVirt-specific field overrides to pod in place
// before the kube quadlet conversion (step 6a).
//
// The pod is mutated in place rather than deep-copied because it is freshly
// created by RenderLaunchManifest (step 4) and owned exclusively by the
// conversion pipeline — no caller retains or reuses it after Convert returns.
// Skipping the deep copy eliminates a significant allocation (~1 MB per
// conversion) that showed up as GC pressure in CPU profiles.
func preConvertFixups(pod *k8sv1.Pod) *k8sv1.Pod {
	// Ensure TypeMeta is set so the JSON is accepted by the converter.
	pod.Kind = "Pod"
	pod.APIVersion = "v1"

	// Set TerminationGracePeriodSeconds → StopTimeout=120 so virt-launcher
	// has time to send ACPI shutdown to the guest before SIGKILL.
	grace := int64(120)
	pod.Spec.TerminationGracePeriodSeconds = &grace

	return pod
}
