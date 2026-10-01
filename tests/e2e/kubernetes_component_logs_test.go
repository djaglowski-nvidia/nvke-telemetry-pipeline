//go:build e2e && (e2e_release_shared || !e2e_release_owned)

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA-dev/nktp/tests/internal/kube"
)

const componentLogsFixtureName = "nktp-e2e-component-logs"

type componentLogCase struct {
	name       string
	expression string
}

// TestKubernetesComponentLogs checks that API audit events and control plane
// pod logs reach LGTM with their expected classifications, and that CoreDNS and
// kube-proxy pod logs retain their Kubernetes attributes. A one-replica
// Deployment drives scheduler and controller activity and repeatedly resolves
// a temporary Service. Cleanup removes the Service, Deployment, ReplicaSet and
// pods. Namespace reads generate API audit events and API-server request logs.
//
// The cluster must enable API audit logging at the chart's configured path.
// API-server verbosity 3 emits HTTP request logs; scheduler verbosity 2 emits
// "Successfully bound pod to node"; controller-manager verbosity 4 emits
// "Controller created pod". Kube-proxy verbosity 4 emits "Service updated ports"
// when the temporary Service is created or its port changes. CoreDNS's log
// plugin logs the fixture's DNS queries. The test temporarily points the CoreDNS
// Deployment at a copy of its ConfigMap with query logging enabled, restores
// the original reference, then deletes the copy. The e2e Kind config supplies
// the verbosity levels; other clusters need equivalent settings. Existing
// Kind clusters must be recreated to pick up verbosity changes.
//
// A plaintext HTTP request to etcd's TLS client port produces a rejection log
// without credentials or data operations. This requires pods/proxy access and
// an etcd pod labeled component=etcd with a TLS listener reachable on port 2379.
func TestKubernetesComponentLogs(t *testing.T) {
	k := newKubeWithReadyRelease(t.Context(), t)

	ctx, cancel := deadline(t.Context())
	defer cancel()
	started := time.Now()

	etcdPods, err := k.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: "component=etcd"})
	require.NoError(t, err, "listing etcd pods for the TLS rejection probe")
	require.NotEmpty(t, etcdPods.Items, "the cluster must have an etcd pod to test")
	createCoreDNSLogsFixture(ctx, t, k)
	createComponentLogsFixture(ctx, t, k)

	// Loki normalizes dots to underscores. Pipeline filters also work
	// for attributes stored as structured metadata rather than labels.
	cases := []componentLogCase{
		{
			"kube-apiserver-audit",
			`{service_name="kube-apiserver"} | nvidia_telemetry_class="k8s.api.audit" | json audit_kind="kind", audit_api_version="apiVersion" | audit_kind="Event" | audit_api_version="audit.k8s.io/v1"`,
		},
	}
	for _, component := range []string{"kube-apiserver", "etcd", "kube-scheduler", "kube-controller-manager"} {
		cases = append(cases, componentLogCase{
			component + "-pod",
			fmt.Sprintf(`{k8s_namespace_name="kube-system"} | k8s_container_name=%q | k8s_pod_name=~%q | nvidia_telemetry_class="k8s.control_plane"`, component, component+"-.+"),
		})
	}
	// These components are collected by the same pod filelog receiver but
	// are not assigned k8s.control_plane by the chart's classification rule.
	for _, component := range []string{"coredns", "kube-proxy"} {
		cases = append(cases, componentLogCase{
			component + "-pod",
			fmt.Sprintf(`{k8s_namespace_name="kube-system"} | k8s_container_name=%q | k8s_pod_name=~%q`, component, component+"-.+"),
		})
	}
	// Require the controlled stimuli, rather than unrelated startup messages.
	for i := range cases {
		switch cases[i].name {
		case "kube-apiserver-pod":
			cases[i].expression += fmt.Sprintf(` |= "HTTP" |= %q`, "/api/v1/namespaces/"+*namespace)
		case "kube-scheduler-pod":
			cases[i].expression += fmt.Sprintf(` |= "Successfully bound pod to node" |= %q`, componentLogsFixtureName)
		case "kube-controller-manager-pod":
			cases[i].expression += fmt.Sprintf(` |= "Controller created pod" |= %q`, componentLogsFixtureName)
		case "etcd-pod":
			cases[i].expression += ` |= "rejected connection on client endpoint"`
		case "coredns-pod":
			cases[i].expression += fmt.Sprintf(` |= %q`, componentLogsFixtureName+"."+*namespace+".svc.cluster.local.")
		case "kube-proxy-pod":
			cases[i].expression += fmt.Sprintf(` |= "Service updated ports" |= %q`, *namespace+"/"+componentLogsFixtureName)
		}
	}

	found := make([]bool, len(cases))
	queryErrors := make([]error, len(cases))
	lastWorkloadRefresh := time.Now()
	lastServiceRefresh := time.Now()
	var lastEtcdProbe time.Time
	err = wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		// Generate fresh audit events after the filelog receiver starts at EOF.
		// A read is sufficient; no fixture or cluster mutation is needed.
		_, err := k.CoreV1().Namespaces().Get(ctx, *namespace, metav1.GetOptions{})
		if err != nil {
			t.Logf("reading the component logs Namespace: %v", err)
		}

		complete := true
		for i, tc := range cases {
			if found[i] {
				continue
			}
			if ctx.Err() != nil {
				return false, nil
			}
			// Retry stimuli at most every 30 seconds in case filelog's first
			// read started at EOF after the initial messages were written.
			switch tc.name {
			case "kube-scheduler-pod", "kube-controller-manager-pod":
				if time.Since(lastWorkloadRefresh) >= 30*time.Second {
					lastWorkloadRefresh = time.Now()
					if err := k.CoreV1().Pods(*namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
						LabelSelector: "app.kubernetes.io/name=" + componentLogsFixtureName,
					}); err != nil {
						t.Logf("refreshing the component logs fixture: %v", err)
					}
				}
			case "etcd-pod":
				if time.Since(lastEtcdProbe) >= 30*time.Second {
					lastEtcdProbe = time.Now()
					for _, pod := range etcdPods.Items {
						probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
						// The proxy request is expected to fail: HTTP cannot
						// handshake with etcd's TLS endpoint. The delivered
						// rejection log, not the proxy error, proves success.
						_, _ = k.PodEndpoint(probeCtx, "kube-system", pod.Name, "2379", "/version")
						probeCancel()
					}
				}
			case "kube-proxy-pod":
				if time.Since(lastServiceRefresh) >= 30*time.Second {
					lastServiceRefresh = time.Now()
					service, err := k.CoreV1().Services(*namespace).Get(ctx, componentLogsFixtureName, metav1.GetOptions{})
					if err != nil {
						t.Logf("reading the component logs Service: %v", err)
					} else {
						// Change actual proxy configuration; metadata-only updates
						// do not produce "Service updated ports" messages.
						if service.Spec.Ports[0].Port == 8080 {
							service.Spec.Ports[0].Port = 8081
						} else {
							service.Spec.Ports[0].Port = 8080
						}
						if _, err := k.CoreV1().Services(*namespace).Update(ctx, service, metav1.UpdateOptions{}); err != nil {
							t.Logf("refreshing the component logs Service: %v", err)
						}
					}
				}
			}
			found[i], queryErrors[i] = lokiHasLogsSince(ctx, k, tc.expression, started)
			if ctx.Err() != nil {
				return false, nil
			}
			complete = complete && queryErrors[i] == nil && found[i]
		}
		return complete, nil
	})
	if err != nil {
		t.Logf("component log polling stopped after %s: %v", time.Since(started), err)
		logComponentLogsDiagnostics(context.WithoutCancel(t.Context()), t, k, cases, found, started)
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if assert.NoError(t, queryErrors[i], "querying %s logs in LGTM", tc.name) {
				assert.True(t, found[i], "no %s log in LGTM since %s matching %s",
					tc.name, started.Format(time.RFC3339), tc.expression)
			}
		})
	}
	if !wait.Interrupted(err) {
		assert.NoError(t, err, "polling Kubernetes component logs")
	}
}

