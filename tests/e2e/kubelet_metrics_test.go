// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	workload "github.com/NVIDIA-dev/nktp/tests/internal/funcs"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestKubeletMetrics proves that every node sends both kubelet and cAdvisor
// metrics to Prometheus. A passive, digest-pinned NVIDIA container on each node
// gives cAdvisor a real workload whose memory metric must be non-zero.
func TestKubeletMetrics(t *testing.T) {
	const name = "nktp-e2e-kubelet-metrics"
	const image = "ghcr.io/nvidia/topograph:v0.4.0@sha256:f227bf161464da4a23273a535914c98b33d26e5bf24bdf7f50159ab4b875b717"
	namespace := testenv.EnvConf().Namespace()
	labels := map[string]string{"app.kubernetes.io/name": name}
	fixture := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken:  new(false),
					TerminationGracePeriodSeconds: new(int64(1)),
					Tolerations:                   []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   new(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            "memory",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"/bin/sleep", "3600"},
						SecurityContext: &corev1.SecurityContext{
							RunAsUser:                new(int64(65532)),
							AllowPrivilegeEscalation: new(false),
							ReadOnlyRootFilesystem:   new(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}

	cleanup := func(ctx context.Context, c *envconf.Config) error {
		r := c.Client().Resources(namespace)
		if err := r.Delete(ctx, fixture); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting DaemonSet %s: %w", name, err)
		}
		return wait.For(func(ctx context.Context) (bool, error) {
			var current appsv1.DaemonSet
			err := r.Get(ctx, name, namespace, &current)
			if err != nil && !apierrors.IsNotFound(err) {
				return false, err
			}
			var pods corev1.PodList
			if listErr := r.List(ctx, &pods, resources.WithLabelSelector("app.kubernetes.io/name="+name)); listErr != nil {
				return false, listErr
			}
			return apierrors.IsNotFound(err) && len(pods.Items) == 0, nil
		}, wait.WithContext(ctx), wait.WithTimeout(0), wait.WithInterval(2*time.Second), wait.WithImmediate())
	}

	var want []string
	f := features.New("Kubelet and cAdvisor metrics").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			client := c.Client()
			var nodes corev1.NodeList
			require.NoError(t, client.Resources().List(setupCtx, &nodes), "listing nodes for the kubelet metrics fixture")
			require.NotEmpty(t, nodes.Items, "the cluster must have a node to test")
			for _, node := range nodes.Items {
				want = append(want, node.Name)
			}
			slices.Sort(want)
			require.NoError(t, cleanup(setupCtx, c), "removing any previous kubelet metrics fixture")
			require.NoError(t, client.Resources(namespace).Create(setupCtx, fixture), "creating the kubelet metrics fixture")
			return ctx
		}).
		Assess("Fixture is ready on every node", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			nodes := int32(len(want))
			require.NoError(t, workload.WaitForDaemonSet(waitCtx, workload.DaemonSetWaitOptions{
				Name: name, Namespace: namespace, Client: c.Client(), NodeCount: &nodes,
			}), "waiting for DaemonSet %s/%s to be ready on all %d nodes", namespace, name, nodes)
			return ctx
		}).
		Assess("Every node exports kubelet and cAdvisor metrics", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			// The framework's CRUD client has no Service proxy operation.
			client, err := coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			services := client.Services(namespace)
			kubeletQuery := "count by (node) (kubelet_active_pods > 0)"
			cadvisorQuery := fmt.Sprintf(
				`max by (node) (container_memory_working_set_bytes{namespace=%q,pod=~%q,container="memory"}) > 0`,
				namespace, name+"-.+")

			var gotKubelet, gotCadvisor []string
			var lastErr error
			err = wait.For(func(ctx context.Context) (bool, error) {
				gotKubelet, lastErr = prometheusMetricLabelValues(ctx, services, kubeletQuery, "node")
				if lastErr != nil {
					return false, nil
				}
				gotCadvisor, lastErr = prometheusMetricLabelValues(ctx, services, cadvisorQuery, "node")
				if lastErr != nil {
					return false, nil
				}
				return coversValues(gotKubelet, want) && coversValues(gotCadvisor, want), nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			require.NoError(t, err,
				"want nodes %v; kubelet_active_pods nodes %v; container_memory_working_set_bytes nodes %v; last query error: %v",
				want, gotKubelet, gotCadvisor, lastErr)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			require.NoError(t, cleanup(cleanupCtx, c), "removing the kubelet metrics fixture and its pods")
			return ctx
		}).
		Feature()
	testenv.Test(t, f)
}

// coversValues permits extra series while requiring every expected label value.
func coversValues(got, want []string) bool {
	for _, expected := range want {
		if !slices.Contains(got, expected) {
			return false
		}
	}
	return true
}

func prometheusMetricLabelValues(ctx context.Context, services coreclient.ServiceInterface, expression, label string) ([]string, error) {
	body, err := services.ProxyGet("http", "lgtm", "9090", "/api/v1/query", map[string]string{"query": expression}).DoRaw(ctx)
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
	slices.Sort(values)
	return slices.Compact(values), nil
}
