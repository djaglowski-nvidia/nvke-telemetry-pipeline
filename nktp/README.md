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

## Managed Dependencies

### OpenTelemetry and CRDs

NKTP depends on the published [opentelemetry-kube-stack 0.20.8](https://github.com/open-telemetry/opentelemetry-helm-charts/tree/opentelemetry-kube-stack-0.20.8/charts/opentelemetry-kube-stack)
chart. It supplies OpenTelemetry Operator chart 0.119.0, operator image 0.154.0,
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

### otel-lgtm

[grafana/otel-lgtm](https://github.com/grafana/docker-otel-lgtm) is an all-in-one observability backend (Grafana, Loki, Tempo, Prometheus) with OTLP ingest.
NKTP deploys it by default so that telemetry is viewable immediately after install. Grafana is served on port 3000 of the `lgtm` service, with anonymous admin access (no login).

**Note:** This container is intended for kicking the tires only. It will not support substantial scale and MUST be disabled for production use:

```yaml
otel-lgtm:
  enabled: false
```
