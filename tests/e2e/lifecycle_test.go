//go:build e2e && (e2e_release_owned || !e2e_release_shared)

package e2e

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA-dev/nktp/tests/internal/kube"
)

// TestChartLifecycle installs the chart, validates it, reapplies it, validates
// it again, removes it, and checks what the removal left behind. That loop is
// the unit the wider test matrix repeats, so a new scenario is a different set
// of inputs to it rather than another stage.
//
// It owns the release from end to end, which is why the steps are subtests of
// one test rather than tests in their own right: nothing here is meaningful
// out of order, and Go orders subtests but not tests. It refuses to adopt an
// existing release because cleanup would otherwise delete something it did not
// create.
//
// Reapply checks generations of Helm-managed workloads, where any change means
// the same inputs rendered differently. Operator-derived workloads are only
// reported because the Operator currently rewrites them even when their
// Collector resource is unchanged. This verifies a stable reapply of the local
// chart; it is not an upgrade test against a previously published release.
func TestChartLifecycle(t *testing.T) {
	if !*install {
		t.Skip("-nktp.install is unset, so this suite does not own a release to install or remove")
	}

	deployed := newChart(t)
	owner := t

	installed := t.Run("the chart installs", func(t *testing.T) {
		ctx, cancel := deadline(t.Context())
		defer cancel()

		owned, err := deployed.InstallFresh(ctx)
		if owned {
			registerChartCleanup(owner, deployed)
		}
		require.NoError(t, err,
			"installing a fresh release; validate an existing release with -nktp.install=false")
	})
	// A release that failed to install says nothing about the chart's
	// behaviour once installed, and an uninstall check run against one would
	// report on whatever the failure happened to leave.
	require.True(t, installed, "the rest of the lifecycle needs an installed release")

	for _, stage := range []string{
		"an installed release",
		"an installed release, reapplied",
	} {
		if stage == "an installed release, reapplied" {
			t.Run("reapplying the release changes nothing", func(t *testing.T) {
				before, err := k.Workloads(t.Context(), *namespace)
				require.NoError(t, err)

				ctx, cancel := deadline(t.Context())
				defer cancel()
				_, err = deployed.Apply(ctx)
				require.NoError(t, err, "reapplying the chart")

				after, err := k.Workloads(t.Context(), *namespace)
				require.NoError(t, err)

				// Helm-managed generations must stay stable for identical inputs.
				// If install-time generation is introduced, -nktp.stable-reapply=false
				// reports changes without enforcing stability.
				wasHelm, isHelm := generations(before, false), generations(after, false)
				if *stableReapply {
					assert.Equal(t, wasHelm, isHelm,
						"reapplying the same release rolled or changed a workload Helm manages")
				} else if !slices.Equal(wasHelm, isHelm) {
					t.Log("note: reapplying the release moved a workload Helm manages")
				}

				// The Operator rewrites derived workloads even for unchanged Collector
				// CRs. Report those rollouts without failing the chart's stability check.
				if !slices.Equal(generations(before, true), generations(after, true)) {
					t.Log("note: the operator rewrote the workloads it derives from unchanged Collector CRs, so a reapply rolled every collector pod")
				}
			})
		}

		t.Run(stage, func(t *testing.T) {
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
		})
	}

	t.Run("uninstalling takes the workloads with it", func(t *testing.T) {
		ctx, cancel := deadline(t.Context())
		defer cancel()
		require.NoError(t, deployed.Uninstall(ctx), "uninstalling the release")

		// Kubernetes' garbage collector removes the workloads the operator
		// derives, once the Collector they reference is gone. Helm waits only
		// for the objects it applied itself.
		waitCtx, cancelWait := deadline(t.Context())
		defer cancelWait()
		assert.NoError(t, k.WaitWorkloadsGone(waitCtx, *namespace), "uninstall should leave no workloads behind")
	})

	t.Run("uninstalling leaves the CRDs in place", func(t *testing.T) {
		// The CRDs come from the crds/ directories of the bundled charts,
		// which Helm never deletes, so an NKTP uninstall leaves a cluster's
		// other Collectors and monitors alone.
		for _, crd := range []struct{ group, resource string }{
			{"opentelemetry.io", "opentelemetrycollectors"},
			{"monitoring.coreos.com", "servicemonitors"},
		} {
			served, err := k.ServesResource(crd.group, crd.resource)
			require.NoError(t, err)
			assert.True(t, served, "%s.%s should survive an uninstall", crd.resource, crd.group)
		}
	})
}

// generations renders the workloads one side owns, so that comparing two
// rounds reports which of them moved rather than only that something did.
func generations(workloads []kube.Workload, derived bool) []string {
	var out []string
	for _, w := range workloads {
		if w.Derived == derived {
			out = append(out, fmt.Sprintf("%s generation %d", w, w.Generation))
		}
	}
	return out
}
