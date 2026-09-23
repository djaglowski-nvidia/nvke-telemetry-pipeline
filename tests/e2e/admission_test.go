//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/NVIDIA-dev/nktp/tests/internal/framework"
	"github.com/NVIDIA-dev/nktp/tests/internal/kube"
)

const collectorValidationWebhook = "vopentelemetrycollectorcreateupdatebeta.kb.io"

// TestAdmissionWebhook uses the configured release mode, submits a disposable
// OpenTelemetryCollector that is structurally valid but combines deployment
// mode with a target allocator, and requires the Operator's validating webhook
// to deny it. Matching the webhook's name in the denial proves that the API
// server reached the webhook, rather than rejecting the object through CRD
// schema validation or failing to connect.
func TestAdmissionWebhook(t *testing.T) {
	withRelease(t, verifyAdmissionWebhook)
}

func verifyAdmissionWebhook(t *testing.T, k *kube.Client) {
	t.Helper()
	name := "nktp-e2e-invalid-webhook-probe"
	invalid := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "opentelemetry.io/v1beta1",
		"kind":       "OpenTelemetryCollector",
		"metadata": map[string]any{
			"name":      name,
			"namespace": *namespace,
		},
		"spec": map[string]any{
			"mode": "deployment",
			"config": map[string]any{
				"receivers": map[string]any{
					"otlp": map[string]any{
						"protocols": map[string]any{
							"grpc": map[string]any{},
						},
					},
				},
				"exporters": map[string]any{
					"debug": map[string]any{},
				},
				"service": map[string]any{
					"pipelines": map[string]any{
						"traces": map[string]any{
							"receivers": []any{"otlp"},
							"exporters": []any{"debug"},
						},
					},
				},
			},
			"targetAllocator": map[string]any{
				"enabled": true,
			},
		},
	}}

	ctx, cancel := deadline(t)
	defer cancel()
	_, err := k.CreateCustomResource(ctx, *namespace, framework.Collectors, invalid)
	if err == nil {
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), *timeout+30*time.Second)
			defer cleanupCancel()
			assert.NoError(t, k.DeleteCustomResource(cleanupCtx, *namespace, framework.Collectors, name), "deleting invalid Collector %s", name)
		})
	}
	require.Error(t, err, "an invalid Collector should be denied")
	assert.ErrorContains(t, err, `admission webhook "`+collectorValidationWebhook+`" denied the request`)
}
