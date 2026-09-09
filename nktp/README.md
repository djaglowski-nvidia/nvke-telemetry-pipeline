# NKTP Helm Chart

NKTP is how NVIDIA runs OpenTelemetry on Kubernetes.

## OpenTelemetry Topology

TODO

## Managed Dependencies

### CRDs

NKTP installs the OpenTelemetry CRDs (OpenTelemetryCollector, Instrumentation, TargetAllocator, OpAMPBridge)
by default through the bundled OpenTelemetry Operator chart. Helm upgrades update them.
The CRDs remain when NKTP is uninstalled.

Set `opentelemetry-operator.crds.create` to `false` when another mechanism manages these CRDs.
The operator remains enabled.

```yaml
opentelemetry-operator:
  crds:
    create: false
```

NKTP installs the Prometheus monitoring CRDs (ServiceMonitor, PodMonitor, Probe, ScrapeConfig) by default,
using the [prometheus-operator-crds](https://github.com/prometheus-community/helm-charts/tree/main/charts/prometheus-operator-crds) chart.
`helm upgrade` keeps them up to date. They are never deleted on uninstall.

Disable them, for example when another mechanism manages them:

```yaml
prometheus-crds:
  install: false
```

### opentelemetry-operator

The [OpenTelemetry Operator](https://github.com/open-telemetry/opentelemetry-operator) manages the
OpenTelemetryCollector CRs that define NKTP workloads. NKTP bundles
[operator chart 0.122.0](https://github.com/open-telemetry/opentelemetry-helm-charts/tree/opentelemetry-operator-0.122.0/charts/opentelemetry-operator),
which uses operator image 0.158.0 by default. Configuration passes through under the
`opentelemetry-operator` key using the upstream chart's conventions.

Use `manager.image.tag` to override the operator version.

```yaml
opentelemetry-operator:
  manager:
    image:
      tag: "<version>"
```

Set `opentelemetry-operator.enabled` to `false` when an existing operator manages the cluster.
This also disables installation of the OpenTelemetry CRDs.

Helm generates a self-signed certificate for the admission webhooks by default
and reuses it on upgrades. This keeps the certificate and webhook CA consistent
when you roll back between these revisions. The certificate expires after 365 days
by default and is not renewed automatically. If cert-manager is installed, enable
it to manage certificate renewal.

```yaml
opentelemetry-operator:
  admissionWebhooks:
    certManager:
      enabled: true
```

### otel-lgtm

[grafana/otel-lgtm](https://github.com/grafana/docker-otel-lgtm) is an all-in-one observability backend (Grafana, Loki, Tempo, Prometheus) with OTLP ingest.
NKTP deploys it by default so that telemetry is viewable immediately after install. Grafana is served on port 3000 of the `lgtm` service, with anonymous admin access (no login).

**Note:** This container is intended for kicking the tires only. It will not support substantial scale and MUST be disabled for production use:

```yaml
otel-lgtm:
  enabled: false
```