// Diagnostics use their own deadline after polling times out. Summaries avoid
// application log bodies and unrelated healthy pods. All counters are cumulative
// since process startup; sent records prove delivery to the next hop, not to Loki.
func logComponentLogsDiagnostics(ctx context.Context, t *testing.T, k *kube.Client, cases []componentLogCase, found []bool, started time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	// Only the two collectors on this path and LGTM's internal collector matter.
	probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
	metrics, err := k.ServiceEndpointQuery(probeCtx, *namespace, "lgtm", "9090", "/api/v1/query", map[string]string{
		"query": `sum by (__name__, service_name, receiver, exporter) ({__name__=~"otelcol_(receiver_(accepted|refused)_log_records|exporter_(sent|send_failed)_log_records)(_total)?",service_name=~"nktp-daemonset|nktp-gateway-logs|otelcol-contrib"})`,
	})
	probeCancel()
	var counters struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string  `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err == nil {
		err = json.Unmarshal(metrics, &counters)
	}
	if err != nil || counters.Status != "success" {
		t.Logf("log diagnostics: collector counters unavailable: error=%v status=%s detail=%s", err, counters.Status, counters.Error)
	} else {
		for _, service := range []string{"nktp-daemonset", "nktp-gateway-logs", "otelcol-contrib"} {
			summary := map[string]string{}
			for _, result := range counters.Data.Result {
				if result.Metric["service_name"] != service {
					continue
				}
				component := result.Metric["receiver"]
				if component == "" {
					component = result.Metric["exporter"]
				}
				if component != "filelog" && component != "filelog/kube-api-audit" && component != "otlp" && component != "otlp_http" && component != "otlp_http/logs" {
					continue
				}
				name := strings.NewReplacer(
					"otelcol_receiver_accepted_log_records", "accepted",
					"otelcol_receiver_refused_log_records", "refused",
					"otelcol_exporter_sent_log_records", "sent",
					"otelcol_exporter_send_failed_log_records", "failed",
				).Replace(strings.TrimSuffix(result.Metric["__name__"], "_total"))
				var value string
				if json.Unmarshal(result.Value[1], &value) == nil {
					summary[component+":"+name] = value
				}
			}
			t.Logf("log diagnostics: cumulative counters %s %v", service, summary)
		}
	}

	// LGTM's collector can accept OTLP while Loki rejects every export. Its
	// own /ready and rejection counters expose that otherwise hidden final hop.
	probeCtx, probeCancel = context.WithTimeout(ctx, 3*time.Second)
	backend, err := k.CoreV1().Pods(*namespace).List(probeCtx, metav1.ListOptions{LabelSelector: "app=lgtm"})
	probeCancel()
	if err != nil || len(backend.Items) != 1 {
		t.Logf("log diagnostics: locating LGTM: error=%v", err)
	} else {
		pod := backend.Items[0]
		logLGTMIngestionDiagnostics(ctx, t, k, pod)
		probeCtx, probeCancel = context.WithTimeout(ctx, 3*time.Second)
		ready, readyErr := k.PodEndpoint(probeCtx, *namespace, pod.Name, "3100", "/ready")
		probeCancel()
		t.Logf("log diagnostics: Loki ready=%t error=%v", strings.TrimSpace(string(ready)) == "ready", readyErr)
		probeCtx, probeCancel = context.WithTimeout(ctx, 3*time.Second)
		body, metricsErr := k.PodEndpoint(probeCtx, *namespace, pod.Name, "3100", "/metrics")
		probeCancel()
		// Select only numeric values and known operational labels. Never emit
		// arbitrary stream labels, which can contain application attributes.
		summary := map[string]float64{}
		label := regexp.MustCompile(`(?:reason|status_code)="([^"]+)"`)
		ringState := regexp.MustCompile(`state="([^"]+)"`)
		for _, line := range strings.Split(string(body), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			value, parseErr := strconv.ParseFloat(fields[1], 64)
			if parseErr != nil {
				continue
			}
			switch {
			case strings.HasPrefix(line, "loki_discarded_samples_total{"):
				if match := label.FindStringSubmatch(line); len(match) == 2 {
					summary["rejected:"+match[1]] += value
				}
			case strings.HasPrefix(line, "loki_request_duration_seconds_count{") && strings.Contains(line, `route="otlp_v1_logs"`):
				if match := label.FindStringSubmatch(line); len(match) == 2 {
					summary["otlp_http:"+match[1]] += value
				}
			case strings.HasPrefix(line, "loki_distributor_lines_received_total{"):
				summary["received"] += value
			case strings.HasPrefix(line, "loki_ingester_memory_streams{"):
				summary["streams"] += value
			// Loki 3.7 reports disk-throttled pushes as "Ingester is shutting
			// down" even while /ready and /services remain healthy. The WAL
			// disk usage gauge is a fraction; print it as a percentage.
			case fields[0] == "loki_ingester_wal_disk_usage_percent":
				summary["wal_disk_usage_percent"] = value * 100
			case fields[0] == "loki_ingester_wal_disk_full_failures_total":
				summary["wal_disk_full_failures"] = value
			case strings.HasPrefix(line, "loki_distributor_ingester_appends_failures_total"):
				summary["ingester_append_failures"] += value
			case strings.HasPrefix(line, "cortex_ring_members{") && strings.Contains(line, `name="ingester"`):
				if match := ringState.FindStringSubmatch(line); len(match) == 2 {
					summary["ingester_ring:"+match[1]] += value
				}
			}
		}
		t.Logf("log diagnostics: Loki cumulative ingestion %v error=%v", summary, metricsErr)
	}

	// Deduplicate common selectors. Most cases share kube-system, so printing
	// the same empty query six times adds no evidence. Include label names to
	// expose an ingestion mapping mismatch without copying log payloads.
	probeCtx, probeCancel = context.WithTimeout(ctx, 3*time.Second)
	labels, labelsErr := k.ServiceEndpointQuery(probeCtx, *namespace, "lgtm", "3000",
		"/api/datasources/proxy/uid/loki/loki/api/v1/labels", map[string]string{
			"start": fmt.Sprint(started.UnixNano()), "end": fmt.Sprint(time.Now().UnixNano()),
		})
	probeCancel()
	var labelResponse struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if labelsErr == nil {
		labelsErr = json.Unmarshal(labels, &labelResponse)
	}
	t.Logf("log diagnostics: Loki index labels since test start=%v status=%s error=%v", labelResponse.Data, labelResponse.Status, labelsErr)
	seen := map[string]bool{}
	for i, tc := range cases {
		selector, _, _ := strings.Cut(tc.expression, "}")
		selector += "}"
		if found[i] || seen[selector] || ctx.Err() != nil {
			continue
		}
		seen[selector] = true
		probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
		present, err := lokiHasLogsSince(probeCtx, k, selector, started)
		probeCancel()
		t.Logf("log diagnostics: Loki selector=%s present=%t error=%v", selector, present, err)
	}

	// Count the controlled messages at their source instead of printing every
	// pod's status or total output, neither of which proves the stimulus fired.
	probeCtx, probeCancel = context.WithTimeout(ctx, 3*time.Second)
	pods, err := k.CoreV1().Pods("kube-system").List(probeCtx, metav1.ListOptions{})
	probeCancel()
	if err != nil {
		t.Logf("log diagnostics: source pods unavailable: %v", err)
		return
	}
	sources := map[string]int{}
	for i, tc := range cases {
		if found[i] || !strings.HasSuffix(tc.name, "-pod") {
			continue
		}
		component := strings.TrimSuffix(tc.name, "-pod")
		tokens := strings.Split(tc.expression, " |= ")[1:]
		for _, pod := range pods.Items {
			if !strings.HasPrefix(pod.Name, component+"-") {
				continue
			}
			probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
			body, err := k.CoreV1().Pods("kube-system").GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: component, SinceTime: &metav1.Time{Time: started},
				TailLines: new(int64(200)), LimitBytes: new(int64(65536)),
			}).DoRaw(probeCtx)
			probeCancel()
			if err != nil {
				t.Logf("log diagnostics: reading source %s: %v", pod.Name, err)
				continue
			}
			// Preserve a zero entry to distinguish no matches from no pod.
			sources[component] += 0
			for _, line := range strings.Split(string(body), "\n") {
				matches := len(tokens) > 0
				for _, token := range tokens {
					text, err := strconv.Unquote(token)
					matches = matches && err == nil && strings.Contains(line, text)
				}
				if matches {
					sources[component]++
				}
			}
		}
	}
	t.Logf("log diagnostics: matching messages in Kubernetes pod logs since test start=%v (not Loki; last 200 lines/64KiB per pod)", sources)
}

