// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e && (e2e_release_shared || !e2e_release_owned)

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	kubeletMetricsFixtureName = "nktp-e2e-kubelet-metrics"
)

// TestKubeletMetrics uses the configured release mode and proves that every node sends
// both kubelet and cAdvisor metrics to Prometheus. It runs one passive,
// digest-pinned NVIDIA container on every node so cAdvisor has a real workload
// to observe, requires that workload's memory metric to be non-zero, and then
// removes the fixture and, in install mode, the release.
func TestKubeletMetrics(t *testing.T) {
	readyRelease(t.Context(), t)

	ctx, cancel := deadline(t.Context())
	defer cancel()

	nodes, err := k.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "listing nodes for the kubelet metrics fixture")
	require.NotEmpty(t, nodes.Items, "the cluster must have a node to test")

	require.NoError(t, createKubeletMetricsFixture(ctx))
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := deadline(context.WithoutCancel(t.Context()))
		defer cleanupCancel()
		assert.NoError(t, deleteKubeletMetricsFixture(cleanupCtx), "removing the kubelet metrics fixture")
	})
	require.NoError(t, waitForKubeletMetricsFixture(ctx, int32(len(nodes.Items))))

	want := make([]string, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		want = append(want, node.Name)
	}
	sort.Strings(want)

	require.NoError(t, waitForKubeletMetricNodes(ctx, want),
		"waiting for kubelet and cAdvisor metrics from every node")
}

func createKubeletMetricsFixture(ctx context.Context) error {
	labels := map[string]string{"app.kubernetes.io/name": kubeletMetricsFixtureName}

	fixture := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: kubeletMetricsFixtureName, Namespace: *namespace},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: passiveFixturePodTemplate(kubeletMetricsFixtureName),
		},
	}

	if _, err := k.AppsV1().DaemonSets(*namespace).Create(ctx, fixture, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating DaemonSet %s: %w", kubeletMetricsFixtureName, err)
	}
	return nil
}

func waitForKubeletMetricsFixture(ctx context.Context, nodes int32) error {
	var status appsv1.DaemonSetStatus
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		current, err := k.AppsV1().DaemonSets(*namespace).Get(ctx, kubeletMetricsFixtureName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		status = current.Status
		return status.ObservedGeneration >= current.Generation &&
			status.DesiredNumberScheduled == nodes &&
			status.NumberReady == nodes &&
			status.NumberAvailable == nodes, nil
	})
	if wait.Interrupted(err) {
		return fmt.Errorf("%w: DaemonSet %s wants %d pods, status is desired=%d ready=%d available=%d",
			err, kubeletMetricsFixtureName, nodes, status.DesiredNumberScheduled, status.NumberReady, status.NumberAvailable)
	}
	return err
}

func deleteKubeletMetricsFixture(ctx context.Context) error {
	err := k.AppsV1().DaemonSets(*namespace).Delete(ctx, kubeletMetricsFixtureName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	selector := "app.kubernetes.io/name=" + kubeletMetricsFixtureName
	return wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := k.AppsV1().DaemonSets(*namespace).Get(ctx, kubeletMetricsFixtureName, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		pods, listErr := k.CoreV1().Pods(*namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if listErr != nil {
			return false, listErr
		}
		return apierrors.IsNotFound(err) && len(pods.Items) == 0, nil
	})
}

func waitForKubeletMetricNodes(ctx context.Context, want []string) error {
	kubeletQuery := "count by (node) (kubelet_active_pods > 0)"
	cadvisorQuery := fmt.Sprintf(
		`max by (node) (container_memory_working_set_bytes{namespace=%q,pod=~%q,container="memory"}) > 0`,
		*namespace, kubeletMetricsFixtureName+"-.+")

	var gotKubelet, gotCadvisor []string
	var lastErr error
	err := wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		gotKubelet, lastErr = prometheusMetricNodes(ctx, kubeletQuery)
		if lastErr != nil {
			return false, nil
		}
		gotCadvisor, lastErr = prometheusMetricNodes(ctx, cadvisorQuery)
		if lastErr != nil {
			return false, nil
		}
		return coversValues(gotKubelet, want) && coversValues(gotCadvisor, want), nil
	})
	if wait.Interrupted(err) {
		return fmt.Errorf("%w: want nodes %v; kubelet_active_pods nodes %v; container_memory_working_set_bytes nodes %v; last query error: %v",
			err, want, gotKubelet, gotCadvisor, lastErr)
	}
	return err
}

// coversValues permits extra series while requiring every expected label value.
func coversValues(got, want []string) bool {
	found := make(map[string]bool, len(got))
	for _, value := range got {
		found[value] = true
	}
	for _, expected := range want {
		if !found[expected] {
			return false
		}
	}
	return true
}

func prometheusMetricNodes(ctx context.Context, expression string) ([]string, error) {
	return prometheusMetricLabelValues(ctx, expression, "node")
}

func prometheusMetricLabelValues(ctx context.Context, expression, label string) ([]string, error) {
	body, err := k.ServiceEndpointQuery(ctx, *namespace, "lgtm", "9090", "/api/v1/query", map[string]string{"query": expression})
	if err != nil {
		return nil, fmt.Errorf("querying Prometheus for %q: %w", expression, err)
	}
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding Prometheus response for %q: %w", expression, err)
	}
	if response.Status != "success" {
		return nil, fmt.Errorf("Prometheus query %q returned status %q: %s", expression, response.Status, response.Error)
	}

	values := make([]string, 0, len(response.Data.Result))
	for _, result := range response.Data.Result {
		if value := result.Metric[label]; value != "" {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return slices.Compact(values), nil
}
