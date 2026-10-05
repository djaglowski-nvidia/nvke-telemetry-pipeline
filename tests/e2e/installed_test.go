//go:build e2e && (e2e_release_shared || !e2e_release_owned)

package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestInstalled validates the node exporter, backend, Collector workloads,
// monitors, and required CRDs of an existing release when -nktp.install=false.
// TestChartLifecycle runs the same checks around an install it owns; their
// shared validation functions remain in chart_test.go under the e2e tag.
func TestInstalled(t *testing.T) {
	if *install {
		t.Skip("the lifecycle test owns the release; pass -nktp.install=false to validate one already installed")
	}
	readyRelease(t.Context(), t)
	for _, tc := range []struct {
		name  string
		check func(context.Context) error
	}{
		{"the node exporter runs on every node", checkNodeExporterCoverage},
		{"the node exporter serves host metrics", checkNodeExporterMetrics},
		{"the node exporter declares itself as a scrape target", checkNodeExporterMonitor},
		{"the default backend serves Grafana", checkGrafanaHealth},
		{"every collector the chart declares is deployed", checkCollectorDeployment},
		{"every collector the chart declares is running", checkCollectorReadiness},
		{"the CRDs the chart needs are served", checkRequiredCRDs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := deadline(t.Context())
			defer cancel()
			assert.NoError(t, tc.check(ctx))
		})
	}
}
