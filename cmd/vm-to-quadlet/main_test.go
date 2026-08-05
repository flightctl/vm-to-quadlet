package main

import (
	"errors"
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
