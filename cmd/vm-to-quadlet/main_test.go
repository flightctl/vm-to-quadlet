package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestQuantityErrorHint(t *testing.T) {
	testCases := []struct {
		name        string
		err         error
		expectEmpty bool
	}{
		{
			name: "When the error is resource.ErrSuffix it should return a hint",
			err:  resource.ErrSuffix,
		},
		{
			name: "When the error is resource.ErrNumeric it should return a hint",
			err:  resource.ErrNumeric,
		},
		{
			name: "When the error is resource.ErrFormatWrong it should return a hint",
			err:  resource.ErrFormatWrong,
		},
		{
			name: "When the error wraps resource.ErrSuffix it should still return a hint",
			err:  fmt.Errorf("while decoding JSON: %w", resource.ErrSuffix),
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

func TestUint32OverflowHint(t *testing.T) {
	testCases := []struct {
		name        string
		err         error
		expectEmpty bool
	}{
		{
			name: "When the error is an UnmarshalTypeError on a uint32 field it should return a hint naming the field",
			err: &json.UnmarshalTypeError{
				Value:  "number -1",
				Type:   reflect.TypeOf(uint32(0)),
				Struct: "VirtualMachineInstanceTemplateSpec",
				Field:  "spec.template.spec.domain.cpu.cores",
			},
		},
		{
			name: "When the wrapped error is an UnmarshalTypeError on a uint32 field it should still return a hint",
			err: fmt.Errorf("while decoding JSON: %w", &json.UnmarshalTypeError{
				Type:  reflect.TypeOf(uint32(0)),
				Field: "spec.template.spec.domain.cpu.sockets",
			}),
		},
		{
			name: "When the UnmarshalTypeError is on a non-uint32 field it should return no hint",
			err: &json.UnmarshalTypeError{
				Type:  reflect.TypeOf(""),
				Field: "spec.template.spec.domain.cpu.model",
			},
			expectEmpty: true,
		},
		{
			name:        "When the error is not an UnmarshalTypeError it should return no hint",
			err:         errors.New("failed to read VM file: no such file or directory"),
			expectEmpty: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			hint := uint32OverflowHint(tc.err)
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
// is rejected at unmarshal time, with a hint naming the exact field. Cores is a
// uint32 field, so a negative number can't be represented and fails to decode
// before any validation or defaulting runs -- unlike "cores: 0", which is
// indistinguishable from an omitted field and gets defaulted to 1 (see
// TestPrepareForRendering_ZeroCoresDefaultsToOne in pkg/standalone).
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
	require.Contains(err.Error(), "Hint: spec.template.spec.domain.cpu.cores must be a whole number of 0 or more")
}
