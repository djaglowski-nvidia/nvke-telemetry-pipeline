// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestTraces sends a fresh, uniquely identified span from a disposable pod to
// an installed node Collector's OTLP/HTTP receiver. The normal pipeline must
// forward it through the trace gateway to LGTM's Tempo, preserving its service,
// span identity and timestamps and adding the Collector's node attribute.
func TestTraces(t *testing.T) {
	const service = "nktp-e2e-traces"
	namespace := testenv.EnvConf().Namespace()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: service + "-", Namespace: namespace},
	}
	traceBytes := make([]byte, 16)
	spanBytes := make([]byte, 8)
	var traceID, spanID string
	var start, end int64
	var collector *corev1.Pod
	var proxyClient coreclient.CoreV1Interface
	var cleanup func()

	f := features.New("Trace delivery").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			client := c.Client()
			r := client.Resources(namespace)
			var err error
			proxyClient, err = coreclient.NewForConfig(client.RESTConfig())
			require.NoError(t, err, "creating the proxy and pod-log client")

			var collectors corev1.PodList
			require.NoError(t, r.List(setupCtx, &collectors,
				resources.WithLabelSelector("app.kubernetes.io/name=nktp-daemonset-collector")), "listing node Collectors")
			for i := range collectors.Items {
				candidate := &collectors.Items[i]
				if candidate.Status.Phase != corev1.PodRunning || candidate.DeletionTimestamp != nil {
					continue
				}
				for _, condition := range candidate.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						collector = candidate
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

			_, err = rand.Read(traceBytes)
			require.NoError(t, err)
			_, err = rand.Read(spanBytes)
			require.NoError(t, err)
			traceID, spanID = hex.EncodeToString(traceBytes), hex.EncodeToString(spanBytes)
			start = time.Now().UnixNano()
			end = start + int64(time.Millisecond)
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
			// Retry transient receiver failures, but stop after an accepted post.
			generator.Command = []string{"/bin/sh", "-ec", `
until wget -q -T 5 -O - --header 'Content-Type: application/json' --post-data "$PAYLOAD" "$ENDPOINT"; do
  sleep 2
done
`}
			pod.Labels = template.Labels
			pod.Spec = template.Spec
			require.NoError(t, r.Create(setupCtx, pod), "creating trace generator")
			cleanup = sync.OnceFunc(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
				defer cleanupCancel()
				if t.Failed() {
					var current corev1.Pod
					if err := r.Get(cleanupCtx, pod.Name, namespace, &current); err == nil {
						t.Logf("trace generator %s: %+v", pod.Name, current.Status)
					}
					logs, err := proxyClient.Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
						LimitBytes: new(int64(16 << 10)),
					}).DoRaw(cleanupCtx)
					t.Logf("trace generator logs: error=%v\n%s", err, logs)
				}
				err := r.Delete(cleanupCtx, pod, func(opts *metav1.DeleteOptions) {
					opts.Preconditions = &metav1.Preconditions{UID: &pod.UID}
				})
				if !apierrors.IsNotFound(err) {
					assert.NoError(t, err, "deleting trace generator")
				}
				assert.NoError(t, wait.For(func(ctx context.Context) (bool, error) {
					var current corev1.Pod
					err := r.Get(ctx, pod.Name, namespace, &current)
					if apierrors.IsNotFound(err) {
						return true, nil
					}
					return false, err
				}, wait.WithContext(cleanupCtx), wait.WithTimeout(0), wait.WithInterval(time.Second), wait.WithImmediate()),
					"waiting for trace generator removal")
			})
			// Keep cleanup on fatal failures even if framework fail-fast skips teardown.
			t.Cleanup(cleanup)
			return ctx
		}).
		Assess("Trace reaches Tempo with its span and resource attributes preserved", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			r := c.Client().Resources(namespace)
			t.Logf("waiting for trace %s from %s through Collector %s on node %s", traceID, pod.Name, collector.Name, collector.Spec.NodeName)
			var lastErr error
			err := wait.For(func(ctx context.Context) (bool, error) {
				var current corev1.Pod
				if err := r.Get(ctx, pod.Name, namespace, &current); err != nil {
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
				body, queryErr := proxyClient.Services(namespace).ProxyGet("http", "lgtm", "3000",
					"/api/datasources/proxy/uid/tempo/api/v2/traces/"+traceID, nil).DoRaw(ctx)
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
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			require.NoError(t, err, "waiting for trace %s in Tempo; last query error: %v", traceID, lastErr)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			if cleanup != nil {
				cleanup()
			}
			return ctx
		}).
		Feature()
	testenv.Test(t, f)
}
