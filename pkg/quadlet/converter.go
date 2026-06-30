package quadlet

import (
	"bytes"
	"fmt"

	k8sv1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	podmanquadlet "github.com/flightctl/vm-to-quadlet/internal/third_party/kube/quadlet"
	podmanv1 "github.com/flightctl/vm-to-quadlet/internal/third_party/k8s.io/api/core/v1"
)

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
// round-tripped through YAML to bridge into the podman-vendored type system.
func runInProcess(vmName string, pod *k8sv1.Pod, opts Options) ([]UnitFile, error) {
	data, err := yaml.Marshal(pod)
	if err != nil {
		return nil, fmt.Errorf("marshal pod: %w", err)
	}

	var podmanPod podmanv1.Pod
	if err := yaml.Unmarshal(data, &podmanPod); err != nil {
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
		var buf bytes.Buffer
		if err := f.Write(&buf); err != nil {
			return nil, fmt.Errorf("render %s: %w", f.Name, err)
		}
		files = append(files, UnitFile{Name: f.Name, Content: buf.String()})
	}
	return files, nil
}

// preConvertFixups returns a deep copy of pod with KubeVirt-specific field
// overrides applied before the kube quadlet conversion (step 6a).
func preConvertFixups(pod *k8sv1.Pod) *k8sv1.Pod {
	pod = pod.DeepCopy()

	// Ensure TypeMeta is set so the YAML is accepted by the converter.
	pod.Kind = "Pod"
	pod.APIVersion = "v1"

	// Set TerminationGracePeriodSeconds → StopTimeout=120 so virt-launcher
	// has time to send ACPI shutdown to the guest before SIGKILL.
	grace := int64(120)
	pod.Spec.TerminationGracePeriodSeconds = &grace

	return pod
}
