// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// The bundled ServiceMonitors use a scrape interval of at most one minute.
// Allow two intervals for a scrape and its export to reach LGTM.
const componentMetricMaxAge = 2 * time.Minute

// TestKubernetesComponentMetrics checks that each bundled Kubernetes component
// ServiceMonitor delivers a recent metric to LGTM. The job label distinguishes
// the four Kubernetes components that all export kubernetes_build_info.
func TestKubernetesComponentMetrics(t *testing.T) {
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

	f := features.New("Kubernetes component metrics")
	for _, tc := range cases {
		f.Assess(tc.component, func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			client, err := coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			services := client.Services(c.Namespace())

			expression := fmt.Sprintf(`timestamp(%s{job=%q}) > time() - %d`,
				tc.metric, tc.job, int(componentMetricMaxAge.Seconds()))
			var jobs []string
			var queryErr error
			err = wait.For(func(ctx context.Context) (bool, error) {
				jobs, queryErr = prometheusMetricLabelValues(ctx, services, expression, "job")
				return queryErr == nil && len(jobs) > 0, nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			if assert.NoError(t, queryErr, "querying %s{job=%q} in LGTM", tc.metric, tc.job) {
				assert.NotEmpty(t, jobs, "%s{job=%q} has no sample in LGTM from the last %s",
					tc.metric, tc.job, componentMetricMaxAge)
			}
			assert.NoError(t, err, "polling %s metrics", tc.component)
			return ctx
		})
	}
	testenv.Test(t, f.Feature())
}
