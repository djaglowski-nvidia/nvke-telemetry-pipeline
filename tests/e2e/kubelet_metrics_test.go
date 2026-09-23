//go:build e2e

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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/NVIDIA-dev/nktp/tests/internal/kube"
)

const (
	kubeletMetricsFixtureName = "nktp-e2e-kubelet-metrics"
	// Topograph is an NVIDIA-owned, public, multi-platform Alpine image. Keep
	// the release tag for provenance and pin the registry index digest
	// so this test always runs the inspected image.
	kubeletMetricsFixtureImage = "ghcr.io/nvidia/topograph:v0.4.0@sha256:f227bf161464da4a23273a535914c98b33d26e5bf24bdf7f50159ab4b875b717"
)

// TestKubeletMetrics installs a fresh release and proves that every node sends
// both kubelet and cAdvisor metrics to Prometheus. It runs one passive,
// digest-pinned NVIDIA container on every node so cAdvisor has a real workload
// to observe, requires that workload's memory metric to be non-zero, and then
// removes both the fixture and the release.
func TestKubeletMetrics(t *testing.T) {
	if !*install {
		t.Skip("-nktp.install is unset, so validating the existing release remains read-only")
	}

	k := newKube(t)
	deployed := newChart(t, k)

	ctx, cancel := deadline(t)
	defer cancel()
	owned, err := deployed.InstallFresh(ctx)
	if owned {
		registerChartCleanup(t, deployed, k)
	}
	require.NoError(t, err, "installing a fresh release for the kubelet metrics test")

	verifyKubeletMetrics(t, k)
}

// verifyKubeletMetrics runs one passive container on every node, then waits
// for both the kubelet and cAdvisor scrape jobs to reach the metrics backend.
func verifyKubeletMetrics(t *testing.T, k *kube.Client) {
	t.Helper()

	ctx, cancel := deadline(t)
	defer cancel()

	nodes, err := k.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "listing nodes for the kubelet metrics fixture")
	require.NotEmpty(t, nodes.Items, "the cluster must have a node to test")

	require.NoError(t, createKubeletMetricsFixture(ctx, k))
	defer func() {
		cleanupCtx, cleanupCancel := deadline(t)
		defer cleanupCancel()
		assert.NoError(t, deleteKubeletMetricsFixture(cleanupCtx, k), "removing the kubelet metrics fixture")
	}()
	require.NoError(t, waitForKubeletMetricsFixture(ctx, k, int32(len(nodes.Items))))

	want := make([]string, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		want = append(want, node.Name)
	}
	sort.Strings(want)

	require.NoError(t, waitForKubeletMetricNodes(ctx, k, want),
		"waiting for kubelet and cAdvisor metrics from every node")
}

func createKubeletMetricsFixture(ctx context.Context, k *kube.Client) error {
	labels := map[string]string{"app.kubernetes.io/name": kubeletMetricsFixtureName}

	fixture := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: kubeletMetricsFixtureName, Namespace: *namespace},
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
						Image:           kubeletMetricsFixtureImage,
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

	if _, err := k.AppsV1().DaemonSets(*namespace).Create(ctx, fixture, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating DaemonSet %s: %w", kubeletMetricsFixtureName, err)
	}
	return nil
}

func waitForKubeletMetricsFixture(ctx context.Context, k *kube.Client, nodes int32) error {
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

func deleteKubeletMetricsFixture(ctx context.Context, k *kube.Client) error {
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

func waitForKubeletMetricNodes(ctx context.Context, k *kube.Client, want []string) error {
	kubeletQuery := "count by (node) (kubelet_active_pods > 0)"
	cadvisorQuery := fmt.Sprintf(
		`max by (node) (container_memory_working_set_bytes{namespace=%q,pod=~%q,container="memory"}) > 0`,
		*namespace, kubeletMetricsFixtureName+"-.+")

	var gotKubelet, gotCadvisor []string
	var lastErr error
	err := wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		gotKubelet, lastErr = prometheusMetricNodes(ctx, k, kubeletQuery)
		if lastErr != nil {
			return false, nil
		}
		gotCadvisor, lastErr = prometheusMetricNodes(ctx, k, cadvisorQuery)
		if lastErr != nil {
			return false, nil
		}
		return coversNodes(gotKubelet, want) && coversNodes(gotCadvisor, want), nil
	})
	if wait.Interrupted(err) {
		return fmt.Errorf("%w: want nodes %v; kubelet_active_pods nodes %v; container_memory_working_set_bytes nodes %v; last query error: %v",
			err, want, gotKubelet, gotCadvisor, lastErr)
	}
	return err
}

// coversNodes permits stale series for nodes that have left the cluster while
// requiring a current series for every node the API listed for this run.
func coversNodes(got, want []string) bool {
	found := make(map[string]bool, len(got))
	for _, node := range got {
		found[node] = true
	}
	for _, node := range want {
		if !found[node] {
			return false
		}
	}
	return true
}

func prometheusMetricNodes(ctx context.Context, k *kube.Client, expression string) ([]string, error) {
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

	nodes := make([]string, 0, len(response.Data.Result))
	for _, result := range response.Data.Result {
		if node := result.Metric["node"]; node != "" {
			nodes = append(nodes, node)
		}
	}
	sort.Strings(nodes)
	return slices.Compact(nodes), nil
}
