//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/NVIDIA-dev/nktp/tests/internal/kube"
)

// The bundled ServiceMonitors use a scrape interval of at most one minute.
// Allow two intervals for a scrape and its export to reach LGTM.
const componentMetricMaxAge = 2 * time.Minute

// TestKubernetesComponentMetrics checks that each bundled Kubernetes component
// ServiceMonitor delivers a recent metric to LGTM. The job label distinguishes
// the four Kubernetes components that all export kubernetes_build_info.
func TestKubernetesComponentMetrics(t *testing.T) {
	withRelease(t, verifyKubernetesComponentMetrics)
}

func verifyKubernetesComponentMetrics(t *testing.T, k *kube.Client) {
	t.Helper()

	cases := []struct {
		component string
		metric    string
		job       string
	}{
		{"kube-apiserver", "kubernetes_build_info", "apiserver"},
		{"kube-controller-manager", "kubernetes_build_info", "kube-controller-manager"},
		{"kube-scheduler", "kubernetes_build_info", "kube-scheduler"},
		{"kube-proxy", "kubernetes_build_info", "kube-proxy"},
		{"coredns", "coredns_build_info", "coredns"},
		{"etcd", "etcd_server_has_leader", "kube-etcd"},
	}

	ctx, cancel := deadline(t)
	defer cancel()

	found := make([]bool, len(cases))
	queryErrors := make([]error, len(cases))
	err := wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		complete := true
		for i, tc := range cases {
			if ctx.Err() != nil {
				return false, nil
			}
			expression := fmt.Sprintf(`timestamp(%s{job=%q}) > time() - %d`,
				tc.metric, tc.job, int(componentMetricMaxAge.Seconds()))
			jobs, queryErr := prometheusMetricLabelValues(ctx, k, expression, "job")
			if ctx.Err() != nil {
				return false, nil
			}
			queryErrors[i] = queryErr
			found[i] = queryErr == nil && len(jobs) > 0
			complete = complete && found[i]
		}
		return complete, nil
	})
	for i, tc := range cases {
		t.Run(tc.component, func(t *testing.T) {
			if assert.NoError(t, queryErrors[i], "querying %s{job=%q} in LGTM", tc.metric, tc.job) {
				assert.True(t, found[i], "%s{job=%q} has no sample in LGTM from the last %s",
					tc.metric, tc.job, componentMetricMaxAge)
			}
		})
	}
	if !wait.Interrupted(err) {
		assert.NoError(t, err, "polling Kubernetes component metrics")
	}
}
