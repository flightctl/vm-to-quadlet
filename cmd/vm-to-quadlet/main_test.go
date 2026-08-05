package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuantityErrorHint(t *testing.T) {
	testCases := []struct {
		name        string
		err         error
		expectEmpty bool
	}{
		{
			name: "When the error is an invalid quantity suffix it should return a hint",
			err:  errors.New(`unable to parse quantity's suffix`),
		},
		{
			name: "When the error is an invalid quantity numeric part it should return a hint",
			err:  errors.New(`unable to parse numeric part of quantity`),
		},
		{
			name: "When the error is a malformed quantity format it should return a hint",
			err:  errors.New(`quantities must match the regular expression '...'`),
		},
		{
			name:        "When the error is unrelated to quantities it should return no hint",
			err:         errors.New(`failed to read VM file: no such file or directory`),
			expectEmpty: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			hint := quantityErrorHint(tc.err)
			if tc.expectEmpty {
				require.Empty(hint)
				return
			}
			require.NotEmpty(hint)
			require.Contains(hint, "Hint:")
		})
	}
}

// TestReadVM_NegativeCoresFails locks in that a negative domain.cpu.cores value
// is rejected at unmarshal time. Cores is a uint32 field, so a negative number
// can't be represented and fails to decode before any validation or defaulting
// runs -- unlike "cores: 0", which is indistinguishable from an omitted field
// and gets defaulted to 1 (see TestPrepareForRendering_ZeroCoresDefaultsToOne
// in pkg/standalone).
func TestReadVM_NegativeCoresFails(t *testing.T) {
	require := require.New(t)

	vmYAML := `
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: test-vm
spec:
  template:
    spec:
      domain:
        cpu:
          cores: -1
`
	path := filepath.Join(t.TempDir(), "vm.yaml")
	require.NoError(os.WriteFile(path, []byte(vmYAML), 0o644))

	_, err := readVM(path)
	require.Error(err)
	require.Contains(err.Error(), "cores")
	require.Contains(err.Error(), "uint32")
}
