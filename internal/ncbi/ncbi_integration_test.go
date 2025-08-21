//go:build integration
// +build integration

package ncbi

import "testing"

// This file contains integration tests that hit the real NCBI API or run full
// pipeline. They are excluded by default; run with `go test -tags=integration ./...`.

func TestIntegrationPlaceholder(t *testing.T) {
	t.Skip("integration tests are disabled by default")
}
