# Getting started with NKTP

NKTP is one Helm chart that collects a Kubernetes cluster's metrics, logs and
traces and exports them over OTLP. This guide installs it, connects it to a
backend, and covers the settings that most often differ from one cluster to the
next. The [chart README](../nktp/README.md) is the reference for everything it
deploys and every value.

## Before you install

The full list is in [Prerequisites](../nktp/README.md#prerequisites). The ones
that most often need preparing:

- **cert-manager.** The default values need it. You can run without it; see
  [Install without cert-manager](../nktp/README.md#install-without-cert-manager).
- **The resource metrics API** (`metrics.k8s.io`), which the Collector
  autoscalers read. NKTP can install metrics-server if nothing provides it.
- **The Operator's admission webhook must be reachable** from the API server on
  TCP 9443. Managed clusters can block it with firewall rules or security groups.
- **Capacity.** The defaults are sized for large clusters: about 10 CPU and
  26 GiB of memory in requests for the cluster-wide components, plus about
  1.1 CPU and 2.1 GiB on every node. See
  [Resource sizing](../nktp/README.md#resource-sizing).

## Install

TODO: installation instructions for the GHCR vended chart

```bash

```

To install from a checkout instead, build the chart's dependencies first. Their
archives aren't committed:

```bash
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
helm dependency build nktp
kubectl create namespace nktp
kubectl label namespace nktp pod-security.kubernetes.io/enforce=privileged
helm install nktp ./nktp --namespace nktp --values my-values.yaml --wait
```

- **The namespace needs the `privileged` Pod Security level,** because the node
  Collector and node-exporter use host paths and namespaces. `--create-namespace`
  would create it without the label.
- **Wait twice.** `helm install --wait` returns once Helm's own workloads are
  ready, but the Operator creates the Collectors after that, so a green Helm
  install can still have crash-looping Collectors.
- **`my-values.yaml`** holds your settings from the sections below. Leave it
  out to install with the defaults.
- **Versions:** release tags and development builds are described in
  [RELEASING.md](../RELEASING.md).

## First look

By default everything exports to a bundled development backend, `lgtm`, in the
release namespace: Grafana, Prometheus, Loki and Tempo in one Pod. It keeps data
in memory and Grafana grants anonymous admin access, so it is for evaluation
only.

Grafana is on port 3000 of the `lgtm` Service, with Prometheus, Loki and Tempo
as data sources for metrics, logs and traces. Port-forward it, bound to
localhost:

```bash
kubectl -n nktp port-forward svc/lgtm 3000:3000 --address 127.0.0.1
```

See [the default dashboards](../nktp/dashboards/README.md#default-autoloaded-into-lgtm)
for the dashboards that are included by default.

## Set a backend

Everything leaves the cluster through four Collectors: the three gateways, one
per signal, and the watchdog, which carries the other Collectors' own
telemetry. Point all of them at your backend and turn the bundled one off. The
backend must accept OTLP over HTTP for metrics, logs and traces, and accept
delta-temporality metrics, which the Target Allocators send.

```yaml
otel-lgtm:
  enabled: false
watchdog:
  otelcol:
    config:
      exporters:
        otlp_http:
          endpoint: https://<backend>
      service:
        telemetry:
          metrics:
            readers:
              - periodic:
                  exporter:
                    otlp: {protocol: http/protobuf, endpoint: https://<backend>}
          logs:
            processors:
              - batch:
                  exporter:
                    otlp: {protocol: http/protobuf, endpoint: https://<backend>}
          traces:
            processors:
              - batch:
                  exporter:
                    otlp: {protocol: http/protobuf, endpoint: https://<backend>}
gateway_logs:
  otelcol: {config: {exporters: {otlp_http: {endpoint: https://<backend>}}}}
gateway_metrics:
  otelcol: {config: {exporters: {otlp_http: {endpoint: https://<backend>}}}}
gateway_traces:
  otelcol: {config: {exporters: {otlp_http: {endpoint: https://<backend>}}}}
```

- **TLS and mTLS** to the backend: see [Secure gateway export](gateway_mtls.md).
- **Credentials** go in a Secret, never in values. Add it to the Collector's
  `kubernetes.env` and reference it from the exporter with `${env:NAME}`, for
  example in `headers`. `env` is a list, so restate the chart's `GOMEMLIMIT`
  and `GOGC` entries.

## Scraping your services

NKTP collects every Pod's logs and state, and the nodes' metrics, without
being asked. A service's own metrics are scraped only when it has a
`ServiceMonitor` or `PodMonitor`, which can live in any namespace and needs no
NKTP-specific label. If a NetworkPolicy guards the metrics port, admit the
release namespace. A monitor that needs credentials references a Secret, which
the node Target Allocator reads only in the namespaces listed in
`daemonset.kubernetes.targetAllocator.prometheusCR.secretNamespaces`; see
[Node collector](../nktp/README.md#node-collector).

## Upgrading

Helm never upgrades CRDs it installed from a chart's `crds/` directory. The
OpenTelemetry and Prometheus CRDs come from there, so a chart version that bumps
the Operator leaves the old CRDs in place, and fields only the new version knows
are silently dropped. Apply the new CRDs before `helm upgrade`.
