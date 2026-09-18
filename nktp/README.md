# NKTP Helm Chart

NKTP is how NVIDIA runs OpenTelemetry on Kubernetes.

## OpenTelemetry Topology

TODO

### Watchdog

The watchdog receives internal metrics, logs and traces from NKTP collectors over
OTLP/HTTP at `http://nktp-watchdog-collector:4318` in the Helm release namespace.
It forwards received telemetry and exports its own telemetry to LGTM by default.

The bundled OpenTelemetry Operator manages the watchdog. Autoscaling requires the
Kubernetes resource metrics API, commonly provided by metrics-server.
Setting `watchdog.kubernetes.targetAllocator.enabled` to `false` will cause Helm
rendering to fail.

Configure the Collector through `watchdog.otelcol.config`.

Use `watchdog.kubernetes` for Collector CR settings and `watchdog.otelcol.image`
to override the global `otelcol_image`.
Include `GOMEMLIMIT` and `GOGC` in any replacement `watchdog.kubernetes.env` list
to retain these settings.

### Logs gateway

The logs gateway accepts OTLP logs over gRPC at `nktp-gateway-logs-collector:4317`
and HTTP at `http://nktp-gateway-logs-collector:4318` in the Helm release namespace.
It forwards received logs to LGTM and sends its own metrics, logs and traces to
watchdog.

Use `gateway_logs.otelcol.config` for Collector configuration and
`gateway_logs.kubernetes` for Kubernetes workload configuration. Autoscaling
requires the Kubernetes resource metrics API. Retain `GOMEMLIMIT` and `GOGC` when
replacing `gateway_logs.kubernetes.env`.

### Metrics gateway

The metrics gateway accepts OTLP metrics over gRPC at `nktp-gateway-metrics-collector:4317`
and HTTP at `http://nktp-gateway-metrics-collector:4318` in the Helm release namespace.
It forwards received metrics to LGTM and sends its own metrics, logs and traces to
watchdog.

Use `gateway_metrics.otelcol.config` for Collector configuration and
`gateway_metrics.kubernetes` for Kubernetes workload configuration. Autoscaling
requires the Kubernetes resource metrics API. Retain `GOMEMLIMIT` and `GOGC` when
replacing `gateway_metrics.kubernetes.env`.

### Traces gateway

The traces gateway accepts OTLP traces over gRPC at `nktp-gateway-traces-collector:4317`
and HTTP at `http://nktp-gateway-traces-collector:4318` in the Helm release namespace.
It forwards received traces to LGTM and sends its own metrics, logs and traces to
watchdog.

Use `gateway_traces.otelcol.config` for Collector configuration and
`gateway_traces.kubernetes` for Kubernetes workload configuration. Autoscaling
requires the Kubernetes resource metrics API. Retain `GOMEMLIMIT` and `GOGC` when
replacing `gateway_traces.kubernetes.env`.

### Cluster events

The cluster event reader sends Kubernetes Event logs to the logs gateway. Two
replicas share one watch through leader election. Its own metrics, logs and
traces go to watchdog.

Use `cluster_events.otelcol.config` for Collector configuration and
`cluster_events.kubernetes` for Kubernetes workload configuration. Retain
`GOMEMLIMIT`, `GOGC` and `POD_NAMESPACE` when replacing its environment list.
Set `receivers.k8s_events.namespaces` within the Collector configuration to limit
the watched namespaces. Startup lists the retained Events, so large clusters
need memory sized for that list. Events during a leadership handoff can be missed.

The chart creates the election Lease in the release namespace and restricts the
reader's Lease permissions to that name. Set `extensions.k8s_leader_elector.lease_name`
in the Collector configuration to change the name. Use a literal name so Helm can
create the matching Lease. If using another Lease namespace, provision the Lease
and its RBAC there separately.

### kube-state-metrics