// Loki and its collector run as processes in the single lgtm container. The
// e2e values enable their process logs; no telemetry payload exporter is enabled.
// Keep only the most recent operational warnings/errors, including the previous
// container when it restarted, rather than dumping every backend's startup logs.
func logLGTMIngestionDiagnostics(ctx context.Context, t *testing.T, k *kube.Client, pod corev1.Pod) {
	t.Helper()
	restarted := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "lgtm" {
			continue
		}
		restarted = status.RestartCount > 0
		t.Logf("log diagnostics: LGTM ready=%t restarts=%d", status.Ready, status.RestartCount)
		if terminated := status.LastTerminationState.Terminated; terminated != nil {
			t.Logf("log diagnostics: LGTM previous exit=%d reason=%s finished=%s", terminated.ExitCode, terminated.Reason, terminated.FinishedAt.Time.Format(time.RFC3339))
		}
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
	services, err := k.PodEndpoint(probeCtx, pod.Namespace, pod.Name, "3100", "/services")
	probeCancel()
	// /services may be HTML. Retain only its text and bound the summary.
	text := strings.Join(strings.Fields(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(string(services), " ")), " ")
	if len(text) > 2048 {
		text = text[:2048] + " (truncated)"
	}
	t.Logf("log diagnostics: Loki services=%q error=%v", text, err)

	severity := regexp.MustCompile(`(?i)(\blevel=(warn|error)\b|\t(WARN|ERROR)\t|"level"\s*:\s*"(warn|error)")`)
	for _, previous := range []bool{false, true} {
		if previous && !restarted {
			continue
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
		body, err := k.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container: "lgtm", Previous: previous,
			TailLines: new(int64(400)), LimitBytes: new(int64(65536)),
		}).DoRaw(probeCtx)
		probeCancel()
		var messages []string
		for _, line := range strings.Split(string(body), "\n") {
			if severity.MatchString(line) {
				if len(line) > 2048 {
					line = line[:2048] + " (truncated)"
				}
				messages = append(messages, line)
			}
		}
		t.Logf("log diagnostics: LGTM previous=%t warnings/errors=%d (last 400 lines/64KiB) error=%v", previous, len(messages), err)
		for _, message := range messages[max(0, len(messages)-10):] {
			t.Logf("log diagnostics: LGTM previous=%t %s", previous, message)
		}
	}
}

