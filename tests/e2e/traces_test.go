//go:build e2e && (e2e_release_shared || !e2e_release_owned)

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// TestTraces sends a fresh, uniquely identified span from a disposable pod to
// an installed node Collector's OTLP/HTTP receiver. The normal pipeline must
// forward it through the trace gateway to LGTM's Tempo, preserving its service,
// span identity and timestamps and adding the Collector's node attribute.
func TestTraces(t *testing.T) {
	readyRelease(t.Context(), t)
	ctx, cancel := deadline(t.Context())
	defer cancel()

	collectors, err := k.CoreV1().Pods(*namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=nktp-daemonset-collector",
	})
	require.NoError(t, err, "listing node Collectors")
	var collector *corev1.Pod
	for i := range collectors.Items {
		pod := &collectors.Items[i]
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				collector = pod
				break
			}
		}
		if collector != nil {
			break
		}
	}
	require.NotNil(t, collector, "a running, ready node Collector that is not terminating must exist")
	require.NotEmpty(t, collector.Status.PodIP)
	require.NotEmpty(t, collector.Spec.NodeName)

	traceBytes := make([]byte, 16)
	spanBytes := make([]byte, 8)
	_, err = rand.Read(traceBytes)
	require.NoError(t, err)
	_, err = rand.Read(spanBytes)
	require.NoError(t, err)
	traceID, spanID := hex.EncodeToString(traceBytes), hex.EncodeToString(spanBytes)
	start := time.Now().UnixNano()
	end := start + int64(time.Millisecond)
	const service = "nktp-e2e-traces"
	payload := fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":%q}}]},"scopeSpans":[{"scope":{"name":"nktp-e2e"},"spans":[{"traceId":%q,"spanId":%q,"name":"trace-delivery","kind":1,"startTimeUnixNano":"%d","endTimeUnixNano":"%d"}]}]}]}`,
		service, traceID, spanID, start, end)

	template := passiveFixturePodTemplate(service)
	template.Spec.RestartPolicy = corev1.RestartPolicyNever
	template.Spec.NodeName = collector.Spec.NodeName
	generator := &template.Spec.Containers[0]
	generator.Name = "generator"
	generator.Env = []corev1.EnvVar{
		{Name: "PAYLOAD", Value: payload},
		{Name: "ENDPOINT", Value: "http://" + net.JoinHostPort(collector.Status.PodIP, "4318") + "/v1/traces"},
	}
	// Retry transient receiver failures; terminate after an accepted post so the
	// same span is not continuously ingested while waiting for backend delivery.
	generator.Command = []string{"/bin/sh", "-ec", `
until wget -q -T 5 -O - --header 'Content-Type: application/json' --post-data "$PAYLOAD" "$ENDPOINT"; do
  sleep 2
done
`}
	pod, err := k.CoreV1().Pods(*namespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: service + "-", Labels: template.Labels},
		Spec:       template.Spec,
	}, metav1.CreateOptions{})
	require.NoError(t, err, "creating trace generator")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := deadline(context.WithoutCancel(t.Context()))
		defer cleanupCancel()
		if t.Failed() {
			current, err := k.CoreV1().Pods(*namespace).Get(cleanupCtx, pod.Name, metav1.GetOptions{})
			if err == nil {
				t.Logf("trace generator %s: %+v", pod.Name, current.Status)
			}
			logs, err := k.CoreV1().Pods(*namespace).GetLogs(pod.Name, &corev1.PodLogOptions{LimitBytes: new(int64(16 << 10))}).DoRaw(cleanupCtx)
			t.Logf("trace generator logs: error=%v\n%s", err, logs)
		}
		err := k.CoreV1().Pods(*namespace).Delete(cleanupCtx, pod.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &pod.UID},
		})
		if !apierrors.IsNotFound(err) {
			assert.NoError(t, err, "deleting trace generator")
		}
		assert.NoError(t, wait.PollUntilContextCancel(cleanupCtx, time.Second, true, func(ctx context.Context) (bool, error) {
			_, err := k.CoreV1().Pods(*namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}), "waiting for trace generator removal")
	})

	t.Logf("waiting for trace %s from %s through Collector %s on node %s", traceID, pod.Name, collector.Name, collector.Spec.NodeName)
	var lastErr error
	err = wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		current, err := k.CoreV1().Pods(*namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if current.Status.Phase == corev1.PodFailed {
			return false, fmt.Errorf("trace generator failed: %+v", current.Status)
		}
		if current.Status.Phase != corev1.PodSucceeded {
			return false, nil
		}
		// Grafana proxies Tempo without another LGTM Service port.
		// Tempo returns base64 IDs; the generator sends hex OTLP/JSON IDs.
		// https://grafana.com/docs/tempo/latest/api_docs/pushing-spans-with-http/
		body, queryErr := k.ServiceEndpoint(ctx, *namespace, "lgtm", "3000",
			"/api/datasources/proxy/uid/tempo/api/v2/traces/"+traceID)
		lastErr = queryErr
		if queryErr != nil {
			return false, nil // A trace is initially absent while export completes.
		}
		var response struct {
			Trace struct {
				ResourceSpans []struct {
					Resource struct {
						Attributes []struct {
							Key   string `json:"key"`
							Value struct {
								StringValue string `json:"stringValue"`
							} `json:"value"`
						} `json:"attributes"`
					} `json:"resource"`
					ScopeSpans []struct {
						Spans []struct {
							TraceID string `json:"traceId"`
							SpanID  string `json:"spanId"`
							Name    string `json:"name"`
							Start   string `json:"startTimeUnixNano"`
							End     string `json:"endTimeUnixNano"`
						} `json:"spans"`
					} `json:"scopeSpans"`
				} `json:"resourceSpans"`
			} `json:"trace"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return false, fmt.Errorf("decoding Tempo trace %s: %w", traceID, err)
		}
		for _, resource := range response.Trace.ResourceSpans {
			attributes := map[string]string{}
			for _, attribute := range resource.Resource.Attributes {
				attributes[attribute.Key] = attribute.Value.StringValue
			}
			if attributes["service.name"] != service || attributes["k8s.node.name"] != collector.Spec.NodeName {
				continue
			}
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					if span.TraceID == base64.StdEncoding.EncodeToString(traceBytes) &&
						span.SpanID == base64.StdEncoding.EncodeToString(spanBytes) && span.Name == "trace-delivery" &&
						span.Start == fmt.Sprint(start) && span.End == fmt.Sprint(end) {
						return true, nil
					}
				}
			}
		}
		lastErr = fmt.Errorf("Tempo returned trace %s without the expected span and resource attributes: %s", traceID, body)
		return false, nil
	})
	require.NoError(t, err, "waiting for trace %s in Tempo; last query error: %v", traceID, lastErr)
}