The DaemonSet under `kube-state-metrics.daemonset` collects scheduled Pods on its node.
Configure central workloads under `kube-state-metrics.deployments`. Each central workload's `resource_types`
selects the Kubernetes resource types to collect and grants matching RBAC.
Set `track_unscheduled_pods: true` to collect Pods without a node assignment.
This is supported only for central workloads and requires `resource_types` to be exactly `[pods]`.
Resource names come from the [upstream resource list](https://github.com/kubernetes/kube-state-metrics/blob/v2.20.0/docs/README.md#exposed-metrics).

`mutatingadmissionpolicies` and `mutatingadmissionpolicybindings` are disabled by
default because KSM uses their `admissionregistration.k8s.io/v1` APIs, which older
clusters do not serve. Add them to `resource_types` only when the cluster serves both APIs.

`kube-state-metrics.namespaces` sets the namespace scope unless a workload supplies
its own `namespaces` list. RBAC follows the effective namespace selection.
An empty list means all namespaces. Empty strings are ignored.

Secret metrics are opt-in because collection requires list/watch access to complete
Secret objects, including their contents. To enable them, set
`kube-state-metrics.deployments.configuration.resource_types` to `[configmaps, secrets]`.
Set that workload's `namespaces` list to limit access. An empty list grants access
across the cluster.

Shared `kubernetes` defaults can be overridden under each workload. `resources`,
`env` and `securityContext` configure the KSM container, `podSecurityContext`
configures Pod security, and remaining fields configure the Pod spec.
Set `serviceAccount` to use an existing account.

When overriding scheduling settings, ensure the node Collector runs on every node
where the KSM DaemonSet runs so it can scrape the local KSM endpoint.

Increase `shards` when a central workload exceeds its memory or scrape response
budget. Helm supplies `--shard` and `--total-shards`. Each shard runs as one
Deployment and briefly stops collection during replacement.

### Node collector

The node Collector accepts OTLP logs, metrics, and traces at
`nktp-daemonset-collector:4317` over gRPC and `http://nktp-daemonset-collector:4318`
over HTTP. The Service routes traffic only to the Collector on the sender's node.
If no local Collector is ready, traffic fails rather than reaching another node.
The Collector sets `k8s.node.name` and forwards each signal to its corresponding
gateway. Qualify the Service name with the NKTP namespace when
sending from another namespace.

The DaemonSet reads pod logs from `/var/log/pods` and sends them to the logs
gateway. A dedicated Target Allocator assigns monitored metric endpoints only
to collectors on the same node. Metrics go to
`http://nktp-gateway-metrics-collector:4318`, which requires the metrics gateway.
The `hostmetrics` receiver reads host `/proc` through a read-only mount at
`/hostfs/proc`. Only its `process` scraper is enabled. It collects per-process
CPU time, physical and virtual memory, and disk I/O every minute, and sends them
through the metrics gateway. Full process command lines are not exported.
Restricted processes can have missing metrics or attributes and produce scrape
errors. The collector does not gain additional Linux capabilities.
Node exporter remains responsible for other host metrics.

TODO. Add node and journal log collection.

TODO. Add control-plane log collection.

The collector's own metrics, logs and traces go to watchdog.
Each collector also discovers this DaemonSet's Target Allocator pods on its node
in the release namespace. It scrapes their HTTP metrics every 30 seconds through
`metrics/targetallocator` and sends them to watchdog. This pipeline uses its own
Prometheus receiver and does not depend on Target Allocator assignments.
The chart adds this pipeline after user configuration, with dedicated processors
and an exporter. Replacing `daemonset.otelcol.config` does not remove monitoring.
Its component names are reserved. A conflicting name causes rendering to fail.
A Target Allocator pod needs a DaemonSet collector on its node to be scraped.
The collector has read-only pod discovery permissions in the release namespace.

Use `daemonset.otelcol.config` for Collector configuration and
`daemonset.kubernetes` for Kubernetes workload configuration. The pod-log mount
is read-only, but host file permissions require the collector to run as root.
The file reader follows new records and does not persist offsets across restarts.
Retain `GOMEMLIMIT` and `GOGC` when replacing its environment list.
The chart adds `NODE_NAME` and `POD_NAMESPACE` if the list omits them.

Setting `daemonset.kubernetes.targetAllocator.enabled` to `false` will cause Helm
rendering to fail.

Monitoring CRs labeled `nktp.nvidia.io/large-target: "true"` are excluded.
Unlabeled monitors remain eligible. Targets without a matching collector node
remain unassigned.

The Target Allocator rejects ServiceMonitor and PodMonitor endpoints that reference
local bearer-token or TLS files in the Collector container.

### Large-target metrics

The large-target metrics pool (`large_target_metrics`) selects ServiceMonitor,
PodMonitor, Probe, and ScrapeConfig resources with the metadata label
`nktp.nvidia.io/large-target: "true"`.
Apply this label to monitoring resources for endpoints with large scrape
responses, such as kube-state-metrics.

A Target Allocator distributes scrapes across the pool's replicas. Default
discovery supports endpoints without Secret-backed credentials.
The Target Allocator rejects ServiceMonitor and PodMonitor endpoints that reference
local bearer-token or TLS files in the Collector container.
Setting `large_target_metrics.kubernetes.targetAllocator.enabled` to `false`
will cause Helm rendering to fail. Watchdog scrapes the Target Allocator's metrics.
The allocator has its own security contexts and resource budget under
`large_target_metrics.kubernetes.targetAllocator`. Size its resources for the
discovered Kubernetes objects and targets.

Collected metrics go to `http://nktp-gateway-metrics-collector:4318`.
Each Collector sends its own telemetry to watchdog.
Use `large_target_metrics.otelcol.config` for Collector internal configuration and
`large_target_metrics.kubernetes` for workload configuration.

This pool reserves more memory than the node DaemonSet for large scrape
responses. Autoscaling requires the Kubernetes resource metrics API and
distributes whole targets. An individual endpoint must still fit in one
Collector. Scrape defaults limit each response to 1,000,000 samples and 1 GiB.
Split endpoints that exceed these limits into smaller scrape targets where
possible. If raising the scrape limits, size the exporter queue, memory request
and limit, `GOMEMLIMIT`, and memory limiter thresholds together. Response bytes
do not directly measure the memory needed to parse and export a scrape.
Retain `GOMEMLIMIT` and `GOGC` when replacing environment lists.

## Backpressure

Collector pipelines use synchronous processors and batch data within exporter
queues. With `block_on_overflow: true`, full queues make incoming calls wait for
capacity, allowing downstream congestion to slow ingestion. The memory limiter
can refuse data under memory pressure, so receivers and upstream senders must
retry retryable failures. The Filelog receiver retries these failures until
recovery and drops errors marked permanent.

Each Collector exports its own telemetry through separate SDK buffering. Logs
and traces use bounded asynchronous queues, and metrics use a periodic reader.
This keeps monitoring congestion from blocking collection.

## Managed Dependencies

### OpenTelemetry and CRDs

NKTP depends on the published [opentelemetry-kube-stack 0.22.0](https://github.com/open-telemetry/opentelemetry-helm-charts/tree/opentelemetry-kube-stack-0.22.0/charts/opentelemetry-kube-stack)
chart. It supplies OpenTelemetry Operator chart 0.123.0, operator image 0.159.0,
and native CRDs. NKTP disables its default Collector and general Collector RBAC.
Settings pass through under `opentelemetry-kube-stack` using upstream values.

Helm installs missing OpenTelemetry CRDs (OpenTelemetryCollector, Instrumentation,
TargetAllocator, OpAMPBridge) and Prometheus CRDs (ServiceMonitor, PodMonitor, Probe,
ScrapeConfig) before the chart's resources.

The minimum supported API version for `OpenTelemetryCollector` resources is `opentelemetry.io/v1beta1`.

Use these independent settings when another mechanism manages the Operator or CRDs.

```yaml
opentelemetry-kube-stack:
  opentelemetry-operator:
    enabled: false
  crds:
    installOtel: false
    installPrometheus: false
```

Helm generates a self-signed webhook certificate and reuses it on upgrades. The
certificate expires after 365 days by default and is not renewed automatically.
Enable cert-manager when it is already installed to manage certificate renewal.

```yaml
opentelemetry-kube-stack:
  opentelemetry-operator:
    admissionWebhooks:
      certManager:
        enabled: true
```

Override the Operator image with
`opentelemetry-kube-stack.opentelemetry-operator.manager.image.tag`.

### Node exporter

NKTP installs [prometheus-node-exporter](https://github.com/prometheus-community/helm-charts/tree/main/charts/prometheus-node-exporter) by default
to collect host-level metrics from every node. It runs as a DaemonSet with host networking and binds port 9100 on each node,
and ships a ServiceMonitor so the exporter is discoverable as a scrape target.

Disable it, for example when the cluster already runs a node exporter:

```yaml
node-exporter:
  enabled: false
```

### Metrics server

[metrics-server](https://github.com/kubernetes-sigs/metrics-server) serves the Kubernetes
resource metrics API (`metrics.k8s.io`), which `kubectl top` and Horizontal Pod Autoscalers
read. It is what drives horizontal autoscaling of the three signal gateways, the
large-target metrics pool, and the watchdog: every workload with an `autoscaler` block
scales on CPU and memory utilization, which the HPA controller can only read from this API.

NKTP does **not** install it by default, because many clusters already get it from a
managed control plane or a cluster add-on, and a second copy conflicts over the
cluster-singleton `v1beta1.metrics.k8s.io` APIService. With no provider at all, those HPAs
cannot compute a recommendation and stop scaling, holding each workload at whatever
replica count it currently has. Enable it when nothing else supplies the API:

```yaml
metrics-server:
  enabled: true
```

The bundled chart is pinned to metrics-server chart 3.13.1, which installs metrics-server
0.8.1.

Settings pass through under `metrics-server` using the upstream chart's own values, none
of which NKTP overrides. See the [chart's values reference](https://github.com/kubernetes-sigs/metrics-server/blob/metrics-server-helm-chart-3.13.1/charts/metrics-server/README.md)
for what is configurable, including the TLS defaults for the APIService and for kubelet
certificate verification.

### otel-lgtm

[grafana/otel-lgtm](https://github.com/grafana/docker-otel-lgtm) is an all-in-one observability backend (Grafana, Loki, Tempo, Prometheus) with OTLP ingest.
NKTP deploys it by default so that telemetry is viewable immediately after install.
Prometheus is available on port 9090 of the `lgtm` Service, for example
`http://lgtm.nktp.svc:9090` when installed in namespace `nktp`. Grafana is served on port 3000 of the `lgtm` service, with anonymous admin access (no login).

The default **NKTP DaemonSet** dashboard shows node collector
throughput, failures, queues, CPU and memory, plus Target Allocator scrape health,
collector counts and target assignments. Collector panels use self metrics sent
through watchdog, as do Target Allocator metrics collected by the DaemonSet.
This does not require the metrics gateway. Target Allocator counts stay separate
for each replica. Disabling LGTM removes its dashboards. Target Allocator metrics
continue to flow to the backend configured in watchdog.

**Note:** This container is intended for kicking the tires only. It will not support substantial scale and MUST be disabled for production use:

```yaml
otel-lgtm:
  enabled: false
```
