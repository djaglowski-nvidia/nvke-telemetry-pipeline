//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/NVIDIA-dev/nktp/tests/internal/framework"
	"github.com/NVIDIA-dev/nktp/tests/internal/kube"
)

var certificates = schema.GroupVersionResource{
	Group: "cert-manager.io", Version: "v1", Resource: "certificates",
}

var targetAllocatorMTLSCases = []struct {
	collector   string
	serviceName string
}{
	{collector: "nktp-daemonset", serviceName: "nktp-targetallocator"},
	{collector: "nktp-large-target-metrics", serviceName: "nktp-large-target-metrics-targetallocator"},
	{collector: "nktp-watchdog", serviceName: "nktp-watchdog-targetallocator"},
}

// TestTargetAllocatorMTLS uses the configured release mode and verifies the complete
// cert-manager path for every Collector/Target Allocator pair. It checks the
// requested CR settings, all generated certificates, and each HTTPS service.
// Finally it waits for each allocator's request-duration count to increase for
// the Collector target endpoint. That metric is recorded inside the HTTPS
// handler, so a new request proves the Collector completed mTLS and reached
// the application rather than merely finding a ready certificate.
func TestTargetAllocatorMTLS(t *testing.T) {
	withRelease(t, verifyTargetAllocatorMTLS)
}

func verifyTargetAllocatorMTLS(t *testing.T, k *kube.Client) {
	t.Helper()
	ctx, cancel := deadline(t)
	defer cancel()
	var baseline map[string]float64
	var lastErr error
	err := wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		baseline, lastErr = targetAllocatorRequestCounts(ctx, k)
		return lastErr == nil, nil
	})
	require.NoError(t, err, "reading Target Allocator request counts before checking for new requests: %v", lastErr)

	collectors, err := k.CustomResources(ctx, *namespace, framework.Collectors)
	require.NoError(t, err)
	collectorsByName := resourcesByName(collectors)

	issued, err := k.CustomResources(ctx, *namespace, certificates)
	require.NoError(t, err)
	certificatesByName := resourcesByName(issued)

	for _, tc := range targetAllocatorMTLSCases {
		collector, ok := collectorsByName[tc.collector]
		if assert.True(t, ok, "Collector %s should exist", tc.collector) {
			enabled, found, nestedErr := unstructured.NestedBool(collector.Object, "spec", "targetAllocator", "mtls", "enabled")
			require.NoError(t, nestedErr)
			assert.True(t, found && enabled, "%s should enable Target Allocator mTLS", tc.collector)

			useCertManager, found, nestedErr := unstructured.NestedBool(collector.Object, "spec", "targetAllocator", "mtls", "useCertManager")
			require.NoError(t, nestedErr)
			assert.True(t, found && useCertManager, "%s should explicitly use cert-manager", tc.collector)
		}

		for _, suffix := range []string{"ca-cert", "ta-server-cert", "ta-client-cert"} {
			name := tc.collector + "-" + suffix
			certificate, ok := certificatesByName[name]
			if assert.True(t, ok, "Certificate %s should exist", name) {
				assert.True(t, resourceConditionTrue(certificate, "Ready"), "Certificate %s should be Ready", name)
			}
		}

		service, serviceErr := k.CoreV1().Services(*namespace).Get(ctx, tc.collector+"-targetallocator", metav1.GetOptions{})
		require.NoError(t, serviceErr)
		assert.True(t, hasServicePort(service, "targetallocation-https", 443),
			"%s should expose its mTLS endpoint", service.Name)
	}

	wantServices := make([]string, 0, len(targetAllocatorMTLSCases))
	for _, tc := range targetAllocatorMTLSCases {
		wantServices = append(wantServices, tc.serviceName)
	}
	require.NoError(t, waitForTargetAllocatorRequests(ctx, k, wantServices, baseline),
		"waiting for every Collector to request targets over mTLS")
}

func resourcesByName(resources []unstructured.Unstructured) map[string]unstructured.Unstructured {
	byName := make(map[string]unstructured.Unstructured, len(resources))
	for _, resource := range resources {
		byName[resource.GetName()] = resource
	}
	return byName
}

func resourceConditionTrue(resource unstructured.Unstructured, conditionType string) bool {
	conditions, found, err := unstructured.NestedSlice(resource.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if ok && condition["type"] == conditionType && condition["status"] == "True" {
			return true
		}
	}
	return false
}

func hasServicePort(service *corev1.Service, name string, port int32) bool {
	for _, candidate := range service.Spec.Ports {
		if candidate.Name == name && candidate.Port == port {
			return true
		}
	}
	return false
}

const targetAllocatorRequestQuery = `sum by (service_name) (opentelemetry_allocator_http_duration_seconds_count{path="/jobs/:job_id/targets"})`

func waitForTargetAllocatorRequests(ctx context.Context, k *kube.Client, want []string, baseline map[string]float64) error {
	var got map[string]float64
	var lastErr error
	err := wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		got, lastErr = targetAllocatorRequestCounts(ctx, k)
		if lastErr != nil {
			return false, nil
		}
		for _, service := range want {
			if got[service] <= baseline[service] {
				return false, nil
			}
		}
		return true, nil
	})
	if wait.Interrupted(err) {
		return fmt.Errorf("%w: want allocator services %v to increase from %v, got %v; last query error: %v", err, want, baseline, got, lastErr)
	}
	return err
}

func targetAllocatorRequestCounts(ctx context.Context, k *kube.Client) (map[string]float64, error) {
	body, err := k.ServiceEndpointQuery(ctx, *namespace, "lgtm", "9090", "/api/v1/query", map[string]string{"query": targetAllocatorRequestQuery})
	if err != nil {
		return nil, fmt.Errorf("querying Prometheus for Target Allocator requests: %w", err)
	}
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string  `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding Target Allocator request counts: %w", err)
	}
	if response.Status != "success" {
		return nil, fmt.Errorf("Prometheus Target Allocator request query returned status %q: %s", response.Status, response.Error)
	}

	counts := make(map[string]float64, len(response.Data.Result))
	for _, result := range response.Data.Result {
		var value string
		if err := json.Unmarshal(result.Value[1], &value); err != nil {
			return nil, fmt.Errorf("decoding Target Allocator request count for %q: %w", result.Metric["service_name"], err)
		}
		count, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing Target Allocator request count for %q: %w", result.Metric["service_name"], err)
		}
		if math.IsNaN(count) || math.IsInf(count, 0) {
			return nil, fmt.Errorf("invalid Target Allocator request count %q for %q", value, result.Metric["service_name"])
		}
		counts[result.Metric["service_name"]] = count
	}
	return counts, nil
}