// createCoreDNSLogsFixture preserves the cluster's DNS configuration and only
// adds query logging. A separate ConfigMap lets cleanup delete everything this
// test creates without deleting or overwriting the cluster-owned ConfigMap.
func createCoreDNSLogsFixture(ctx context.Context, t *testing.T, k *kube.Client) {
	t.Helper()
	deployment, err := k.AppsV1().Deployments("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
	require.NoError(t, err, "reading the CoreDNS Deployment")
	var volumeName string
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.ConfigMap != nil && volume.ConfigMap.Name == "coredns" {
			volumeName = volume.Name
			break
		}
	}
	require.NotEmpty(t, volumeName, "CoreDNS must mount the coredns ConfigMap")
	original, err := k.CoreV1().ConfigMaps("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
	require.NoError(t, err, "reading the CoreDNS ConfigMap")
	lines := strings.Split(original.Data["Corefile"], "\n")
	configured := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "log" {
			configured = true
			break
		}
	}
	if !configured {
		for i, line := range lines {
			if strings.TrimSpace(line) == "errors" {
				lines[i] = line + "\n    log"
				configured = true
				break
			}
		}
	}
	require.True(t, configured, "CoreDNS Corefile must have an errors or log directive")
	config := original.DeepCopy()
	fixture := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{GenerateName: componentLogsFixtureName + "-coredns-", Namespace: "kube-system"},
		Data:       config.Data,
		BinaryData: config.BinaryData,
	}
	fixture.Data["Corefile"] = strings.Join(lines, "\n")
	fixture, err = k.CoreV1().ConfigMaps("kube-system").Create(ctx, fixture, metav1.CreateOptions{})
	require.NoError(t, err, "creating the CoreDNS logs ConfigMap")
	// Register cleanup before changing the Deployment or waiting for rollout.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), *timeout+30*time.Second)
		defer cleanupCancel()
		if !assert.NoError(t, setCoreDNSConfigMap(cleanupCtx, k, volumeName, fixture.Name, original.Name), "restoring CoreDNS configuration") {
			return
		}
		// Keep the temporary ConfigMap until the original configuration is
		// serving again, so an incomplete rollout cannot strand DNS pods.
		if !assert.NoError(t, waitForCoreDNSRollout(cleanupCtx, k), "waiting for restored CoreDNS") {
			return
		}
		err := k.CoreV1().ConfigMaps("kube-system").Delete(cleanupCtx, fixture.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &fixture.UID},
		})
		if !apierrors.IsNotFound(err) {
			assert.NoError(t, err, "deleting the CoreDNS logs ConfigMap")
		}
	})
	require.NoError(t, setCoreDNSConfigMap(ctx, k, volumeName, original.Name, fixture.Name), "enabling CoreDNS query logs")
	require.NoError(t, waitForCoreDNSRollout(ctx, k), "waiting for CoreDNS query logging")
}

