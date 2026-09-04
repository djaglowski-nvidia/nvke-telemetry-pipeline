# NKTP Helm Chart

NKTP is how NVIDIA runs OpenTelemetry on Kubernetes.

## OpenTelemetry Topology

TODO

## Managed Dependencies

### CRDs

NKTP installs the Prometheus monitoring CRDs (ServiceMonitor, PodMonitor, Probe, ScrapeConfig) by default,
using the [prometheus-operator-crds](https://github.com/prometheus-community/helm-charts/tree/main/charts/prometheus-operator-crds) chart.
`helm upgrade` keeps them up to date. They are never deleted on uninstall.

Disable them, for example when another mechanism manages them:

```yaml
prometheus-crds:
  install: false
```

### otel-lgtm

[grafana/otel-lgtm](https://github.com/grafana/docker-otel-lgtm) is an all-in-one observability backend (Grafana, Loki, Tempo, Prometheus) with OTLP ingest.
NKTP deploys it by default so that telemetry is viewable immediately after install. Grafana is served on port 3000 of the `lgtm` service, with anonymous admin access (no login).

**Note:** This container is intended for kicking the tires only. It will not support substantial scale and MUST be disabled for production use:

```yaml
otel-lgtm:
  enabled: false
```
