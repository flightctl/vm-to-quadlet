package standalone

import (
	"fmt"
	"os"

	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	virtv1 "kubevirt.io/api/core/v1"
	"kubevirt.io/kubevirt/pkg/defaults"
	"kubevirt.io/kubevirt/pkg/network/vmispec"
	"kubevirt.io/kubevirt/pkg/testutils"
	"kubevirt.io/kubevirt/pkg/util"
	"kubevirt.io/kubevirt/pkg/virt-api/webhooks/mutating-webhook/mutators"
	virtconfig "kubevirt.io/kubevirt/pkg/virt-config"
	vmCtrl "kubevirt.io/kubevirt/pkg/virt-controller/watch/vm"
)

// PrepareForRendering is step 3: applies all pre-render fixups to the VM,
// derives a VMI, and pre-populates the PVC cache with stubs so that
// KubeVirt's TemplateService can proceed without a live Kubernetes API.
//
// Returns a PreparedVM ready to be passed to kubevirt.NewTemplateService and
// templateSvc.RenderLaunchManifest (step 4).
func PrepareForRendering(vm *virtv1.VirtualMachine, opts Options) (*PreparedVM, error) {
	if err := validateForStandalone(vm); err != nil {
		return nil, err
	}

	if vm.ObjectMeta.Namespace == "" {
		vm.ObjectMeta.Namespace = "default"
	}

	pvcCache := cache.NewIndexer(cache.DeletionHandlingMetaNamespaceKeyFunc, nil)
	stubPVCsForVM(pvcCache, vm)

	clusterConfig := newClusterConfig()

	defaults.SetVirtualMachineDefaults(vm, clusterConfig, nil)

	vmi := vmCtrl.SetupVMIFromVM(vm)

	if err := defaults.SetDefaultVirtualMachineInstance(clusterConfig, vmi); err != nil {
		return nil, fmt.Errorf("failed to set VMI defaults: %v", err)
	}
	if err := mutators.ApplyNewVMIMutations(vmi, clusterConfig); err != nil {
		return nil, fmt.Errorf("failed to apply VMI mutations: %v", err)
	}
	if err := vmispec.SetDefaultNetworkInterface(clusterConfig, &vmi.Spec); err != nil {
		return nil, fmt.Errorf("failed to set default network: %v", err)
	}

	util.SetDefaultVolumeDisk(&vmi.Spec)
	vmCtrl.AutoAttachInputDevice(vmi)

	forcePasstBinding(&vmi.Spec)

	// virt-launcher-monitor resolves the QEMU PID file as /run/libvirt/qemu/run/<uid>_<name>.pid.
	// libvirt names the domain <namespace>_<name>, so the PID file is <namespace>_<name>.pid.
	// When the VMI has no UID (offline conversion), use the namespace so the monitor finds the
	// correct PID file and does not time out prematurely.
	if vmi.UID == "" {
		vmi.UID = types.UID(vmi.Namespace)
	}

	// Populate VMI interface status with PodInterfaceName now that interfaces
	// are finalised (forcePasstBinding has already run). In Kubernetes this is
	// done by virt-handler at runtime; here we do it offline so the marshaled
	// VMI in STANDALONE_VMI carries the correct status from the start.
	populateInterfaceStatus(vmi)

	return &PreparedVM{
		VM:       vm,
		VMI:      vmi,
		PVCCache: pvcCache,
	}, nil
}

// newClusterConfig builds the minimal fake KubeVirt ClusterConfig used during
// offline conversion. It enables the ImageVolume and HostDisk feature gates
// which are required for the corresponding volume types to pass validation.
func newClusterConfig() *virtconfig.ClusterConfig {
	kv := &virtv1.KubeVirt{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubevirt",
			Namespace: "kubevirt",
		},
		Spec: virtv1.KubeVirtSpec{
			Configuration: virtv1.KubeVirtConfiguration{
				DeveloperConfiguration: &virtv1.DeveloperConfiguration{
					FeatureGates: []string{"ImageVolume", "HostDisk"},
				},
				VirtualMachineOptions: &virtv1.VirtualMachineOptions{
					DisableSerialConsoleLog: &virtv1.DisableSerialConsoleLog{},
				},
			},
		},
		Status: virtv1.KubeVirtStatus{
			Phase: virtv1.KubeVirtPhaseDeploying,
		},
	}
	config, _, _ := testutils.NewFakeClusterConfigUsingKV(kv)
	return config
}

// stubPVCsForVM pre-populates pvcCache with minimal stub PersistentVolumeClaim
// objects for every persistentVolumeClaim and dataVolume volume in the VM spec.
// RenderLaunchManifest looks up each PVC by namespace/name to determine its
// volume mode; in standalone mode there is no Kubernetes API to provide real
// PVCs. The stubs carry Filesystem volume mode and ReadWriteOnce access, which
// matches what a Podman named volume provides.
func stubPVCsForVM(pvcCache cache.Indexer, vm *virtv1.VirtualMachine) {
	ns := vm.Namespace
	if ns == "" {
		ns = "default"
	}
	filesystemMode := k8sv1.PersistentVolumeFilesystem
	for _, vol := range vm.Spec.Template.Spec.Volumes {
		switch {
		case vol.PersistentVolumeClaim != nil:
			addPVCStub(pvcCache, ns, vol.PersistentVolumeClaim.ClaimName, filesystemMode)
		case vol.DataVolume != nil:
			addPVCStub(pvcCache, ns, vol.DataVolume.Name, filesystemMode)
		}
	}
}