func setCoreDNSConfigMap(ctx context.Context, k *kube.Client, volumeName, from, to string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := k.AppsV1().Deployments("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
		if err != nil {
			return err
		}
		for i := range deployment.Spec.Template.Spec.Volumes {
			volume := &deployment.Spec.Template.Spec.Volumes[i]
			if volume.Name != volumeName || volume.ConfigMap == nil {
				continue
			}
			if volume.ConfigMap.Name == to {
				return nil
			}
			if volume.ConfigMap.Name != from {
				return fmt.Errorf("CoreDNS volume %s now references %s, expected %s", volumeName, volume.ConfigMap.Name, from)
			}
			volume.ConfigMap.Name = to
			_, err = k.AppsV1().Deployments("kube-system").Update(ctx, deployment, metav1.UpdateOptions{})
			return err
		}
		return fmt.Errorf("CoreDNS ConfigMap volume %s is missing", volumeName)
	})
}

func waitForCoreDNSRollout(ctx context.Context, k *kube.Client) error {
	return wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		deployment, err := k.AppsV1().Deployments("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		replicas := int32(1)
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}
		return deployment.Status.ObservedGeneration >= deployment.Generation &&
			deployment.Status.UpdatedReplicas == replicas && deployment.Status.Replicas == replicas &&
			deployment.Status.AvailableReplicas == replicas, nil
	})
}

