// Package standalone contains all logic that is ours — the fixups applied
// before and after the KubeVirt and quadlet vendored API calls. Each exported
// function corresponds to one named stage of the conversion pipeline:
//
//   Step 3: PrepareForRendering  — pre-render fixups, produces PreparedVM
//   Step 5: AdaptForStandalone   — post-render Pod mutations for standalone/Podman
//   Step 7: ApplyPostConvertFixups — post-quadlet fixups (passt hook injection)
package standalone

import (
	"k8s.io/client-go/tools/cache"
	virtv1 "kubevirt.io/api/core/v1"
)

// Options controls standalone-mode behaviour for steps 3 and 5.
type Options struct {
	LauncherImage  string
	AddVNCProxy    bool
	VNCPort        int
	VNCImage       string
	AddSerialProxy bool
	SerialPort     int
	SerialImage    string
}

// PreparedVM is the output of PrepareForRendering (step 3).
// It carries everything that step 4 (KubeVirt RenderLaunchManifest) and
// step 5 (AdaptForStandalone) need, bundled into one self-contained value.
type PreparedVM struct {
	// VM is the original VirtualMachine, needed by AdaptForStandalone (step 5)
	// for DataVolume init-container injection and persistence warnings.
	VM *virtv1.VirtualMachine

	// VMI is the prepared VirtualMachineInstance ready to pass to
	// templateSvc.RenderLaunchManifest (step 4).
	VMI *virtv1.VirtualMachineInstance

	// PVCCache is pre-populated with stub PersistentVolumeClaim objects for
	// every PVC/DataVolume volume in the VM spec. KubeVirt's TemplateService
	// looks up PVCs from this cache during RenderLaunchManifest; in standalone
	// mode there is no Kubernetes API so we provide fakes.
	PVCCache cache.Indexer
}
