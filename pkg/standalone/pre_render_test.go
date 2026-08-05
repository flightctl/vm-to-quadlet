package standalone

import (
	"testing"

	"github.com/stretchr/testify/require"
	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	virtv1 "kubevirt.io/api/core/v1"
)

func TestValidateDiskVolumeReferences(t *testing.T) {
	testCases := []struct {
		name        string
		spec        virtv1.VirtualMachineInstanceSpec
		expectedLen int
		expectedMsg string
	}{
		{
			name: "When every disk matches a volume it should return no errors",
			spec: virtv1.VirtualMachineInstanceSpec{
				Domain: virtv1.DomainSpec{
					Devices: virtv1.Devices{
						Disks: []virtv1.Disk{{Name: "containerdisk"}, {Name: "cloudinitdisk"}},
					},
				},
				Volumes: []virtv1.Volume{{Name: "containerdisk"}, {Name: "cloudinitdisk"}},
			},
			expectedLen: 0,
		},
		{
			name: "When a disk name has no matching volume it should return an error naming the disk and the available volumes",
			spec: virtv1.VirtualMachineInstanceSpec{
				Domain: virtv1.DomainSpec{
					Devices: virtv1.Devices{
						Disks: []virtv1.Disk{{Name: "containerdiskoooooo"}, {Name: "cloudinitdisk"}},
					},
				},
				Volumes: []virtv1.Volume{{Name: "containerdisk"}, {Name: "cloudinitdisk"}},
			},
			expectedLen: 1,
			expectedMsg: `disk "containerdiskoooooo" does not match any volume in spec.template.spec.volumes; available volumes: [cloudinitdisk containerdisk]`,
		},
		{
			name: "When there are no disks it should return no errors",
			spec: virtv1.VirtualMachineInstanceSpec{
				Volumes: []virtv1.Volume{{Name: "containerdisk"}},
			},
			expectedLen: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			errs := validateDiskVolumeReferences(tc.spec)
			require.Len(errs, tc.expectedLen)
			if tc.expectedMsg != "" {
				require.Equal(tc.expectedMsg, errs[0])
			}
		})
	}
}

func TestValidateForStandalone_DiskVolumeMismatch(t *testing.T) {
	require := require.New(t)

	vm := &virtv1.VirtualMachine{
		Spec: virtv1.VirtualMachineSpec{
			Template: &virtv1.VirtualMachineInstanceTemplateSpec{
				Spec: virtv1.VirtualMachineInstanceSpec{
					Domain: virtv1.DomainSpec{
						Devices: virtv1.Devices{
							Disks: []virtv1.Disk{{Name: "containerdiskoooooo"}},
						},
					},
					Volumes: []virtv1.Volume{{Name: "containerdisk"}},
				},
			},
		},
	}

	err := validateForStandalone(vm)
	require.Error(err)
	require.Contains(err.Error(), `disk "containerdiskoooooo" does not match any volume`)
}

func TestNonPositiveResourceErrors(t *testing.T) {
	testCases := []struct {
		name        string
		list        k8sv1.ResourceList
		expectedLen int
		expectedMsg string
	}{
		{
			name: "When cpu and memory are positive it should return no errors",
			list: k8sv1.ResourceList{
				k8sv1.ResourceCPU:    resource.MustParse("2"),
				k8sv1.ResourceMemory: resource.MustParse("1Gi"),
			},
			expectedLen: 0,
		},
		{
			name:        "When the resource list is nil it should return no errors",
			list:        nil,
			expectedLen: 0,
		},
		{
			name: "When cpu is zero it should return an error",
			list: k8sv1.ResourceList{
				k8sv1.ResourceCPU: resource.MustParse("0"),
			},
			expectedLen: 1,
			expectedMsg: `spec.domain.resources.requests.cpu must be a positive quantity, got "0"`,
		},
		{
			name: "When cpu is negative it should return an error",
			list: k8sv1.ResourceList{
				k8sv1.ResourceCPU: resource.MustParse("-1"),
			},
			expectedLen: 1,
			expectedMsg: `spec.domain.resources.requests.cpu must be a positive quantity, got "-1"`,
		},
		{
			name: "When memory is zero it should return an error",
			list: k8sv1.ResourceList{
				k8sv1.ResourceMemory: resource.MustParse("0"),
			},
			expectedLen: 1,
			expectedMsg: `spec.domain.resources.requests.memory must be a positive quantity, got "0"`,
		},
		{
			name: "When both cpu and memory are non-positive it should return both errors",
			list: k8sv1.ResourceList{
				k8sv1.ResourceCPU:    resource.MustParse("0"),
				k8sv1.ResourceMemory: resource.MustParse("-1Gi"),
			},
			expectedLen: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			errs := nonPositiveResourceErrors("spec.domain.resources.requests", tc.list)
			require.Len(errs, tc.expectedLen)
			if tc.expectedMsg != "" {
				require.Equal(tc.expectedMsg, errs[0])
			}
		})
	}
}

func TestValidateForStandalone_NonPositiveResources(t *testing.T) {
	require := require.New(t)

	vm := &virtv1.VirtualMachine{
		Spec: virtv1.VirtualMachineSpec{
			Template: &virtv1.VirtualMachineInstanceTemplateSpec{
				Spec: virtv1.VirtualMachineInstanceSpec{
					Domain: virtv1.DomainSpec{
						Resources: virtv1.ResourceRequirements{
							Limits: k8sv1.ResourceList{
								k8sv1.ResourceCPU: resource.MustParse("-1"),
							},
						},
					},
				},
			},
		},
	}

	err := validateForStandalone(vm)
	require.Error(err)
	require.Contains(err.Error(), `spec.domain.resources.limits.cpu must be a positive quantity, got "-1"`)
}

// TestPrepareForRendering_ZeroCoresDefaultsToOne locks in that domain.cpu.cores: 0
// is accepted and defaulted to 1, matching real KubeVirt/Kubernetes behavior: the
// mutating admission webhook always runs before the validating one, so a real
// cluster's validation never actually observes an explicit "cores: 0" either -- by
// the time validation would run, cores has already been defaulted to 1. Cores is a
// uint32 struct field, so "0" is indistinguishable from the field being omitted
// entirely; rejecting it here would also reject the far more common case of a VM
// that doesn't specify domain.cpu at all.
func TestPrepareForRendering_ZeroCoresDefaultsToOne(t *testing.T) {
	require := require.New(t)

	vm := &virtv1.VirtualMachine{
		Spec: virtv1.VirtualMachineSpec{
			Template: &virtv1.VirtualMachineInstanceTemplateSpec{
				Spec: virtv1.VirtualMachineInstanceSpec{
					Domain: virtv1.DomainSpec{
						CPU: &virtv1.CPU{Cores: 0},
						Devices: virtv1.Devices{
							Disks: []virtv1.Disk{{Name: "containerdisk"}},
						},
					},
					Volumes: []virtv1.Volume{{Name: "containerdisk", VolumeSource: virtv1.VolumeSource{
						ContainerDisk: &virtv1.ContainerDiskSource{Image: "example.com/fedora:latest"},
					}}},
				},
			},
		},
	}

	prepared, err := PrepareForRendering(vm, Options{LauncherImage: "example.com/virt-launcher:latest"})
	require.NoError(err)
	require.NotNil(prepared.VMI.Spec.Domain.CPU)
	require.EqualValues(1, prepared.VMI.Spec.Domain.CPU.Cores)
}