func addPVCStub(pvcCache cache.Indexer, ns, claimName string, volumeMode k8sv1.PersistentVolumeMode) {
	pvc := &k8sv1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claimName,
			Namespace: ns,
		},
		Spec: k8sv1.PersistentVolumeClaimSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce},
			VolumeMode:  &volumeMode,
		},
	}
	_ = pvcCache.Add(pvc)
}

// populateInterfaceStatus fills vmi.Status.Interfaces with PodInterfaceName
// for each interface in the spec. In Kubernetes this is done at runtime by
// virt-handler; for offline conversion we do it here, once interfaces are
// finalised, so that the STANDALONE_VMI env var carries the correct status.
func populateInterfaceStatus(vmi *virtv1.VirtualMachineInstance) {
	for i, iface := range vmi.Spec.Domain.Devices.Interfaces {
		podIfaceName := fmt.Sprintf("eth%d", i)
		vmi.Status.Interfaces = append(vmi.Status.Interfaces, virtv1.VirtualMachineInstanceNetworkInterface{
			Name:             iface.Name,
			PodInterfaceName: podIfaceName,
		})
	}
}

// forcePasstBinding ensures all VMI interfaces use PasstBinding and are
// linked to pod networks. PasstBinding is required for standalone mode;
// Masquerade causes a nil pointer panic in virt-launcher's PreCloudInitIso
// hook when there is no cloud-init volume.
func forcePasstBinding(spec *virtv1.VirtualMachineInstanceSpec) {
	hasPodNetwork := false
	for _, net := range spec.Networks {
		if net.Pod != nil {
			hasPodNetwork = true
			break
		}
	}
	if !hasPodNetwork {
		spec.Networks = append([]virtv1.Network{{
			Name:          "default",
			NetworkSource: virtv1.NetworkSource{Pod: &virtv1.PodNetwork{}},
		}}, spec.Networks...)
	}

	for i := range spec.Domain.Devices.Interfaces {
		iface := &spec.Domain.Devices.Interfaces[i]
		iface.InterfaceBindingMethod = virtv1.InterfaceBindingMethod{
			PasstBinding: &virtv1.InterfacePasstBinding{},
		}
		iface.Masquerade = nil
		iface.Bridge = nil
		iface.DeprecatedSlirp = nil
		iface.SRIOV = nil
	}

	for i := range spec.Domain.Devices.Interfaces {
		iface := &spec.Domain.Devices.Interfaces[i]
		found := false
		for j := range spec.Networks {
			net := &spec.Networks[j]
			if net.Name == iface.Name {
				net.Pod = &virtv1.PodNetwork{}
				net.Multus = nil
				found = true
				break
			}
		}
		if found {
			continue
		}
		iface.Name = "default"
		defaultExists := false
		for _, net := range spec.Networks {
			if net.Name == "default" {
				defaultExists = true
				break
			}
		}
		if !defaultExists {
			spec.Networks = append(spec.Networks, virtv1.Network{
				Name:          "default",
				NetworkSource: virtv1.NetworkSource{Pod: &virtv1.PodNetwork{}},
			})
		}
	}
}

// validateForStandalone checks the VM spec for features that cannot work in
// standalone mode. Hard errors are returned; soft warnings are printed to stderr.
func validateForStandalone(vm *virtv1.VirtualMachine) error {
	spec := vm.Spec.Template.Spec

	var errors []string
	var warnings []string

	dvTemplateNames := map[string]bool{}
	for _, dvt := range vm.Spec.DataVolumeTemplates {
		dvTemplateNames[dvt.Name] = true
	}

	for _, vol := range spec.Volumes {
		if vol.DataVolume != nil {
			if !dvTemplateNames[vol.DataVolume.Name] {
				warnings = append(warnings, fmt.Sprintf(
					"volume %q references DataVolume %q but no matching dataVolumeTemplate found — "+
						"disk will not be auto-created; ensure the disk image exists before starting the VM",
					vol.Name, vol.DataVolume.Name))
			}
			continue
		}
		if vol.ConfigMap != nil {
			errors = append(errors, fmt.Sprintf(
				"volume %q uses ConfigMap which requires the Kubernetes API. "+
					"Use a cloudInitNoCloud or cloudInitConfigDrive volume with inline data instead", vol.Name))
		}
		if vol.Secret != nil {
			errors = append(errors, fmt.Sprintf(
				"volume %q uses Secret which requires the Kubernetes API. "+
					"Use a cloudInitNoCloud or cloudInitConfigDrive volume with inline data instead", vol.Name))
		}
		if vol.ServiceAccount != nil {
			errors = append(errors, fmt.Sprintf(
				"volume %q uses ServiceAccount which requires the Kubernetes API", vol.Name))
		}
	}

	for _, net := range spec.Networks {
		if net.Multus != nil {
			warnings = append(warnings, fmt.Sprintf(
				"network %q uses Multus which requires CNI plugins configured for podman. "+
					"Passt networking will be used by default (use --no-passt to keep Multus)", net.Name))
		}
	}

	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	if len(errors) == 0 {
		return nil
	}

	msg := "VM definition contains features unsupported in standalone mode:\n"
	for _, e := range errors {
		msg += fmt.Sprintf("  - %s\n", e)
	}
	return fmt.Errorf("%s", msg)
}
