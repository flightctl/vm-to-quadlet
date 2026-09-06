//go:build integration

// Package integration_test validates generated Quadlet files against real
// Podman quadlet generators running inside testcontainers.
//
// Run with: go test -v -count=1 -timeout=30m -tags=integration ./test/integration/
package integration_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestQuadletIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Quadlet Integration Suite")
}