func createComponentLogsFixture(ctx context.Context, t *testing.T, k *kube.Client) {
	t.Helper()
	fixture := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: componentLogsFixtureName, Namespace: *namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": componentLogsFixtureName}},
			Template: passiveFixturePodTemplate(componentLogsFixtureName),
		},
	}
	// Use the existing Alpine fixture's BusyBox rather than another image.
	// Kind's cluster domain is cluster.local. The trailing dot prevents DNS
	// search-path expansion, and repeated queries cover filelog's EOF startup.
	fixture.Spec.Template.Spec.Containers[0].Command = []string{
		"/bin/sh", "-ec",
		`while true; do /bin/busybox nslookup "$FIXTURE_DNS_NAME" >/dev/null 2>&1 || true; sleep 5; done`,
	}
	fixture.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{
		Name: "FIXTURE_DNS_NAME", Value: componentLogsFixtureName + "." + *namespace + ".svc.cluster.local.",
	}}
	fixture, err := k.AppsV1().Deployments(*namespace).Create(ctx, fixture, metav1.CreateOptions{})
	require.NoError(t, err, "creating the component logs fixture")
	// Register ownership before waiting: a failed readiness assertion must
	// still remove the Deployment and its ReplicaSets and pods. UID
	// preconditions protect objects replaced after this test created them.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), *timeout+30*time.Second)
		defer cleanupCancel()
		assert.NoError(t, deleteComponentLogsDeployment(cleanupCtx, k, fixture), "removing the component logs Deployment")
	})
	service, err := k.CoreV1().Services(*namespace).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: componentLogsFixtureName, Namespace: *namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app.kubernetes.io/name": componentLogsFixtureName},
			Ports:    []corev1.ServicePort{{Name: "fixture", Port: 8080}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "creating the component logs Service")
	// Only a successful Create registers Service cleanup. A name collision
	// leaves the existing Service alone. Cleanup runs before the Deployment's.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), *timeout+30*time.Second)
		defer cleanupCancel()
		assert.NoError(t, deleteComponentLogsService(cleanupCtx, k, service), "removing the component logs Service")
	})
	var status appsv1.DeploymentStatus
	err = wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		current, err := k.AppsV1().Deployments(*namespace).Get(ctx, componentLogsFixtureName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		status = current.Status
		return status.ObservedGeneration >= current.Generation &&
			status.UpdatedReplicas == 1 && status.AvailableReplicas == 1, nil
	})
	require.NoError(t, err, "waiting for the component logs fixture; last Deployment status: %+v", status)
}

