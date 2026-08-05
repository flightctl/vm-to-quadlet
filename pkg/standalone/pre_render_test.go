package standalone

import (
	"testing"

	"github.com/stretchr/testify/require"
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
