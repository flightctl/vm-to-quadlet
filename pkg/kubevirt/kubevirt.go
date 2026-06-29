// Package kubevirt wraps the KubeVirt TemplateService setup boilerplate needed
// for standalone mode. The actual RenderLaunchManifest API call is made by the
// caller directly on the returned *services.TemplateService so it remains
// visible and undecorated at the pipeline level.
package kubevirt

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	virtv1 "kubevirt.io/api/core/v1"
	"kubevirt.io/kubevirt/pkg/testutils"
	"kubevirt.io/kubevirt/pkg/virt-controller/services"
)

// NewTemplateService constructs a KubeVirt TemplateService configured for
// standalone mode. pvcCache must be pre-populated with PVC stubs for every
// PVC/DataVolume volume referenced in the VM spec before calling
// templateSvc.RenderLaunchManifest — see standalone.PrepareForRendering.
func NewTemplateService(pvcCache cache.Indexer, launcherImage string) *services.TemplateService {
	kv := &virtv1.KubeVirt{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubevirt",
			Namespace: "kubevirt",
		},
		Spec: virtv1.KubeVirtSpec{
			Configuration: virtv1.KubeVirtConfiguration{
				DeveloperConfiguration: &virtv1.DeveloperConfiguration{},
				VirtualMachineOptions: &virtv1.VirtualMachineOptions{
					DisableSerialConsoleLog: &virtv1.DisableSerialConsoleLog{},
				},
			},
		},
		Status: virtv1.KubeVirtStatus{
			Phase: virtv1.KubeVirtPhaseDeploying,
		},
	}
	kv.Spec.Configuration.DeveloperConfiguration.FeatureGates = []string{"ImageVolume", "HostDisk"}

	config, _, _ := testutils.NewFakeClusterConfigUsingKV(kv)

	resourceQuotaStore := cache.NewStore(cache.DeletionHandlingMetaNamespaceKeyFunc)
	namespaceStore := cache.NewStore(cache.DeletionHandlingMetaNamespaceKeyFunc)

	return services.NewTemplateService(
		launcherImage,
		240,
		"/var/run/kubevirt",
		"/var/run/kubevirt-ephemeral-disks",
		"/var/run/kubevirt/container-disks",
		virtv1.HotplugDiskDir,
		"pull-secret-1",
		pvcCache,
		nil,
		config,
		107,
		"quay.io/kubevirt/vm-export:latest",
		resourceQuotaStore,
		namespaceStore,
	)
}
