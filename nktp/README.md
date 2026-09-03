# NKTP Helm Chart

NKTP is how NVIDIA runs OpenTelemetry on Kubernetes.

## OpenTelemetry Topology

TODO

## Managed Dependencies

### otel-lgtm

[grafana/otel-lgtm](https://github.com/grafana/docker-otel-lgtm) is an all-in-one observability backend (Grafana, Loki, Tempo, Prometheus) with OTLP ingest.
NKTP deploys it by default so that telemetry is viewable immediately after install. Grafana is served on port 3000 of the `lgtm` service, with anonymous admin access (no login).

**Note:** This container is intended for kicking the tires only. It will not support substantial scale and MUST be disabled for production use:

```yaml
otel-lgtm:
  enabled: false
```