func deleteComponentLogsService(ctx context.Context, k *kube.Client, service *corev1.Service) error {
	err := k.CoreV1().Services(service.Namespace).Delete(ctx, service.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &service.UID},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func deleteComponentLogsDeployment(ctx context.Context, k *kube.Client, deployment *appsv1.Deployment) error {
	foreground := metav1.DeletePropagationForeground
	err := k.AppsV1().Deployments(deployment.Namespace).Delete(ctx, deployment.Name, metav1.DeleteOptions{
		PropagationPolicy: &foreground,
		Preconditions:     &metav1.Preconditions{UID: &deployment.UID},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	selector := "app.kubernetes.io/name=" + componentLogsFixtureName
	return wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := k.AppsV1().Deployments(*namespace).Get(ctx, componentLogsFixtureName, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		pods, listErr := k.CoreV1().Pods(*namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if listErr != nil {
			return false, listErr
		}
		replicaSets, listErr := k.AppsV1().ReplicaSets(*namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if listErr != nil {
			return false, listErr
		}
		return apierrors.IsNotFound(err) && len(pods.Items) == 0 && len(replicaSets.Items) == 0, nil
	})
}

func lokiHasLogsSince(ctx context.Context, k *kube.Client, expression string, started time.Time) (bool, error) {
	// LGTM exposes Grafana, whose provisioned Loki datasource proxies the
	// internal Loki endpoint without requiring another chart service port.
	now := time.Now()
	body, err := k.ServiceEndpointQuery(ctx, *namespace, "lgtm", "3000",
		"/api/datasources/proxy/uid/loki/loki/api/v1/query_range", map[string]string{
			"query": expression,
			"start": fmt.Sprint(started.UnixNano()),
			"end":   fmt.Sprint(now.UnixNano()),
			"limit": "1",
		})
	if err != nil {
		return false, fmt.Errorf("querying Loki for %q: %w", expression, err)
	}
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return false, fmt.Errorf("decoding Loki response for %q: %w", expression, err)
	}
	if response.Status != "success" {
		return false, fmt.Errorf("Loki query %q returned status %q: %s", expression, response.Status, response.Error)
	}
	if response.Data.ResultType != "streams" {
		return false, fmt.Errorf("Loki query %q returned result type %q, want streams", expression, response.Data.ResultType)
	}
	for _, result := range response.Data.Result {
		if len(result.Values) > 0 {
			return true, nil
		}
	}
	return false, nil
}
