//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/NVIDIA-dev/nktp/tests/internal/framework"
	"github.com/NVIDIA-dev/nktp/tests/internal/kube"
)

// The node exporter labels every object it owns with its chart name, which is
// stable across release names.
const nodeExporterSelector = "app.kubernetes.io/name=node-exporter"

var serviceMonitors = schema.GroupVersionResource{
	Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors",
}

// TestInstalled validates a release this suite did not install, which is what
// qualifying an NKTP installation on a real cluster amounts to. It checks the
// node exporter, backend, Collector workloads, monitors, and required CRDs.
// TestChartLifecycle runs the same checks around an install it owns.
func TestInstalled(t *testing.T) {
	if *install {
		t.Skip("the lifecycle test owns the release; pass -nktp.install=false to validate one already installed")
	}
	verifyInstalled(t, newKube(t))
}

// verifyInstalled asserts what a user can observe of an installed release.
// Nothing here may depend on the release being freshly created, since it also
// runs after a reapply and against installations of unknown age.
func verifyInstalled(t *testing.T, k *kube.Client) {
	t.Helper()

	t.Run("the node exporter runs on every node", func(t *testing.T) {
		ctx, cancel := deadline(t)
		defer cancel()

		daemonsets, err := k.DaemonSets(ctx, *namespace, nodeExporterSelector)
		require.NoError(t, err)
		require.Len(t, daemonsets, 1, "expected exactly one node exporter DaemonSet")

		nodes, err := k.NodeCount(ctx)
		require.NoError(t, err)

		status := daemonsets[0].Status
		assert.EqualValues(t, nodes, status.DesiredNumberScheduled,
			"the node exporter should be scheduled on every node")
		assert.Equal(t, status.DesiredNumberScheduled, status.NumberReady,
			"every scheduled node exporter pod should be ready")
	})

	t.Run("the node exporter serves host metrics", func(t *testing.T) {
		ctx, cancel := deadline(t)
		defer cancel()

		pods, err := k.Pods(ctx, *namespace, nodeExporterSelector)
		require.NoError(t, err)
		require.NotEmpty(t, pods, "no node exporter pods to scrape")

		body, err := k.PodEndpoint(ctx, *namespace, pods[0].Name, "9100", "/metrics")
		require.NoError(t, err, "scraping the node exporter")
		assert.Contains(t, string(body), "node_cpu_seconds_total",
			"the exporter should report host CPU metrics")
	})

	t.Run("the node exporter declares itself as a scrape target", func(t *testing.T) {
		ctx, cancel := deadline(t)
		defer cancel()

		// A ServiceMonitor is a custom resource, so it exists only if the
		// Prometheus CRDs were established before Helm applied it. Rendering
		// one always succeeds, so an install is the only place that shows.
		monitors, err := k.CustomResources(ctx, *namespace, serviceMonitors)
		require.NoError(t, err)
		assert.Contains(t, names(monitors), *release+"-node-exporter",
			"the exporter should declare a monitor, ready for a collector to pick up")
	})

	t.Run("the default backend serves Grafana", func(t *testing.T) {
		ctx, cancel := deadline(t)
		defer cancel()

		body, err := k.ServiceEndpoint(ctx, *namespace, "lgtm", "3000", "/api/health")
		require.NoError(t, err, "reaching Grafana through the lgtm service")
		assert.Contains(t, string(body), "database", "Grafana should report its health")
	})

	// Adding a collector to the chart means adding it here.
	t.Run("every collector the chart declares is deployed", func(t *testing.T) {
		ctx, cancel := deadline(t)
		defer cancel()

		deployed, err := k.CustomResources(ctx, *namespace, framework.Collectors)
		require.NoError(t, err)

		var want []string
		for _, collector := range []string{
			"cluster-events",
			"daemonset",
			"gateway-logs",
			"gateway-metrics",
			"gateway-traces",
			"large-target-metrics",
			"watchdog",
		} {
			want = append(want, *release+"-"+collector)
		}
		assert.ElementsMatch(t, want, names(deployed))
	})

	t.Run("every collector the chart declares is running", func(t *testing.T) {
		ctx, cancel := deadline(t)
		defer cancel()

		// A Collector CR is a request, not a running collector: the operator
		// builds the workload behind it, and a CR it has not acted on, or one
		// whose pods cannot schedule, leaves the subtest above passing over an
		// installation that collects nothing.
		deployed, err := k.CustomResources(ctx, *namespace, framework.Collectors)
		require.NoError(t, err)
		expected, err := framework.CollectorWorkloads(deployed)
		require.NoError(t, err)

		workloads, err := k.Workloads(ctx, *namespace)
		require.NoError(t, err)
		ready := make(map[string]bool, len(workloads))
		for _, w := range workloads {
			ready[w.Name] = w.Ready
		}

		for _, name := range expected {
			if assert.Contains(t, ready, name, "the operator should have built a workload named %s", name) {
				assert.True(t, ready[name], "%s should be ready", name)
			}
		}
	})

	t.Run("the CRDs the chart needs are served", func(t *testing.T) {
		const group = "monitoring.coreos.com"
		for _, resource := range []string{"servicemonitors", "podmonitors", "probes", "scrapeconfigs"} {
			served, err := k.ServesResource(group, resource)
			require.NoError(t, err)
			assert.True(t, served, "%s.%s should be installed", resource, group)
		}
		served, err := k.ServesResource("opentelemetry.io", "opentelemetrycollectors")
		require.NoError(t, err)
		assert.True(t, served, "the Collector CRD should be installed")
	})
}
